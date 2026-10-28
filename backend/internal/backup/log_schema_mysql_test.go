package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/storage"
)

func TestLogRunnerSchemaTransitionKeepsLegacyPointAndRecordsGap(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ledger := provisionTestAuthority(t, db, admin)
	root := t.TempDir()
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "objects")}
	path := filepath.Join(root, "baseline.zip")
	base, _, err := CreateLogBaseline(ctx, db, blobs, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// Build a real-source fixture with the exact older additive catalog. This
	// exercises stored legacy replay, not live capture using the old binary.
	a.Manifest.Schema = 30
	beforeMemory(a)
	a.Manifest.Migrations = a.Manifest.Migrations[:30]
	for _, name := range append([]string{"recipe_runs", "recipe_steps", "patch_drafts", "ingest_jobs", "import_drafts"}, semanticReviewTables...) {
		delete(a.Data, name)
	}
	raw = archiveBytes(t, a)
	base.Schema = 30
	base.ArchiveSHA256 = digest(raw)
	var tables []LogTable
	for _, table := range base.Tables {
		if !logSchemaFutureTable(30, table.Layout.Name) {
			tables = append(tables, table)
		}
	}
	base.Tables = tables
	repository := filepath.Join(root, "logs")
	store, err := OpenLogStore(repository, base.Source)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CommitBaseline(ctx, base, raw, time.Now().UTC())
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	runner, err := NewLogRunner(ctx, db, blobs, cfg.FormatDSN(), repository)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	view, err := runner.Tick(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "CURRENT_WITH_GAPS" || view.HistoryGaps != 1 || view.BaselineID == id {
		t.Fatal("schema transition hid unproven history", view.State, view.HistoryGaps)
	}
	gap, err := runner.Store.readGap(id)
	if err != nil || gap.Reason != "SCHEMA_CHANGE" {
		t.Fatal("transition reason not durable", err)
	}
	legacy, err := runner.Store.Archive(ctx, id, base.Position, time.Now().UTC())
	if err != nil {
		t.Fatal("legacy point became unavailable", err)
	}
	selected := filepath.Join(root, "legacy-point.zip")
	if err = os.WriteFile(selected, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	opt := RestoreOptions{ArchivePath: selected, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, "restored"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
	if _, err = Restore(ctx, opt); err != nil {
		t.Fatal("legacy point cannot restore through current migrations", err)
	}
}
