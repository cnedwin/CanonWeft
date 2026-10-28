package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func protectionFixture(t *testing.T, at time.Time) (string, Source) {
	t.Helper()
	a := emptyArchive(t)
	a.Manifest.SnapshotAt = at
	p := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(p, archiveBytes(t, a), 0600); err != nil {
		t.Fatal(err)
	}
	return p, a.Manifest.Source
}

func TestProtectionCommitsVerifiedRecoveryPointsAndDetectsCorruption(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	p, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Import(context.Background(), p, now, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(context.Background(), now.Add(10*time.Minute))
	if err != nil || status.State != "CURRENT" || len(status.Records) != 1 {
		t.Fatalf("committed point unavailable: %+v, %v", status, err)
	}
	status, err = store.Status(context.Background(), now.Add(16*time.Minute))
	if err != nil || status.State != "STALE" {
		t.Fatalf("old point reported current: %+v %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(store.Path, record.ID+".zip"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(context.Background(), record.ID); err == nil {
		t.Fatal("corrupt recovery point was accepted")
	}
}

func TestProtectionRetentionExpiresLastPointWithoutTouchingUnknownFiles(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	p, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, err := store.Import(context.Background(), p, now, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(store.Path, "personal-notes.txt")
	if err := os.WriteFile(unknown, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.Prune(context.Background(), now.Add(35*24*time.Hour-time.Second)); err != nil || deleted != 0 {
		t.Fatalf("pruned too early: %d %v", deleted, err)
	}
	if deleted, err := store.Prune(context.Background(), now.Add(35*24*time.Hour)); err != nil || deleted != 1 {
		t.Fatalf("privacy deadline did not expire last backup: %d %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(store.Path, r.ID+".zip")); !os.IsNotExist(err) {
		t.Fatalf("expired content remains: %v", err)
	}
	if b, err := os.ReadFile(unknown); err != nil || string(b) != "preserve" {
		t.Fatal("retention touched unrelated file")
	}
	status, err := store.Status(context.Background(), now.Add(36*24*time.Hour))
	if err != nil || status.State != "MISSING" {
		t.Fatalf("missing protection hidden: %+v %v", status, err)
	}
}

func TestProtectionIsolationLockAndInvalidInputs(t *testing.T) {
	now := time.Now().UTC()
	p, source := protectionFixture(t, now)
	root := filepath.Join(t.TempDir(), "backups")
	store, err := OpenProtection(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := OpenProtection(root, source); err == nil {
		duplicate.Close()
		t.Fatal("concurrent writer obtained same repository")
	}
	if _, err := store.Import(context.Background(), p, now, "arbitrary"); err == nil {
		t.Fatal("unknown checkpoint type accepted")
	}
	if _, err := store.Verify(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe checkpoint id accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	other := source
	other.Database = "different_project_db"
	if reopened, err := OpenProtection(root, other); err == nil {
		reopened.Close()
		t.Fatal("repository source binding replaced")
	}
	reopened, err := OpenProtection(root, source)
	if err != nil {
		t.Fatal("OS lock was not released", err)
	}
	defer reopened.Close()
	bad, _ := protectionFixture(t, now.Add(time.Hour))
	if _, err := reopened.Import(context.Background(), bad, now, "baseline"); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future point must fail: %v", err)
	}
	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "mine"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProtection(unrelated, source); err == nil {
		opened.Close()
		t.Fatal("initialized unrelated nonempty directory")
	}
}

func TestProtectionInterruptedImportCleanupAndCancelledImport(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	p, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, err := store.Import(context.Background(), p, now, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	// Crash just before the commit rename: the durable intent owns its archive.
	if err := store.root.Rename(r.ID+".json", r.ID+".pending"); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CleanupInterrupted(context.Background(), now.Add(23*time.Hour)); err != nil || n != 0 {
		t.Fatalf("in-flight grace period violated: %d %v", n, err)
	}
	if n, err := store.CleanupInterrupted(context.Background(), now.Add(24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("interrupted content not removed at deadline: %d %v", n, err)
	}
	if _, err := store.root.Stat(r.ID + ".zip"); !os.IsNotExist(err) {
		t.Fatalf("interrupted payload retained: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Import(ctx, p, now, "baseline"); err == nil {
		t.Fatal("cancelled attempt committed a recovery point")
	}
	status, err := store.Status(context.Background(), now)
	if err != nil || len(status.Records) != 0 {
		t.Fatalf("failed attempts advanced success state: %+v %v", status, err)
	}
}

func TestProtectionScheduleRequiresDailyBaselineAndIntermediatePoints(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		status ProtectionStatus
		want   string
	}{
		{"empty", ProtectionStatus{State: "MISSING"}, "baseline"},
		{"fresh", ProtectionStatus{State: "CURRENT", Records: []ProtectionRecord{{Kind: "baseline", SnapshotAt: now.Add(-time.Minute)}}}, ""},
		{"ten-minutes", ProtectionStatus{State: "CURRENT", Records: []ProtectionRecord{{Kind: "baseline", SnapshotAt: now.Add(-10 * time.Minute)}}}, "checkpoint"},
		{"new-day", ProtectionStatus{State: "STALE", Records: []ProtectionRecord{{Kind: "baseline", SnapshotAt: now.Add(-24 * time.Hour)}}}, "baseline"},
		{"bad-copy", ProtectionStatus{State: "CORRUPT", Records: []ProtectionRecord{{Kind: "baseline", SnapshotAt: now}}}, "baseline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := captureKind(tc.status, now); got != tc.want {
				t.Fatalf("want %q got %q", tc.want, got)
			}
		})
	}
}

func TestProtectionCleansInterruptedDatabaseCapture(t *testing.T) {
	now := time.Now().UTC()
	_, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := strings.Repeat("a", 32)
	intent, _ := json.Marshal(captureIntent{Format: 1, ID: id, Source: source, StartedAt: now})
	if err := store.root.WriteFile(id+".capture.intent", intent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.root.WriteFile(id+".capture", []byte("partial source content"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CleanupInterrupted(context.Background(), now.Add(24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("database capture residue not expired: %d %v", n, err)
	}
	if _, err := store.root.Stat(id + ".capture"); !os.IsNotExist(err) {
		t.Fatal("partial capture remains")
	}
}
