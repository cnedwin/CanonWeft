package executiontemp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTemporaryWorkspacePreservesActiveAndReapsInterruptedRun(t *testing.T) {
	base := filepath.Join(t.TempDir(), "managed")
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path(), "private-input"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(25 * time.Hour)
	report, err := sweep(context.Background(), base, future)
	if err != nil || report.Active != 1 || report.Removed != 0 {
		t.Fatal(report, err)
	}
	// Simulate process death: the OS releases its lock, but files remain.
	if err := run.lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run.root.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = sweep(context.Background(), base, future)
	if err != nil || report.Removed != 1 || report.Blocked != 0 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(run.Path()); !os.IsNotExist(err) {
		t.Fatal("temporary input remains", err)
	}
	report, err = sweep(context.Background(), base, future)
	if err != nil || report.Removed != 0 {
		t.Fatal("cleanup is not idempotent", report, err)
	}
}

func TestTemporaryWorkspaceClosesImmediatelyAndKeepsYoungOrForeignFiles(t *testing.T) {
	base := filepath.Join(t.TempDir(), "managed")
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Close(); err != nil {
		t.Fatal(err)
	}
	if err = run.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
	young, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	young.lease.Close()
	young.root.Close()
	foreign := filepath.Join(base, "do-not-delete")
	if err := os.WriteFile(foreign, []byte("author"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "unowned-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	report, err := Sweep(context.Background(), base)
	if err != nil || report.Waiting != 1 || report.Removed != 0 || report.Blocked != 1 {
		t.Fatal(report, err)
	}
	if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "author" {
		t.Fatal("foreign file changed", err)
	}
}

func TestTemporaryWorkspaceRejectsForgedDirectoryAndInterruptedMarker(t *testing.T) {
	base := filepath.Join(t.TempDir(), "managed")
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	run.lease.Close()
	run.root.Close()
	marker := filepath.Join(base, run.id+".lease")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var record leaseRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.ID = "../outside"
	raw, _ = json.Marshal(record)
	if err = os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := sweep(context.Background(), base, time.Now().UTC().Add(25*time.Hour))
	if err != nil || report.Blocked != 1 || report.Removed != 0 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(run.Path()); err != nil {
		t.Fatal("invalid ownership deleted", err)
	}
	if err = os.WriteFile(marker, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = sweep(context.Background(), base, time.Now().UTC().Add(25*time.Hour))
	if err != nil || report.Blocked != 1 {
		t.Fatal(report, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Sweep(cancelled, base); err == nil {
		t.Fatal("cancelled cleanup proceeded")
	}
}
