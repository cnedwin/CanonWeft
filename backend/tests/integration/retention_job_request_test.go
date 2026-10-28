package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func prepareRequestCleanupJob(t *testing.T, historicalActor bool) (*service.RetentionScanner, *sql.DB, domain.Actor, string, service.RetentionJobReport, string) {
	t.Helper()
	b, db := setup(t)
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string)}
	ctx := context.Background()
	workspace := &service.Workspace{DB: db}
	project, err := workspace.CreateProject(ctx, actor.ID, "旧请求清理", "legacy-private-project-key")
	if err != nil {
		t.Fatal(err)
	}
	budgets := service.Budgets{Workspace: workspace}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	callActor := actor
	if historicalActor {
		callActor.ID = service.NewID()
		if _, err := db.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'historical-cleanup-actor','unused','owner')", callActor.ID, actor.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	}
	call, err := budgets.Reserve(ctx, callActor, project.ID, "legacy-private-model-key", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, callActor, project.ID, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "provider unknown", ""); err != nil {
		t.Fatal(err)
	}
	if historicalActor {
		if _, err := db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", callActor.ID); err != nil {
			t.Fatal(err)
		}
	}
	installCompleteRetentionFixture(t, db)
	for _, item := range []struct {
		table, key string
		primary    map[string]string
	}{
		{"operations", "legacy-private-project-key", map[string]string{"actor_id": actor.ID, "operation_id": "legacy-private-project-key"}},
		{"model_calls", "legacy-private-model-key", map[string]string{"id": call.ID}},
	} {
		owner, err := retention.RowOwnerKey(item.table, item.primary)
		if err != nil {
			t.Fatal(err)
		}
		id, err := retention.ReferenceID(project.ID, item.table, owner, "operation_id")
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(item.key))
		if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,'operation_id','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project.ID, item.table, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := retention.CaptureSourceValues(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project.ID, retention.SourceKey{Table: item.table, Primary: item.primary}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	scanner := &service.RetentionScanner{Workspace: workspace, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	preview, err := scanner.PlanJob(ctx, actor.ID, project.ID, "request-v2:"+service.NewID())
	if err != nil || preview.Expiring != 2 {
		t.Fatal("request cleanup fixture", preview, err)
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project.ID, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	return scanner, db, actor, project.ID, preview, call.ID
}

func TestRetentionJobRetiresRequestKeysWithoutReissuingUnknownCalls(t *testing.T) {
	scanner, db, actor, project, preview, callID := prepareRequestCleanupJob(t, false)
	finishPhysicalJob(t, scanner, preview.ID)
	ctx := context.Background()
	var aliases, receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_operation_keys WHERE actor_id=?", actor.ID).Scan(&aliases); err != nil || aliases != 2 {
		t.Fatal("missing request aliases", aliases, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=?", preview.ID).Scan(&receipts); err != nil || receipts != 4 {
		t.Fatal("missing request receipts", receipts, err)
	}
	if _, err := backup.Create(ctx, db, scanner.Blobs, filepath.Join(t.TempDir(), "retired-request-job.zip")); err != nil {
		t.Fatal("completed request job archive", err)
	}
	replay, err := scanner.Workspace.CreateProject(ctx, actor.ID, "旧请求清理", "legacy-private-project-key")
	if err != nil || replay.ID != project {
		t.Fatal("retired operation did not replay original project", err)
	}
	budgets := service.Budgets{Workspace: scanner.Workspace}
	call, err := budgets.Reserve(ctx, actor, project, "legacy-private-model-key", 80, "USD", strings.Repeat("a", 64))
	if err != nil || call.ID != callID || call.State != "UNKNOWN" {
		t.Fatal("retired model key reissued a call", err)
	}
	var reserved, spent int64
	if err := db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", actor.WorkspaceID).Scan(&reserved, &spent); err != nil || reserved != 80 || spent != 0 {
		t.Fatal("cleanup changed unknown money", reserved, spent, err)
	}
	if _, err := scanner.Workspace.CreateProject(ctx, actor.ID, "不能新建", "unseen-legacy-key"); err != domain.ErrConflict {
		t.Fatal("legacy admission did not close", err)
	}
	if _, err := scanner.Workspace.CreateProject(ctx, actor.ID, "规范新请求", "request-v2:"+service.NewID()); err != nil {
		t.Fatal("canonical request blocked", err)
	}
}

func TestRetentionJobRequestReceiptFailureRollsBackIdentityAndPolicy(t *testing.T) {
	scanner, db, actor, project, preview, _ := prepareRequestCleanupJob(t, false)
	if _, err := db.Exec("CREATE TRIGGER fail_request_receipt BEFORE INSERT ON retention_receipts FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='PRIVATE_REQUEST_FAILURE'"); err != nil {
		t.Fatal(err)
	}
	state, err := scanner.AdvanceJob(context.Background(), preview.ID)
	if err == nil || state != "RETRY_PENDING" || strings.Contains(err.Error(), "PRIVATE_REQUEST_FAILURE") {
		t.Fatal("request failure did not persist safe retry", state, err)
	}
	var aliases, closed, evidence int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_operation_keys WHERE actor_id=?", actor.ID).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_identity_policies WHERE workspace_id=? AND legacy_writes_closed=TRUE", actor.WorkspaceID).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE project_id=?", project).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if aliases != 0 || closed != 0 || evidence != 0 {
		t.Fatal("identity transaction partially committed", aliases, closed, evidence)
	}
	if _, err := db.Exec("DROP TRIGGER fail_request_receipt"); err != nil {
		t.Fatal(err)
	}
	restarted := &service.RetentionScanner{Workspace: scanner.Workspace, Blobs: scanner.Blobs}
	finishPhysicalJob(t, restarted, preview.ID)
}

type requestCleanupBarrier struct {
	service.RetentionScanStore
	scans   int
	entered chan struct{}
	release chan struct{}
}

func (s *requestCleanupBarrier) ScanObjects(ctx context.Context, project string) ([]storage.BlobRef, error) {
	s.scans++
	// First scan discovers the owner. The second holds actor/policy/project in
	// the dedicated retirement transaction, before any identity mutation.
	if s.scans == 2 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.RetentionScanStore.ScanObjects(ctx, project)
}

func TestRetentionJobRequestCutoverSerializesReplay(t *testing.T) {
	scanner, db, actor, project, preview, callID := prepareRequestCleanupJob(t, false)
	barrier := &requestCleanupBarrier{RetentionScanStore: scanner.Blobs, entered: make(chan struct{}), release: make(chan struct{})}
	scanner.Blobs = barrier
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := scanner.AdvanceJob(ctx, preview.ID); done <- err }()
	select {
	case <-barrier.entered:
	case err := <-done:
		t.Fatal("cleanup did not reach ordered locks", err)
	case <-ctx.Done():
		t.Fatal("cleanup never reached barrier")
	}
	released := false
	defer func() {
		if !released {
			close(barrier.release)
		}
	}()
	type result struct {
		id  string
		err error
	}
	replay := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		budgets := service.Budgets{Workspace: scanner.Workspace}
		call, err := budgets.Reserve(ctx, actor, project, "legacy-private-model-key", 80, "USD", strings.Repeat("a", 64))
		replay <- result{call.ID, err}
	}()
	<-started
	select {
	case got := <-replay:
		t.Fatal("request crossed uncommitted retirement", got.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(barrier.release)
	released = true
	if err := <-done; err != nil {
		t.Fatal("retirement failed", err)
	}
	got := <-replay
	if got.err != nil || got.id != callID {
		t.Fatal("concurrent request duplicated or failed", got.err)
	}
	finishPhysicalJob(t, scanner, preview.ID)
	var calls int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_calls WHERE project_id=?", project).Scan(&calls); err != nil || calls != 1 {
		t.Fatal("concurrent request duplicated billing", calls, err)
	}
}

func TestRetentionJobRetiresDisabledHistoricalActorsRequest(t *testing.T) {
	scanner, db, actor, project, preview, callID := prepareRequestCleanupJob(t, true)
	finishPhysicalJob(t, scanner, preview.ID)
	var historical, key, state string
	if err := db.QueryRow("SELECT actor_id,operation_id,state FROM model_calls WHERE id=?", callID).Scan(&historical, &key, &state); err != nil || historical == actor.ID || !strings.HasPrefix(key, "retired-v1:") || state != "UNKNOWN" {
		t.Fatal("historical identity lost", state, err)
	}
	var enabled bool
	if err := db.QueryRow("SELECT enabled FROM users WHERE id=?", historical).Scan(&enabled); err != nil || enabled {
		t.Fatal("cleanup reenabled historical actor", err)
	}
	var reserved int64
	if err := db.QueryRow("SELECT reserved_micros FROM model_budgets WHERE workspace_id=?", actor.WorkspaceID).Scan(&reserved); err != nil || reserved != 80 {
		t.Fatal("historical cleanup changed reservation", reserved, err)
	}
	if _, err := scanner.StartJob(context.Background(), historical, project, preview.ID, preview.PlanHash); err != domain.ErrForbidden {
		t.Fatal("disabled historical actor started maintenance", err)
	}
}

func TestRetentionJobRequestResumeRejectsMissingAlias(t *testing.T) {
	scanner, db, _, _, preview, _ := prepareRequestCleanupJob(t, false)
	if state, err := scanner.AdvanceJob(context.Background(), preview.ID); err != nil || state != "DETACHING" {
		t.Fatal(state, err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=?", preview.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("first retirement did not complete", receipts, err)
	}
	if _, err := db.Exec("DELETE FROM retention_operation_keys"); err != nil {
		t.Fatal(err)
	}
	if state, err := scanner.AdvanceJob(context.Background(), preview.ID); err == nil || state == "COMPLETE" {
		t.Fatal("receipt without independent alias was trusted", state, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=?", preview.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("missing alias allowed further mutations", receipts, err)
	}
}
