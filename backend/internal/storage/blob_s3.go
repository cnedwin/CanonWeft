package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3Blobs struct {
	client    *s3.Client
	bucket    string
	namespace string
}
type S3Config struct{ Endpoint, Region, Bucket, AccessKey, SecretKey string }

func NewS3Blobs(config S3Config) (*S3Blobs, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" {
		return nil, errors.New("invalid S3 endpoint")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost" || endpoint.Hostname() == "::1")) {
		return nil, errors.New("S3 requires TLS outside loopback")
	}
	if config.Region == "" || config.Bucket == "" || strings.ContainsAny(config.Bucket, "/\\") || config.AccessKey == "" || config.SecretKey == "" {
		return nil, errors.New("incomplete S3 configuration")
	}
	client := s3.New(s3.Options{Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""), RetryMaxAttempts: 1, HTTPClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	encoded, _ := json.Marshal([]string{"s3-retention-v1", config.Endpoint, config.Region, config.Bucket})
	return &S3Blobs{client: client, bucket: config.Bucket, namespace: contentHash(encoded)}, nil
}
func (s *S3Blobs) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	if !validBlobKey(project, "") {
		return BlobRef{}, errors.New("invalid project reference")
	}
	data, err := readBounded(ctx, input)
	if err != nil {
		return BlobRef{}, err
	}
	ref := BlobRef{Hash: contentHash(data), Size: int64(len(data))}
	checksum := sha256.Sum256(data)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(project + "/" + ref.Hash), Body: bytes.NewReader(data), ContentLength: aws.Int64(ref.Size), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum[:]))})
	if err != nil {
		var apiError smithy.APIError
		if !errors.As(err, &apiError) || (apiError.ErrorCode() != "PreconditionFailed" && apiError.ErrorCode() != "ConditionalRequestConflict") {
			return BlobRef{}, errors.New("S3 object write failed")
		}
	}
	existing, readErr := s.ReadObject(ctx, project, ref.Hash)
	if readErr != nil || !bytes.Equal(existing, data) {
		return BlobRef{}, errors.New("S3 object verification failed")
	}
	return ref, nil
}
func (s *S3Blobs) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	if !validBlobKey(project, hash) || hash == "" {
		return nil, errors.New("invalid blob reference")
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(project + "/" + hash)})
	if err != nil {
		return nil, errors.New("S3 object read failed")
	}
	defer result.Body.Close()
	data, err := readBounded(ctx, result.Body)
	if err != nil {
		return nil, err
	}
	if contentHash(data) != hash {
		return nil, errors.New("S3 object checksum mismatch")
	}
	return data, nil
}
