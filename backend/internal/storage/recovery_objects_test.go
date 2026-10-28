package storage

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryObjectsDiscardOnlyThisAttempt(t *testing.T) {
	ctx := context.Background()
	blobs := LocalBlobs{Root: t.TempDir()}
	source, target := strings.Repeat("a", 32), strings.Repeat("b", 32)
	original, err := blobs.PutObject(ctx, source, strings.NewReader("original author text"))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewRecoveryObjects(ctx, blobs, target)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	written, err := scope.PutObject(ctx, target, strings.NewReader("copied text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.PutObject(ctx, source, strings.NewReader("forbidden source write")); !errors.Is(err, ErrGCConflict) {
		t.Fatal("copy scope wrote to another project", err)
	}
	// Even unexpected files in the target are not owned by this compensation.
	unknown, err := blobs.PutObject(ctx, target, strings.NewReader("unrelated file"))
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Discard(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.ReadObject(ctx, target, written.Hash); err == nil {
		t.Fatal("copy file survived definite rollback")
	}
	for _, item := range []struct{ project, hash string }{{source, original.Hash}, {target, unknown.Hash}} {
		if _, err := blobs.ReadObject(ctx, item.project, item.hash); err != nil {
			t.Fatal("compensation deleted unrelated material", err)
		}
	}
	if err := scope.Discard(ctx); err != nil {
		t.Fatal("compensation was not idempotent", err)
	}
	if _, err := NewRecoveryObjects(ctx, blobs, target); !errors.Is(err, ErrGCConflict) {
		t.Fatal("existing namespace was claimed by another recovery", err)
	}
}

func TestRecoveryObjectsNeverDiscardAfterCommitAttempt(t *testing.T) {
	ctx := context.Background()
	// An empty author's first project may not have created its blob directory.
	blobs := LocalBlobs{Root: filepath.Join(t.TempDir(), "objects")}
	target := strings.Repeat("b", 32)
	scope, err := NewRecoveryObjects(ctx, blobs, target)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	ref, err := scope.PutObject(ctx, target, strings.NewReader("possibly committed copy"))
	if err != nil {
		t.Fatal(err)
	}
	scope.BeginCommit()
	if err := scope.Discard(ctx); !errors.Is(err, ErrGCConflict) {
		t.Fatal("unknown commit allowed deletion", err)
	}
	if _, err := scope.PutObject(ctx, target, strings.NewReader("late write")); !errors.Is(err, ErrGCConflict) {
		t.Fatal("late write admitted after commit began", err)
	}
	if _, err := blobs.ReadObject(ctx, target, ref.Hash); err != nil {
		t.Fatal("uncertain commit lost material", err)
	}
}

type recoveryLostWriteResponse struct{ recoveryStore }

func (s recoveryLostWriteResponse) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	_, err := s.recoveryStore.PutObject(ctx, project, input)
	if err != nil {
		return BlobRef{}, err
	}
	return BlobRef{}, errors.New("test write response lost")
}

func TestRecoveryObjectsTrackWritesWhoseResponseWasLost(t *testing.T) {
	ctx := context.Background()
	blobs := LocalBlobs{Root: t.TempDir()}
	target := strings.Repeat("b", 32)
	scope, err := NewRecoveryObjects(ctx, blobs, target)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	scope.store = recoveryLostWriteResponse{scope.store}
	if _, err := scope.PutObject(ctx, target, strings.NewReader("written without response")); err == nil {
		t.Fatal("write failure was hidden")
	}
	if err := scope.Discard(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blobs.ScanObjects(ctx, target)
	if err != nil || len(objects) != 0 {
		t.Fatal("unacknowledged write left unreachable text", objects, err)
	}
}

func TestRecoveryObjectsRequireJournalBeforePhysicalWrite(t *testing.T) {
	ctx := context.Background()
	blobs := LocalBlobs{Root: t.TempDir()}
	target := strings.Repeat("b", 32)
	scope, err := NewRecoveryObjects(ctx, blobs, target)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	calls := 0
	if _, err := scope.Journal(func(context.Context, BlobRef) error {
		calls++
		return errors.New("test journal unavailable")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.PutObject(ctx, target, strings.NewReader("must not become an untracked file")); err == nil {
		t.Fatal("write bypassed journal failure")
	}
	objects, err := blobs.ScanObjects(ctx, target)
	if err != nil || calls != 1 || len(objects) != 0 {
		t.Fatal("bytes were written before durable journal", calls, objects, err)
	}
}
