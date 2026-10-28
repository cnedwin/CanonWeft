package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRetentionStoreBindsIdentityAndClosedAuthority(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalRetentionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project := strings.Repeat("a", 32)
	ctx := context.Background()
	ref, err := store.PutObject(ctx, project, strings.NewReader("bound store"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewLocalRetentionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.RetentionNamespace() != store.RetentionNamespace() {
		t.Fatal("restart changed namespace")
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := again.DeleteObject(ctx, project, ref.Hash); err == nil {
		t.Fatal("closed store still deletes")
	}
	if _, err := again.ScanObjects(ctx, project); err == nil {
		t.Fatal("closed store still inventories")
	}
	if result, err := store.DeleteObject(ctx, project, ref.Hash); err != nil || result != GCRemoved {
		t.Fatal(result, err)
	}
	if objects, err := store.ScanObjects(ctx, project); err != nil || len(objects) != 0 {
		t.Fatal(objects, err)
	}
	// Losing/replacing the persistent identity must invalidate a claim even
	// after restart at the same path. It must never look like confirmed absence.
	oldNamespace := store.RetentionNamespace()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".retention-store-id"), filepath.Join(root, "old-identity")); err != nil {
		t.Fatal(err)
	}
	replaced, err := NewLocalRetentionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer replaced.Close()
	if replaced.RetentionNamespace() == oldNamespace {
		t.Fatal("replaced identity reused old deletion authority")
	}
}

func TestS3RetentionNamespaceBindsLocationNotCredentials(t *testing.T) {
	config := S3Config{Endpoint: "https://example.invalid", Region: "test", Bucket: "one", AccessKey: "private-access", SecretKey: "private-secret"}
	a, err := NewS3RetentionStore(config)
	if err != nil {
		t.Fatal(err)
	}
	config.SecretKey = "rotated-secret"
	b, err := NewS3RetentionStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if a.RetentionNamespace() != b.RetentionNamespace() {
		t.Fatal("credential rotation invalidated location")
	}
	writer, err := NewS3Blobs(config)
	if err != nil {
		t.Fatal(err)
	}
	bound, namespace, closeBound, err := writer.BindRecreationWrite()
	if err != nil || bound != writer || namespace != a.RetentionNamespace() || closeBound != nil {
		t.Fatal("ordinary writer and cleanup disagree on namespace", err)
	}
	config.Bucket = "two"
	c, err := NewS3RetentionStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if a.RetentionNamespace() == c.RetentionNamespace() {
		t.Fatal("different bucket has same namespace")
	}
	if len(a.RetentionNamespace()) != 64 {
		t.Fatal("namespace exposes configuration")
	}
}
