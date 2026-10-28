package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalBlobGCDeletesOnlyVerifiedExactProjectObject(t *testing.T) {
	root := t.TempDir()
	store := LocalBlobs{Root: root}
	ctx := context.Background()
	project, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	ref, err := store.PutObject(ctx, project, strings.NewReader("owned object"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(ctx, other, strings.NewReader("owned object")); err != nil {
		t.Fatal(err)
	}
	keep, err := store.PutObject(ctx, project, strings.NewReader("retained object"))
	if err != nil {
		t.Fatal(err)
	}
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	result, err := gc.DeleteObject(ctx, project, ref.Hash)
	if err != nil || result != GCRemoved {
		t.Fatal("exact object was not removed", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, project, ref.Hash)); !os.IsNotExist(err) {
		t.Fatal("object still present", err)
	}
	for _, key := range []struct{ project, hash string }{{other, ref.Hash}, {project, keep.Hash}} {
		if _, err := store.ReadObject(ctx, key.project, key.hash); err != nil {
			t.Fatal("unrelated object deleted", err)
		}
	}
	result, err = gc.DeleteObject(ctx, project, ref.Hash)
	if err != nil || result != GCAbsent {
		t.Fatal("repeat delete not idempotently absent", result, err)
	}
}

func TestLocalBlobGCRejectsUnsafeKeysCancellationAndCorruption(t *testing.T) {
	root := t.TempDir()
	store := LocalBlobs{Root: root}
	project := strings.Repeat("a", 32)
	ref, err := store.PutObject(context.Background(), project, strings.NewReader("private original"))
	if err != nil {
		t.Fatal(err)
	}
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	for _, key := range []struct{ project, hash string }{{"../outside", ref.Hash}, {project, ""}, {project, "../outside"}, {strings.ToUpper(project), ref.Hash}, {project, strings.ToUpper(ref.Hash)}} {
		if _, err := gc.DeleteObject(context.Background(), key.project, key.hash); !errors.Is(err, ErrGCInvalidKey) {
			t.Fatal("unsafe GC key not rejected", key, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gc.DeleteObject(ctx, project, ref.Hash); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel not respected", err)
	}
	if _, err := store.ReadObject(context.Background(), project, ref.Hash); err != nil {
		t.Fatal("canceled operation deleted object", err)
	}
	if err := os.WriteFile(filepath.Join(root, project, ref.Hash), []byte("corrupt PRIVATE_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.DeleteObject(context.Background(), project, ref.Hash); !errors.Is(err, ErrGCConflict) || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatal("corruption not safely rejected", err)
	}
	if _, err := os.Stat(filepath.Join(root, project, ref.Hash)); err != nil {
		t.Fatal("corrupt unverified object deleted", err)
	}
}

func TestLocalBlobGCDoesNotCreateMissingRootOrDeleteDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := NewLocalBlobGC(missing); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("collector created a root")
	}
	root := t.TempDir()
	project, hash := strings.Repeat("a", 32), strings.Repeat("b", 64)
	if err := os.MkdirAll(filepath.Join(root, project, hash), 0700); err != nil {
		t.Fatal(err)
	}
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	if _, err := gc.DeleteObject(context.Background(), project, hash); !errors.Is(err, ErrGCConflict) {
		t.Fatal("directory accepted as object", err)
	}
	if _, err := os.Stat(filepath.Join(root, project, hash)); err != nil {
		t.Fatal("collector removed directory", err)
	}
}
