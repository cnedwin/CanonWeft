package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Implements the SDK HTTP boundary without opening a listener or socket.
type gcHTTP struct {
	t                 *testing.T
	mode              string
	body, key         string
	deleted           bool
	deletes, requests int
}

func (f *gcHTTP) Do(r *http.Request) (*http.Response, error) {
	f.requests++
	respond := func(status int, body string, headers http.Header) (*http.Response, error) {
		if headers == nil {
			headers = http.Header{}
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	}
	errorResponse := func(code string) (*http.Response, error) {
		return respond(403, `<Error><Code>`+code+`</Code><Message>PRIVATE_CANARY</Message></Error>`, nil)
	}
	if r.URL.Query().Has("versioning") {
		if r.Method != "GET" || r.URL.Path != "/gc-test" {
			f.t.Fatal("unexpected bucket request", r.Method, r.URL.Path)
		}
		if f.mode == "bucket denied" {
			return errorResponse("AccessDenied")
		}
		status := ""
		if f.mode == "versioned" || f.mode == "version race" && f.deleted {
			status = "Enabled"
		}
		if f.mode == "suspended" {
			status = "Suspended"
		}
		return respond(200, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>`+status+`</Status></VersioningConfiguration>`, nil)
	}
	query := r.URL.Query()
	operation := query.Get("x-id")
	query.Del("x-id")
	if r.URL.Path != "/gc-test/"+f.key || len(query) != 0 || (operation != "GetObject" && operation != "DeleteObject") {
		f.t.Fatal("collector enumerated or addressed another key", r.Method, r.URL.String())
	}
	switch r.Method {
	case "GET":
		if f.mode == "read denied" {
			return errorResponse("AccessDenied")
		}
		if f.mode == "missing bucket" {
			return respond(404, `<Error><Code>NoSuchBucket</Code></Error>`, nil)
		}
		if f.deleted || f.mode == "missing" {
			return respond(404, `<Error><Code>NoSuchKey</Code></Error>`, nil)
		}
		headers := http.Header{"Etag": []string{`"verified-etag"`}}
		if f.mode == "missing etag" {
			headers.Del("Etag")
		}
		if f.mode == "object locked" {
			headers.Set("X-Amz-Object-Lock-Mode", "GOVERNANCE")
		}
		body := f.body
		if f.mode == "corrupt" {
			body = "changed"
		}
		return respond(200, body, headers)
	case "DELETE":
		f.deletes++
		if r.Header.Get("If-Match") != `"verified-etag"` || r.Header.Get("X-Amz-Bypass-Governance-Retention") != "" {
			f.t.Fatal("delete omitted exact precondition or bypassed retention")
		}
		if f.mode == "delete denied" {
			return errorResponse("AccessDenied")
		}
		if f.mode != "still present" {
			f.deleted = true
		}
		return respond(204, "", nil)
	default:
		f.t.Fatal("unexpected S3 method", r.Method)
	}
	return nil, errors.New("unreachable")
}
func fixtureS3GC(t *testing.T, mode string) (*S3BlobGC, *gcHTTP, string, string) {
	t.Helper()
	project := strings.Repeat("a", 32)
	body := "exact immutable object"
	hash := contentHash([]byte(body))
	f := &gcHTTP{t: t, mode: mode, body: body, key: project + "/" + hash}
	client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("https://gc.invalid"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: f})
	gc, err := NewS3BlobGC(&S3Blobs{client: client, bucket: "gc-test"})
	if err != nil {
		t.Fatal(err)
	}
	return gc, f, project, hash
}

func TestS3BlobGCExactConditionalDeleteAndAbsentWithoutNetwork(t *testing.T) {
	gc, f, project, hash := fixtureS3GC(t, "")
	result, err := gc.DeleteObject(context.Background(), project, hash)
	if err != nil || result != GCRemoved || f.deletes != 1 {
		t.Fatal("conditional physical deletion failed", result, err, f.deletes)
	}
	result, err = gc.DeleteObject(context.Background(), project, hash)
	if err != nil || result != GCAbsent || f.deletes != 1 {
		t.Fatal("retry was not verified absent", result, err, f.deletes)
	}
}
func TestS3BlobGCRefusesVersionsDeniedReadsCorruptionAndUnconfirmedDelete(t *testing.T) {
	for _, mode := range []string{"versioned", "suspended", "bucket denied", "read denied", "missing bucket", "missing etag", "corrupt", "object locked", "delete denied", "still present", "version race"} {
		t.Run(mode, func(t *testing.T) {
			gc, f, project, hash := fixtureS3GC(t, mode)
			if result, err := gc.DeleteObject(context.Background(), project, hash); err == nil || result != "" || strings.Contains(err.Error(), "PRIVATE_CANARY") {
				t.Fatal("unsafe backend counted as clean or leaked provider error", result, err)
			}
			want := 0
			if mode == "delete denied" || mode == "still present" || mode == "version race" {
				want = 1
			}
			if f.deletes != want {
				t.Fatal("unexpected delete request", f.deletes, want)
			}
		})
	}
}
func TestS3BlobGCInvalidKeysAndCancellationNeverReachTransport(t *testing.T) {
	gc, f, project, hash := fixtureS3GC(t, "")
	if _, err := gc.DeleteObject(context.Background(), "../outside", hash); !errors.Is(err, ErrGCInvalidKey) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gc.DeleteObject(ctx, project, hash); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.requests != 0 {
		t.Fatal("invalid request reached S3")
	}
}
