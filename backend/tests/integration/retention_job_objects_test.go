package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type cleanupFileStore struct {
	*storage.LocalRetentionStore
	namespace string
	deletes   int
	mode      string
	root      string
}

func (s *cleanupFileStore) RetentionNamespace() string {
	if s.namespace != "" {
		return s.namespace
	}
	return s.LocalRetentionStore.RetentionNamespace()
}
func (s *cleanupFileStore) DeleteObject(ctx context.Context, project, hash string) (storage.GCResult, error) {
	s.deletes++
	if s.mode == "denied" {
		return "", storage.ErrGCUnavailable
	}
	if s.mode == "false-success" {
		return storage.GCRemoved, nil
	}
	return s.LocalRetentionStore.DeleteObject(ctx, project, hash)
}

func prepareObjectCleanup(t *testing.T) (*service.RetentionScanner, *cleanupFileStore, *sql.DB, string, string, service.RetentionJobReport, string, string) {
	t.Helper()
	b, db := setup(t)
	project := createProject(b, "实际文件清理", "retention-job-object")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	root := t.TempDir()
	bound, err := storage.NewLocalRetentionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bound.Close() })
	files := &cleanupFileStore{LocalRetentionStore: bound, root: root}
	ctx := context.Background()
	old, err := files.PutObject(ctx, project, strings.NewReader("old unused file"))
	if err != nil {
		t.Fatal(err)
	}
	neighbor, err := files.PutObject(ctx, project, strings.NewReader("keep this new file"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: files}
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-02 03:04:05.000006' WHERE project_id=? AND object_hash=?", project, old.Hash); err != nil {
		t.Fatal(err)
	}
	preview, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil || preview.RemovableObjects != 1 {
		t.Fatal("expected one real removable object", preview, err)
	}
	if _, err := scanner.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	return scanner, files, db, actor, project, preview, old.Hash, neighbor.Hash
}

func TestRetentionJobRemovesExactPhysicalObjectAndVerifies(t *testing.T) {
	scanner, files, db, _, project, preview, old, neighbor := prepareObjectCleanup(t)
	ctx := context.Background()
	var state string
	for range 8 {
		var err error
		state, err = scanner.AdvanceJob(ctx, preview.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETE" {
			break
		}
	}
	if state != "COMPLETE" {
		t.Fatal("physical deletion stage has not been implemented", state)
	}
	if _, err := files.ReadObject(ctx, project, old); err == nil {
		t.Fatal("completed cleanup retained physical file")
	}
	if _, err := files.ReadObject(ctx, project, neighbor); err != nil {
		t.Fatal("cleanup touched neighboring file", err)
	}
	var removed int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=? AND object_hash=? AND state='DELETED' AND deleted_at IS NOT NULL", project, old).Scan(&removed); err != nil || removed != 1 {
		t.Fatal("missing deletion state", err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key=? AND result<>'BLOCKED'", preview.ID, "object:"+old).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("physical removal has no verified receipts", receipts, err)
	}
	if files.deletes != 1 {
		t.Fatal("file deleted more than once", files.deletes)
	}
	if _, err := backup.Create(ctx, db, files, filepath.Join(t.TempDir(), "object-cleanup.zip")); err != nil {
		t.Fatal("completed object cleanup cannot be archived", err)
	}
}

func preparePhysicalClaim(t *testing.T, scanner *service.RetentionScanner, db *sql.DB, job string) {
	t.Helper()
	for range 4 {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND phase='REMOVED' AND result='BLOCKED'", job).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		if state, err := scanner.AdvanceJob(context.Background(), job); err != nil || state != "DELETING" {
			t.Fatal("prepare claim", state, err)
		}
	}
	t.Fatal("claim was not persisted before deletion")
}

func finishPhysicalJob(t *testing.T, scanner *service.RetentionScanner, job string) {
	t.Helper()
	for range 8 {
		state, err := scanner.AdvanceJob(context.Background(), job)
		if err != nil {
			t.Fatal("resume physical job", state, err)
		}
		if state == "COMPLETE" {
			return
		}
	}
	t.Fatal("physical job never completed")
}

func TestRetentionJobObjectReceiptFailureResumesObservedAbsence(t *testing.T) {
	scanner, files, db, actor, project, preview, old, neighbor := prepareObjectCleanup(t)
	preparePhysicalClaim(t, scanner, db, preview.ID)
	if files.deletes != 0 {
		t.Fatal("prepared record already deleted file")
	}
	if _, err := db.Exec("CREATE TRIGGER fail_object_verified BEFORE INSERT ON retention_receipts FOR EACH ROW BEGIN IF NEW.phase='VERIFIED' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='PRIVATE_OBJECT_FAILURE'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := scanner.AdvanceJob(ctx, preview.ID)
	if err == nil || state != "RETRY_PENDING" || strings.Contains(err.Error(), "PRIVATE_OBJECT_FAILURE") {
		t.Fatal("failed receipt not retriable or leaked SQL", state, err)
	}
	if _, err := files.ReadObject(ctx, project, old); err == nil {
		t.Fatal("test did not reach physical deletion")
	}
	var objectState string
	if err := db.QueryRow("SELECT state FROM retention_objects WHERE project_id=? AND object_hash=?", project, old).Scan(&objectState); err != nil || objectState != "DELETING" {
		t.Fatal("failed commit lost durable claim", objectState, err)
	}
	var successful int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND result<>'BLOCKED'", preview.ID).Scan(&successful); err != nil || successful != 0 {
		t.Fatal("failed commit falsely recorded deletion", successful, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_object_verified"); err != nil {
		t.Fatal(err)
	}
	if err := files.LocalRetentionStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.NewLocalRetentionStore(files.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	files.LocalRetentionStore = reopened
	restarted := &service.RetentionScanner{Workspace: scanner.Workspace, Blobs: files}
	if _, err := restarted.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	finishPhysicalJob(t, restarted, preview.ID)
	var removed string
	if err := db.QueryRow("SELECT result FROM retention_receipts WHERE job_id=? AND phase='REMOVED'", preview.ID).Scan(&removed); err != nil || removed != "ABSENT" {
		t.Fatal("recovery misrepresented absent file as newly deleted", removed, err)
	}
	if files.deletes != 1 {
		t.Fatal("restart repeated an already finished physical deletion", files.deletes)
	}
	if _, err := files.ReadObject(ctx, project, neighbor); err != nil {
		t.Fatal("neighbor lost", err)
	}
}

func TestRetentionJobExecutesMixedSourceAndObjectPlan(t *testing.T) {
	scanner, db, actor, project, _, _ := prepareCleanupExecution(t)
	files, err := storage.NewLocalRetentionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	scanner.Blobs = files
	ctx := context.Background()
	object, err := files.PutObject(ctx, project, strings.NewReader("mixed plan orphan"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-02' WHERE project_id=? AND object_hash=?", project, object.Hash); err != nil {
		t.Fatal(err)
	}
	preview, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil || preview.Expiring != 2 || preview.RemovableObjects != 1 {
		t.Fatal("mixed plan fixture", preview, err)
	}
	if _, err := scanner.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	finishPhysicalJob(t, scanner, preview.ID)
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND result<>'BLOCKED'", preview.ID).Scan(&receipts); err != nil || receipts != 6 {
		t.Fatal("mixed progress missing receipts", receipts, err)
	}
	if _, err := files.ReadObject(ctx, project, object.Hash); err == nil {
		t.Fatal("mixed plan retained orphan")
	}
}

func TestRetentionJobObjectRefusesUnsafeProgress(t *testing.T) {
	for _, mode := range []string{"namespace", "denied", "false-success", "forged-clock", "missing-without-claim", "renewed-lease"} {
		t.Run(mode, func(t *testing.T) {
			scanner, files, db, _, project, preview, old, _ := prepareObjectCleanup(t)
			ctx := context.Background()
			if mode == "missing-without-claim" {
				if _, err := files.LocalRetentionStore.DeleteObject(ctx, project, old); err != nil {
					t.Fatal(err)
				}
			} else if mode == "renewed-lease" {
				if _, err := db.Exec("UPDATE retention_objects SET lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 10 MINUTE),revision=revision+1 WHERE project_id=? AND object_hash=?", project, old); err != nil {
					t.Fatal(err)
				}
			} else {
				preparePhysicalClaim(t, scanner, db, preview.ID)
			}
			switch mode {
			case "namespace":
				files.namespace = strings.Repeat("e", 64)
			case "denied", "false-success":
				files.mode = mode
			case "forged-clock":
				if _, err := db.Exec("UPDATE retention_receipts SET recorded_at='2000-01-01' WHERE job_id=?", preview.ID); err != nil {
					t.Fatal(err)
				}
			}
			var state string
			var stepErr error
			for range 3 {
				state, stepErr = scanner.AdvanceJob(ctx, preview.ID)
				if stepErr != nil || state == "BLOCKED" {
					break
				}
			}
			if stepErr == nil && state != "BLOCKED" {
				t.Fatal("unsafe state accepted", state)
			}
			var successful int
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND result<>'BLOCKED'", preview.ID).Scan(&successful); err != nil || successful != 0 {
				t.Fatal("manufactured completion", successful, err)
			}
			if mode != "missing-without-claim" {
				if _, err := files.ReadObject(ctx, project, old); err != nil {
					t.Fatal("unsafe attempt removed real file", err)
				}
			}
			if mode != "denied" && mode != "false-success" && files.deletes != 0 {
				t.Fatal("unsafe attempt reached physical deletion")
			}
			if mode == "namespace" || mode == "denied" || mode == "false-success" {
				files.namespace = ""
				files.mode = ""
				finishPhysicalJob(t, scanner, preview.ID)
			}
		})
	}
}
