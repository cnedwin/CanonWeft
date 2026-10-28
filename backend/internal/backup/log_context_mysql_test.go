package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
)

func TestLogCaptureCancellationReleasesSchemaLeaseAndAllowsAutomaticCheckpoint(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	setup, stopSetup := context.WithTimeout(context.Background(), time.Minute)
	defer stopSetup()
	if err := storage.RunMigrations(setup, db); err != nil {
		t.Fatal(err)
	}
	provisionTestAuthority(t, db, admin)
	root := t.TempDir()
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "objects")}
	baselinePath := filepath.Join(root, "log-baseline.zip")
	baseline, _, err := CreateLogBaseline(setup, db, blobs, baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Schema != 38 {
		t.Fatalf("expected current schema 38, got %d", baseline.Schema)
	}
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := OpenLogReplay(baseline, raw)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := binlog.NewReader(cfg.FormatDSN(), baseline.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	// This observer owns a separate pool. Reusing the capture pool could hide
	// a leaked session by accidentally querying it on the lock-owning connection.
	observer, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	parent, cancel := context.WithCancel(setup)
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- replay.withLogCapture(parent, db, reader, func(inner context.Context, _ queryer, _ []binlog.TableSchema, _ binlog.Position, _ time.Time) error {
			close(entered)
			<-inner.Done()
			return inner.Err()
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("capture failed before acquiring its schema lease: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not enter its schema lease")
	}
	var owner sql.NullInt64
	if err := observer.QueryRowContext(setup, "SELECT IS_USED_LOCK(CONCAT(DATABASE(), ':schema'))").Scan(&owner); err != nil || !owner.Valid {
		t.Fatalf("independent observer did not see the held schema lease: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("capture did not return cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled capture did not return")
	}
	if err := observer.QueryRowContext(setup, "SELECT IS_USED_LOCK(CONCAT(DATABASE(), ':schema'))").Scan(&owner); err != nil || owner.Valid {
		t.Fatalf("cancelled capture retained its schema lease: held=%v error=%v", owner.Valid, err)
	}
	if replay.Point().Position != baseline.Position || replay.Point().Hash != baseline.ArchiveSHA256 {
		t.Fatal("cancelled capture advanced its recovery point")
	}

	// A fresh automatic capture must obtain the same schema lease and produce
	// a real validated archive after the cancelled log capture has torn down.
	fresh, stopFresh := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopFresh()
	runner, err := NewProtectionRunner(fresh, db, blobs, filepath.Join(root, "automatic-backups"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	result, err := runner.Tick(fresh, time.Now().UTC())
	if err != nil || !result.Captured || result.State != "CURRENT" || result.Record.ID == "" {
		t.Fatalf("automatic snapshot failed after capture cancellation: %+v %v", result, err)
	}
	checkpoint, err := os.ReadFile(filepath.Join(runner.Store.Path, result.Record.ID+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := ValidateArchive(bytes.NewReader(checkpoint), int64(len(checkpoint)))
	if err != nil {
		t.Fatal("automatic snapshot archive failed validation", err)
	}
	if archive.Manifest.Schema != 38 || archive.Manifest.Source != baseline.Source {
		t.Fatal("automatic snapshot changed schema or source after cancellation")
	}
}
