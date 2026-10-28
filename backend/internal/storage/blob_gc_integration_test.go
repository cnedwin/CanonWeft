package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3BlobRetentionIntegration(t *testing.T) {
	if os.Getenv("STUDIO_TEST_S3_BUCKET") != "studio-test" || os.Getenv("STUDIO_TEST_S3_ENDPOINT") != "http://127.0.0.1:18333" {
		t.Fatal("explicit local isolated S3 test configuration required")
	}
	config := S3Config{Endpoint: os.Getenv("STUDIO_TEST_S3_ENDPOINT"), Region: "us-east-1", Bucket: "studio-test", AccessKey: os.Getenv("STUDIO_TEST_S3_ACCESS"), SecretKey: os.Getenv("STUDIO_TEST_S3_SECRET")}
	store, err := NewS3RetentionStore(config)
	if err != nil {
		t.Fatal(err)
	}
	var identity [16]byte
	if _, err = rand.Read(identity[:]); err != nil {
		t.Fatal(err)
	}
	project := hex.EncodeToString(identity[:])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var refs []BlobRef
	for _, text := range []string{"expiry integration owned target", "expiry integration keep neighbor"} {
		ref, err := store.PutObject(ctx, project, bytes.NewBufferString(text))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			if _, err := store.blobs.client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(project + "/" + ref.Hash)}); err != nil {
				t.Error("owned S3 test object cleanup failed")
			}
		})
	}
	readConfig := config
	readConfig.AccessKey = os.Getenv("STUDIO_TEST_S3_READ_ACCESS")
	readConfig.SecretKey = os.Getenv("STUDIO_TEST_S3_READ_SECRET")
	readStore, err := NewS3RetentionStore(readConfig)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := readStore.ScanObjects(ctx, project)
	if err != nil || len(objects) != 2 {
		t.Fatal("actual read-only S3 inventory failed", err)
	}
	wanted := map[string]int64{refs[0].Hash: refs[0].Size, refs[1].Hash: refs[1].Size}
	for _, object := range objects {
		if size, ok := wanted[object.Hash]; !ok || size != object.Size {
			t.Fatal("actual S3 inventory contains an unrelated or incorrect object")
		}
	}
	if store.RetentionNamespace() != readStore.RetentionNamespace() {
		t.Fatal("same storage location has different identity")
	}
	if result, err := readStore.DeleteObject(ctx, project, refs[0].Hash); err == nil || result != "" {
		t.Fatal("read-only credentials received a deletion receipt")
	}
	if _, err := store.ReadObject(ctx, project, refs[0].Hash); err != nil {
		t.Fatal("denied deletion removed content", err)
	}
	if result, err := store.DeleteObject(ctx, project, refs[0].Hash); err != nil || result != GCRemoved {
		t.Fatalf("actual S3 conditional deletion: result=%s error=%v", result, err)
	}
	if _, err := store.ReadObject(ctx, project, refs[0].Hash); err == nil {
		t.Fatal("deleted object remains readable")
	}
	if _, err := store.ReadObject(ctx, project, refs[1].Hash); err != nil {
		t.Fatal("neighbor object was affected", err)
	}
	remaining, err := readStore.ScanObjects(ctx, project)
	if err != nil || len(remaining) != 1 || remaining[0].Hash != refs[1].Hash {
		t.Fatal("actual S3 inventory did not observe current physical state", err)
	}
	if result, err := store.DeleteObject(ctx, project, refs[0].Hash); err != nil || result != GCAbsent {
		t.Fatalf("idempotent absent result=%s error=%v", result, err)
	}
}
