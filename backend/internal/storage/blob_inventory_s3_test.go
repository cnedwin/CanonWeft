package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type inventoryHTTP struct {
	t             *testing.T
	mode, project string
	pages, reads  int
}

func (f *inventoryHTTP) Do(r *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	}
	if r.Method != "GET" {
		f.t.Fatal("inventory attempted a mutation", r.Method)
	}
	if r.URL.Query().Has("versioning") {
		status := ""
		if f.mode == "versioned" || (f.mode == "version changed" && f.pages > 0) {
			status = "Enabled"
		}
		if f.mode == "suspended" {
			status = "Suspended"
		}
		return respond(200, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>`+status+`</Status></VersioningConfiguration>`)
	}
	if r.URL.Query().Get("list-type") == "2" {
		f.pages++
		if r.URL.Path != "/inventory-test" || r.URL.Query().Get("prefix") != f.project+"/" || r.URL.Query().Get("max-keys") != "1000" || r.URL.Query().Has("delimiter") {
			f.t.Fatal("unscoped inventory request")
		}
		if f.mode == "denied" || (f.mode == "late denied" && f.pages > 1) {
			return respond(403, `<Error><Code>AccessDenied</Code><Message>PRIVATE_CANARY</Message></Error>`)
		}
		if f.mode == "empty" {
			return respond(200, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
		}
		if f.mode == "missing completion" {
			return respond(200, `<ListBucketResult></ListBucketResult>`)
		}
		if f.pages > 3 {
			f.t.Fatal("repeated pagination did not stop")
		}
		if f.pages > 1 && r.URL.Query().Get("continuation-token") != "page-2" {
			f.t.Fatal("lost continuation token")
		}
		body := "first"
		if f.pages > 1 {
			body = "second"
		}
		key := f.project + "/" + contentHash([]byte(body))
		size := len(body)
		if f.mode == "duplicate" && f.pages > 1 {
			key = f.project + "/" + contentHash([]byte("first"))
			size = len("first")
		}
		if f.mode == "outside key" {
			key = strings.Repeat("b", 32) + "/" + contentHash([]byte(body))
		}
		if f.mode == "unknown key" {
			key = f.project + "/prepared-interrupted"
		}
		if f.mode == "wrong size" {
			size++
		}
		truncated, token := f.pages == 1, "page-2"
		if f.mode == "repeated token" {
			truncated = true
		}
		if f.mode == "missing token" {
			token = ""
		}
		next := ""
		if truncated {
			next = "<NextContinuationToken>" + token + "</NextContinuationToken>"
		}
		return respond(200, fmt.Sprintf(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>%t</IsTruncated>%s<Contents><Key>%s</Key><Size>%d</Size><LastModified>2000-01-01T00:00:00Z</LastModified></Contents></ListBucketResult>`, truncated, next, key, size))
	}
	f.reads++
	for _, body := range []string{"first", "second"} {
		if r.URL.Path == "/inventory-test/"+f.project+"/"+contentHash([]byte(body)) {
			if f.mode == "corrupt" {
				body = "PRIVATE_CANARY"
			}
			return respond(200, body)
		}
	}
	f.t.Fatal("unexpected inventory object read")
	return nil, fmt.Errorf("unexpected inventory request")
}

func TestS3BlobInventoryPaginatesVerifiesAndRejectsIncompleteResults(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "denied", "late denied", "versioned", "suspended", "version changed", "outside key", "unknown key", "wrong size", "duplicate", "missing token", "missing completion", "repeated token", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &inventoryHTTP{t: t, mode: mode, project: strings.Repeat("a", 32)}
			store := &S3Blobs{bucket: "inventory-test", client: s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String("https://example.invalid"), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: fixture, RetryMaxAttempts: 1})}
			reader, ok := any(store).(inventoryReader)
			if !ok {
				t.Fatal("S3 physical inventory is not implemented")
			}
			objects, err := reader.ScanObjects(context.Background(), fixture.project)
			if mode == "valid" {
				if err != nil || len(objects) != 2 || fixture.pages != 2 || fixture.reads != 2 || objects[0].Hash >= objects[1].Hash {
					t.Fatal("incomplete paginated inventory", objects, err)
				}
			} else if mode == "empty" {
				if err != nil || len(objects) != 0 || fixture.reads != 0 || fixture.pages != 1 {
					t.Fatal("invalid empty inventory", err)
				}
			} else if err == nil || objects != nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
				t.Fatal("unsafe or partial S3 inventory accepted", err)
			}
		})
	}
}
