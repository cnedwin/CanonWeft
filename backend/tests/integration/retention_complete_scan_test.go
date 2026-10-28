package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func installCompleteRetentionFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	installSourceRegistrationFixture(t, db)
	var text strings.Builder
	for _, name := range []string{"retention.sql.pending", "retention_identity.sql.pending", "retention_sources.sql.pending"} {
		raw, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"))
		text.WriteString("\n")
	}
	hash := sha256.Sum256([]byte(text.String()))
	checksum := hex.EncodeToString(hash[:])
	if _, err := db.Exec("UPDATE schema_migrations SET checksum=? WHERE version=36", checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version=36,checksum=?,history_initialized=TRUE WHERE id=1", checksum); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionCompleteScanPersistsVerifiedInventoryAndInvalidatesOnWrite(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "完整扫描项目", "complete-scan-project")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	interruptRetentionMigrationMarker(t, db)
	b.request("POST", "/api/projects/"+project+"/retention-inventory/scan", nil, 503)
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	object, err := blobs.PutObject(context.Background(), project, strings.NewReader("legacy unreferenced object"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	if _, err := scanner.Scan(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("uninstalled schema admitted a complete scan", err)
	}
	installCompleteRetentionFixture(t, db)
	first, err := scanner.Scan(context.Background(), actor, project)
	if err != nil || !first.Complete || first.State != "CURRENT" || first.Objects != 1 || first.References == 0 || first.InventoryHash == "" {
		t.Fatal("complete scan failed", first, err)
	}
	var firstSeen, scanned string
	var revision int64
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=?", project, object.Hash).Scan(&firstSeen); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT inventory_revision,CAST(scanned_at AS CHAR) FROM project_lifecycle WHERE project_id=? AND state='CURRENT'", project).Scan(&revision, &scanned); err != nil || revision != first.InventoryRevision {
		t.Fatal("scan checkpoint missing", err)
	}
	second, err := scanner.Scan(context.Background(), actor, project)
	if err != nil || second.InventoryRevision <= first.InventoryRevision || second.Objects != 1 {
		t.Fatal("second scan did not publish a new epoch", err)
	}
	var repeated string
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=?", project, object.Hash).Scan(&repeated); err != nil || repeated != firstSeen {
		t.Fatal("scan renewed object age", err)
	}
	w := service.Workspace{DB: db}
	if _, err := w.UpdateProject(context.Background(), actor, project, "真实修改后库存失效", 1); err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE project_id=? AND state='UNSCANNED' AND scanned_at IS NULL AND inventory_revision>?", project, second.InventoryRevision).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatal("business mutation retained an old valid inventory", err)
	}
	if _, err := blobs.ReadObject(context.Background(), project, object.Hash); err != nil {
		t.Fatal("scan deleted an orphan object", err)
	}
	third, err := scanner.Scan(context.Background(), actor, project)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := objectwriter.Write(context.Background(), tx, blobs, project, []byte("new physical object")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE project_id=? AND state='UNSCANNED' AND scanned_at IS NULL AND inventory_revision>?", project, third.InventoryRevision).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatal("protected object write retained a complete physical inventory", err)
	}
}

func TestRetentionCompleteScanFailureCannotLeaveCurrentInventory(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "扫描失败回滚", "complete-scan-failure")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	object, err := blobs.PutObject(context.Background(), project, strings.NewReader("owned content"))
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	first, err := scanner.Scan(context.Background(), actor, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs.Root, project, "prepared-interrupted"), []byte("private leftover"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.Scan(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("unknown physical file accepted", err)
	}
	var invalid int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE project_id=? AND state='UNSCANNED' AND scanned_at IS NULL AND inventory_revision>?", project, first.InventoryRevision).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatal("failed scan left CURRENT", err)
	}
	if _, err = blobs.ReadObject(context.Background(), project, object.Hash); err != nil {
		t.Fatal("failed scan removed valid content", err)
	}
	foreignWorkspace, foreignActor := service.NewID(), service.NewID()
	if _, err = db.Exec("INSERT INTO workspaces(id,name) VALUES(?,'foreign')", foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'foreign-scan','unused','owner')", foreignActor, foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.Scan(context.Background(), foreignActor, project); err != domain.ErrForbidden {
		t.Fatal("foreign owner scanned project", err)
	}
	if err := os.Remove(filepath.Join(blobs.Root, project, "prepared-interrupted")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(context.Background(), actor, project); err != nil {
		t.Fatal("scan did not recover after the test leftover was removed", err)
	}
	if err := os.Remove(filepath.Join(blobs.Root, project, object.Hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("missing PRESENT object became an implicit deletion receipt", err)
	}
	var tombstones int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=? AND state='DELETED'", project).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Fatal("scanner manufactured a deletion record", err)
	}
}

type heldObjectInventory struct {
	storage.LocalBlobs
	entered chan struct{}
	release chan struct{}
}

func (s heldObjectInventory) ScanObjects(ctx context.Context, project string) ([]storage.BlobRef, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.LocalBlobs.ScanObjects(ctx, project)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRetentionCompleteScanSerializesPhysicalWrites(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "并发扫描", "complete-scan-lock")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	held := heldObjectInventory{LocalBlobs: storage.LocalBlobs{Root: t.TempDir()}, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	defer release()
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: held}
	scanned := make(chan error, 1)
	go func() { _, err := scanner.Scan(ctx, actor, project); scanned <- err }()
	select {
	case <-held.entered:
	case err := <-scanned:
		t.Fatal("scan ended before physical enumeration", err)
	case <-ctx.Done():
		t.Fatal("scan never reached physical enumeration")
	}
	written := make(chan error, 1)
	go func() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			written <- err
			return
		}
		defer tx.Rollback()
		if _, err = objectwriter.Write(ctx, tx, held.LocalBlobs, project, []byte("concurrent physical content")); err == nil {
			err = tx.Commit()
		}
		written <- err
	}()
	select {
	case err := <-written:
		t.Fatal("write passed the active scan lock", err)
	case <-time.After(75 * time.Millisecond):
	}
	release()
	if err := <-scanned; err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE project_id=? AND state='UNSCANNED' AND scanned_at IS NULL", project).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatal("queued writer left the completed scan current", err)
	}
}

func TestRetentionCompleteScanRequiresExactMigrationProvenance(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "扫描迁移门禁", "complete-scan-schema")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_version WHERE id=1").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, change, undo string }{
		{"legacy provenance", "UPDATE schema_migrations SET origin='LEGACY_BASELINE' WHERE version=36", "UPDATE schema_migrations SET origin='EXECUTED' WHERE version=36"},
		{"wrong checksum", "UPDATE schema_migrations SET checksum=REPEAT('0',64) WHERE version=36", "UPDATE schema_migrations SET checksum='" + checksum + "' WHERE version=36"},
		{"partial header", "UPDATE schema_version SET version=35", "UPDATE schema_version SET version=36"},
		{"timezone converted observation", "ALTER TABLE retention_objects MODIFY first_seen_at TIMESTAMP(6) NOT NULL", "ALTER TABLE retention_objects MODIFY first_seen_at DATETIME(6) NOT NULL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.Exec(tc.undo); err != nil {
					t.Fatal(err)
				}
			}()
			if _, err := scanner.Scan(context.Background(), actor, project); err != domain.ErrNotReady {
				t.Fatal("unqualified schema admitted", err)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle").Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected admission modified scan state", err)
			}
		})
	}
}

func TestRetentionCompleteScanCheckpointFailureRollsBackNewObservations(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "扫描提交故障", "complete-scan-checkpoint")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	if _, err := blobs.PutObject(context.Background(), project, strings.NewReader("unobserved content")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER fail_complete_checkpoint BEFORE UPDATE ON project_lifecycle FOR EACH ROW BEGIN IF NEW.state='CURRENT' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test checkpoint failure'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	if _, err := scanner.Scan(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("checkpoint failure was ignored", err)
	}
	for _, table := range []string{"retention_references", "retention_source_slots", "retention_objects"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id=?", project).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed scan committed partial observations", table, err)
		}
	}
	var invalid int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE project_id=? AND state='UNSCANNED' AND scanned_at IS NULL", project).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatal("failed scan lost committed invalidation", err)
	}
}
