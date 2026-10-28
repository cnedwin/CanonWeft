package qualification_test

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

// Preserve an actual adapter UNKNOWN receipt, then construct the old layout and
// wall-clock boundary explicitly. This tests migration, not an old model's quality.
func legacyReceiptLayout(t *testing.T) (*fixture, service.QualificationRun, string) {
	t.Helper()
	f, p, input := modelFixture(t)
	p.lost = true
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	r.Frozen.Suite.Policy = "repository-deterministic-v2"
	raw, _ := json.Marshal(r.Frozen)
	if _, err = f.db.Exec("UPDATE qualification_runs SET frozen=?,created_at=DATE_SUB((SELECT recorded_at FROM schema_migrations WHERE version=29),INTERVAL 4 SECOND) WHERE id=?", raw, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE generation_tasks SET created_at=DATE_SUB((SELECT recorded_at FROM schema_migrations WHERE version=29),INTERVAL 3 SECOND) WHERE id=?", r.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	var call string
	if err = f.db.QueryRow("SELECT call_id FROM generation_tasks WHERE id=?", r.Steps[0].ChildID).Scan(&call); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE model_calls SET created_at=DATE_SUB((SELECT recorded_at FROM schema_migrations WHERE version=29),INTERVAL 2 SECOND) WHERE id=?", call); err != nil {
		t.Fatal(err)
	}
	return f, r, call
}

func resetToJournal29(t *testing.T, f *fixture) {
	t.Helper()
	// The fixture deliberately rewinds the entire journal. Leaving a newer
	// schema31 marker behind would exercise journal corruption, not a resumable
	// migration30 backfill interrupted after its DDL committed.
	if _, err := f.db.Exec("DELETE FROM schema_migrations WHERE version>29"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE schema_version SET version=29,checksum=(SELECT checksum FROM schema_migrations WHERE version=29) WHERE id=1"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMigration30RetriesAfterCommittedDDLAndFailedBackfill(t *testing.T) {
	f, _, call := legacyReceiptLayout(t)
	ctx := context.Background()
	if _, err := f.db.Exec("DELETE FROM qualification_calls WHERE call_id=?", call); err != nil {
		t.Fatal(err)
	}
	resetToJournal29(t, f)
	if _, err := f.db.Exec("CREATE TRIGGER legacy_backfill_interruption BEFORE INSERT ON qualification_calls FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='tests-only interrupted legacy backfill'"); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunMigrations(ctx, f.db); err == nil {
		t.Fatal("backfill interruption not exercised")
	}
	var columns, journal int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='qualification_calls' AND column_name IN ('origin','legacy_source')").Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=30").Scan(&journal); err != nil || columns != 2 || journal != 0 {
		t.Fatal("partial DDL incorrectly journalled or lost", columns, journal, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER legacy_backfill_interruption"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := storage.RunMigrations(ctx, f.db); err != nil {
			t.Fatal("legacy migration was not resumable", err)
		}
	}
	var origin, state string
	var held, reserved, spent int64
	if err := f.db.QueryRow("SELECT q.origin,q.state,q.held_micros,c.reserved_micros,q.charged_micros FROM qualification_calls q JOIN model_calls c ON c.id=q.call_id WHERE q.call_id=?", call).Scan(&origin, &state, &held, &reserved, &spent); err != nil || origin != "LEGACY_28_DB" || state != "UNKNOWN" || held != reserved || held <= 0 || spent != 0 {
		t.Fatal("legacy unknown fee lost", origin, state, held, reserved, spent, err)
	}
	if _, err := backup.Create(ctx, f.db, f.svc.IP.Blobs, filepath.Join(t.TempDir(), "migrated30.zip")); err != nil {
		t.Fatal("converted legacy database cannot be backed up", err)
	}
	var beforeProof, afterProof string
	var revision int64
	if err := f.db.QueryRow("SELECT legacy_source FROM qualification_calls WHERE call_id=?", call).Scan(&beforeProof); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT revision FROM model_call_accounting WHERE call_id=?", call).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: 71, BillReference: "legacy-late-invoice", Evidence: "Local fixture invoice received after migration30 completed", Reason: "settle legacy actual receipt only", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT held_micros,charged_micros,legacy_source FROM qualification_calls WHERE call_id=?", call).Scan(&held, &spent, &afterProof); err != nil || held != 0 || spent != 71 || beforeProof != afterProof {
		t.Fatal("legacy late invoice mutated origin proof or lost known charge", held, spent, err)
	}
	if _, err := backup.Create(ctx, f.db, f.svc.IP.Blobs, filepath.Join(t.TempDir(), "legacy-late-invoice30.zip")); err != nil {
		t.Fatal("settled legacy proof rejected after actual invoice", err)
	}
}

func TestLegacyMigrationNeverRepairsMissingNative29Or30Mapping(t *testing.T) {
	f, p, input := modelFixture(t)
	p.lost = true
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	if _, err = f.db.Exec("DELETE FROM qualification_calls WHERE run_id=?", r.ID); err != nil {
		t.Fatal(err)
	}
	resetToJournal29(t, f)
	if err = storage.RunMigrations(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM qualification_calls WHERE run_id=?", r.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("native missing mapping relabelled legacy", count, err)
	}
	if _, err = backup.Create(ctx, f.db, f.svc.IP.Blobs, filepath.Join(t.TempDir(), "must-refuse.zip")); err == nil {
		t.Fatal("native missing mapping accepted after migration")
	}
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || budget.Reserved <= 0 || p.calls.Load() != 1 {
		t.Fatal("native unknown request freed or resent", budget, p.calls.Load(), err)
	}
}

func writeSchema28Archive(t *testing.T, current, output string) {
	t.Helper()
	reader, err := zip.OpenReader(current)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	files := map[string][]byte{}
	for _, entry := range reader.File {
		source, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(source)
		source.Close()
		if e != nil {
			t.Fatal(e)
		}
		files[entry.Name] = raw
	}
	var manifest backup.Manifest
	var data backup.Data
	if json.Unmarshal(files["manifest.json"], &manifest) != nil || json.Unmarshal(files["data.json"], &data) != nil {
		t.Fatal("native archive fixture malformed")
	}
	manifest.Schema = 28
	manifest.Migrations = manifest.Migrations[:28]
	delete(data, "qualification_calls")
	delete(data, "qualification_week_grants")
	// These tables did not exist in schema28. The importer correctly rejects
	// archives that claim an old version while carrying future table data.
	for _, name := range []string{"recipe_steps", "recipe_runs", "import_drafts", "ingest_jobs", "patch_drafts", "semantic_review_runs", "semantic_review_acceptances", "ip_branch_deltas", "memory_sessions", "memory_items", "memory_revisions"} {
		if table, ok := data[name]; ok && len(table) != 0 {
			t.Fatalf("schema28 fixture contains newer table data: %s", name)
		}
		delete(data, name)
	}
	files["data.json"], err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(files["data.json"])
	for index := range manifest.Files {
		if manifest.Files[index].Path == "data.json" {
			manifest.Files[index].Bytes = int64(len(files["data.json"]))
			manifest.Files[index].SHA256 = hex.EncodeToString(sum[:])
		}
	}
	files["manifest.json"], err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(target)
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, e := writer.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = entry.Write(files[name]); e != nil {
			t.Fatal(e)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}
}

func restoreLegacyCopy(t *testing.T, f *fixture, archive, root string) (*sql.DB, string) {
	t.Helper()
	cfg := f.config.Clone()
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if !strings.HasPrefix(cfg.DBName, "studio_restore_") {
			t.Fatal("unsafe legacy restore cleanup")
		}
		if _, err := f.admin.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	if _, err := backup.Restore(context.Background(), backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: root, Authority: backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg.DBName
}

func TestLegacySchema28ArchiveConvertsTo30BacksUpAndRestoresAgain(t *testing.T) {
	f, r, call := legacyReceiptLayout(t)
	ctx := context.Background()
	root := t.TempDir()
	native, legacy := filepath.Join(root, "native30.zip"), filepath.Join(root, "legacy28.zip")
	if _, err := backup.Create(ctx, f.db, f.svc.IP.Blobs, native); err != nil {
		t.Fatal(err)
	}
	writeSchema28Archive(t, native, legacy)
	firstBlobs := filepath.Join(root, "restored-one")
	first, firstName := restoreLegacyCopy(t, f, legacy, firstBlobs)
	var origin, state string
	var held, reserved int64
	if err := first.QueryRow("SELECT q.origin,q.state,q.held_micros,c.reserved_micros FROM qualification_calls q JOIN model_calls c ON c.id=q.call_id WHERE q.call_id=?", call).Scan(&origin, &state, &held, &reserved); err != nil || origin != "LEGACY_28_ARCHIVE" || state != "UNKNOWN" || held != reserved || held <= 0 {
		t.Fatal("schema28 conversion lost actual unknown receipt", origin, state, held, reserved, err)
	}
	if err := first.QueryRow("SELECT state FROM qualification_runs WHERE id=?", r.ID).Scan(&state); err != nil || state != "CANCELLED" {
		t.Fatal("legacy restore regained execution authority", state, err)
	}
	// Explicitly establish the disposable copy's protected baseline; accounts,
	// model bindings and budgets remain disabled throughout both restores.
	if _, err := authority.Provision(ctx, f.admin, f.ledger.Schema, firstName); err != nil {
		t.Fatal(err)
	}
	again := filepath.Join(root, "again30.zip")
	if _, err := backup.Create(ctx, first, storage.LocalBlobs{Root: firstBlobs}, again); err != nil {
		t.Fatal("converted schema28 cannot be backed up as30", err)
	}
	second, _ := restoreLegacyCopy(t, f, again, filepath.Join(root, "restored-two"))
	if err := second.QueryRow("SELECT held_micros,origin FROM qualification_calls WHERE call_id=?", call).Scan(&held, &origin); err != nil || held != reserved || origin != "LEGACY_28_ARCHIVE" {
		t.Fatal("second restore lost historical proof/hold", held, origin, err)
	}
	var enabled int
	if err := second.QueryRow("SELECT COUNT(*) FROM model_budgets WHERE enabled=TRUE").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("legacy conversion enabled paid work", enabled, err)
	}
}
