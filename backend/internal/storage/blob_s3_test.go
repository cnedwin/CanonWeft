package storage

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3BlobContractAndReadOnlyPermission(t *testing.T) {
	if os.Getenv("STUDIO_TEST_S3_BUCKET") != "studio-test" || os.Getenv("STUDIO_TEST_S3_ENDPOINT") != "http://127.0.0.1:18333" {
		t.Fatal("explicit local isolated S3 test configuration required")
	}
	config := S3Config{Endpoint: os.Getenv("STUDIO_TEST_S3_ENDPOINT"), Region: "us-east-1", Bucket: "studio-test", AccessKey: os.Getenv("STUDIO_TEST_S3_ACCESS"), SecretKey: os.Getenv("STUDIO_TEST_S3_SECRET")}
	store, err := NewS3Blobs(config)
	if err != nil {
		t.Fatal(err)
	}
	testBlobContract(t, store)
	project := strings.Repeat("d", 32)
	ref, err := store.PutObject(context.Background(), project, bytes.NewBufferString("read-only-proof"))
	if err != nil {
		t.Fatal(err)
	}
	config.AccessKey = os.Getenv("STUDIO_TEST_S3_READ_ACCESS")
	config.SecretKey = os.Getenv("STUDIO_TEST_S3_READ_SECRET")
	reader, err := NewS3Blobs(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadObject(context.Background(), project, ref.Hash); err != nil {
		t.Fatal("read permission failed", err)
	}
	if _, err = reader.PutObject(context.Background(), project, bytes.NewBufferString("forbidden")); err == nil {
		t.Fatal("readonly credential wrote object")
	}
	if _, err = reader.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(project + "/" + ref.Hash)}); err == nil {
		t.Fatal("readonly credential deleted object")
	}
	if _, err = store.ReadObject(context.Background(), project, ref.Hash); err != nil {
		t.Fatal("object lost after denied delete", err)
	}
}
