package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testBlobContract(t *testing.T, store BlobStore) {
	t.Helper()
	ctx := context.Background()
	project := strings.Repeat("a", 32)
	data := []byte("星河\n不可变的正文 🌙")
	ref, err := store.PutObject(ctx, project, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutObject(ctx, project, bytes.NewReader(data))
	if err != nil || second != ref {
		t.Fatal("idempotent put", err)
	}
	got, err := store.ReadObject(ctx, project, ref.Hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("read mismatch", err)
	}
	if _, err = store.ReadObject(ctx, strings.Repeat("b", 32), ref.Hash); err == nil {
		t.Fatal("cross-project object readable")
	}
	if _, err = store.PutObject(ctx, "../outside", bytes.NewReader(data)); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err = store.PutObject(ctx, project, io.LimitReader(&endless{}, MaxBlobBytes+1)); err == nil {
		t.Fatal("oversized object accepted")
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.PutObject(ctx, project, bytes.NewReader(data))
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("concurrent immutable write", err)
		}
	}
}

type endless struct{}

func (*endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
func TestLocalBlobContract(t *testing.T) { testBlobContract(t, LocalBlobs{Root: t.TempDir()}) }
func TestBlobCorruptionAndEscapeRejected(t *testing.T) {
	root := t.TempDir()
	s := LocalBlobs{Root: root}
	ctx := context.Background()
	project := strings.Repeat("c", 32)
	ref, err := s.PutObject(ctx, project, strings.NewReader("original"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, project, ref.Hash), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadObject(ctx, project, ref.Hash); err == nil {
		t.Fatal("corrupt object returned")
	}
	if _, err = s.PutObject(ctx, project, strings.NewReader("original")); err == nil {
		t.Fatal("corrupt object silently overwritten")
	}
}
