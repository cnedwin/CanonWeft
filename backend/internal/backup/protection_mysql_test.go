package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/storage"
)

func TestProtectionMySQLAutomaticCaptureAndIsolatedRestore(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	w, p := randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'auto protected')", w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'recoverable novel')", p, w)
	ledger := provisionTestAuthority(t, db, admin)
	blobs := storage.LocalBlobs{Root: filepath.Join(t.TempDir(), "blobs")}
	runner, err := NewProtectionRunner(ctx, db, blobs, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	now := time.Now().UTC()
	first, err := runner.Tick(ctx, now)
	if err != nil || !first.Captured || first.State != "CURRENT" || first.Record.Kind != "baseline" {
		t.Fatalf("initial automatic baseline failed: %+v %v", first, err)
	}
	second, err := runner.Tick(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil || second.Captured {
		t.Fatalf("fresh recovery point repeated: %+v %v", second, err)
	}
	target := targetDSN(t, cfg, admin)
	metrics, err := Restore(ctx, RestoreOptions{ArchivePath: filepath.Join(runner.Store.Path, first.Record.ID+".zip"), TargetDSN: target, BlobRoot: filepath.Join(t.TempDir(), "restored"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}})
	if err != nil {
		t.Fatal(err)
	}
	targetConfig, err := mysql.ParseDSN(target)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("mysql", targetConfig.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var name string
	if err = restored.QueryRow("SELECT name FROM projects WHERE id=?", p).Scan(&name); err != nil || name != "recoverable novel" || metrics.TargetDatabase == cfg.DBName {
		t.Fatalf("automatic backup did not restore into independent copy: %q %v", name, err)
	}
	// Failure must never invent a new successful recovery point.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := runner.Tick(ctx, now.Add(11*time.Minute)); err == nil || failed.Captured {
		t.Fatalf("closed source was counted as protected: %+v %v", failed, err)
	}
	status, err := runner.Store.Status(ctx, now.Add(16*time.Minute))
	if err != nil || len(status.Records) != 1 || status.State != "STALE" {
		t.Fatalf("failed capture refreshed successful protection: %+v %v", status, err)
	}
}

func TestProtectionRunStopsAndRestartDoesNotDuplicateFreshPoint(t *testing.T) {
	db, _, admin := testDatabase(t)
	ctx := context.Background()
	provisionTestAuthority(t, db, admin)
	root := filepath.Join(t.TempDir(), "backups")
	blobs := storage.LocalBlobs{Root: filepath.Join(t.TempDir(), "blobs")}
	for attempt := 0; attempt < 2; attempt++ {
		runner, err := NewProtectionRunner(ctx, db, blobs, root)
		if err != nil {
			t.Fatal(err)
		}
		running, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		results := make(chan ProtectionTick, 1)
		failures := make(chan error, 1)
		go func() {
			defer close(done)
			runner.Run(running, func(result ProtectionTick, err error) {
				results <- result
				failures <- err
				cancel()
			})
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cancel()
			t.Fatal("protection loop did not stop promptly after cancellation")
		}
		result, err := <-results, <-failures
		if err != nil || result.Captured != (attempt == 0) {
			t.Fatalf("restart duplicated/missed capture: %+v %v", result, err)
		}
		if err := runner.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
