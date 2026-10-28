package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type cleanupExecution interface {
	StartJob(context.Context, string, string, string, string) (service.RetentionJobReport, error)
	AdvanceJob(context.Context, string) (string, error)
}

func prepareCleanupExecution(t *testing.T) (*service.RetentionScanner, *sql.DB, string, string, service.RetentionJobReport, []string) {
	t.Helper()
	b, db := setup(t)
	project := createProject(b, "断点续办清理任务", "retention-job-execution")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	ctx := context.Background()
	budgets := service.Budgets{Workspace: &service.Workspace{DB: db}}
	if _, err := budgets.Configure(ctx, actor, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	for i := range 2 {
		call, err := budgets.Reserve(ctx, domain.Actor{ID: actor}, project, "cleanup-call-"+strings.Repeat("x", i+1), 80, "USD", strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		if err := budgets.Dispatch(ctx, domain.Actor{ID: actor}, project, call.ID); err != nil {
			t.Fatal(err)
		}
		if err := budgets.Settle(ctx, call.ID, "SUCCEEDED", 37, "OLD_PRIVATE_RECEIPT", ""); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call.ID)
	}
	unknown, err := budgets.Reserve(ctx, domain.Actor{ID: actor}, project, "cleanup-unknown", 80, "USD", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, domain.Actor{ID: actor}, project, unknown.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, unknown.ID, "UNKNOWN", 0, "unknown receipt", ""); err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	for _, id := range calls {
		key := retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": id}}
		owner, _ := retention.RowOwnerKey(key.Table, key.Primary)
		ref, _ := retention.ReferenceID(project, key.Table, owner, "provider_receipt")
		hash := sha256.Sum256([]byte("OLD_PRIVATE_RECEIPT"))
		if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'model_calls',?,'provider_receipt','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := retention.CaptureSourceValues(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, key); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	scanner := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	preview, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil || preview.Expiring != 2 {
		t.Fatal("fixture must plan actual due source fields", preview, err)
	}
	return scanner, db, actor, project, preview, calls
}

func TestRetentionJobExecutesSourcesWithDurableReceipts(t *testing.T) {
	scanner, db, actor, project, preview, calls := prepareCleanupExecution(t)
	executor, ok := any(scanner).(cleanupExecution)
	if !ok {
		t.Fatal("durable cleanup execution is not connected to the real scanner")
	}
	ctx := context.Background()
	if state, err := executor.AdvanceJob(ctx, preview.ID); err != nil || state != "PLANNED" {
		t.Fatal("unstarted preview executed", state, err)
	}
	if _, err := executor.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", actor); err != nil {
		t.Fatal(err)
	}
	if state, err := executor.AdvanceJob(ctx, preview.ID); err != domain.ErrForbidden || state != "" {
		t.Fatal("revoked owner executed background work", state, err)
	}
	if _, err := db.Exec("UPDATE users SET enabled=TRUE WHERE id=?", actor); err != nil {
		t.Fatal(err)
	}
	if state, err := executor.AdvanceJob(ctx, preview.ID); err != nil || state != "DETACHING" {
		t.Fatal("first source batch", state, err)
	}
	var detached int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND phase='DETACHED'", preview.ID).Scan(&detached); err != nil || detached != 1 {
		t.Fatal("first receipt was not committed", detached, err)
	}
	// A fresh service resumes from SQL, with no process-local progress.
	restarted := &service.RetentionScanner{Workspace: scanner.Workspace, Blobs: scanner.Blobs}
	executor = any(restarted).(cleanupExecution)
	var state string
	for range 5 {
		var err error
		state, err = executor.AdvanceJob(ctx, preview.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETE" {
			break
		}
	}
	if state != "COMPLETE" {
		t.Fatal("source cleanup never completed", state)
	}
	for _, id := range calls {
		var value string
		if err := db.QueryRow("SELECT provider_receipt FROM model_calls WHERE id=?", id).Scan(&value); err != nil || value != "" {
			t.Fatal("original source remains", err)
		}
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts WHERE job_id=?", preview.ID).Scan(&receipts); err != nil || receipts != 4 {
		t.Fatal("missing exact detach/verify receipts", receipts, err)
	}
	var reserved, spent int64
	if err := db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets").Scan(&reserved, &spent); err != nil || reserved != 80 || spent != 74 {
		t.Fatal("cleanup changed financial accounting", reserved, spent, err)
	}
	if replay, err := executor.AdvanceJob(ctx, preview.ID); err != nil || replay != "COMPLETE" {
		t.Fatal("completed retry failed", replay, err)
	}
	if _, err := backup.Create(ctx, db, scanner.Blobs, filepath.Join(t.TempDir(), "completed-cleanup.zip")); err != nil {
		t.Fatal("actual completed receipts break backup admission", err)
	}
}

func TestRetentionJobReceiptFailureRollsBackAndResumes(t *testing.T) {
	scanner, db, actor, project, preview, calls := prepareCleanupExecution(t)
	executor, ok := any(scanner).(cleanupExecution)
	if !ok {
		t.Fatal("executor missing")
	}
	ctx := context.Background()
	if _, err := executor.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_cleanup_receipt BEFORE INSERT ON retention_receipts FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='PRIVATE_SQL_FAILURE_MUST_NOT_LEAK'"); err != nil {
		t.Fatal(err)
	}
	state, err := executor.AdvanceJob(ctx, preview.ID)
	if err == nil || state != "RETRY_PENDING" || strings.Contains(err.Error(), "PRIVATE_SQL") {
		t.Fatal("receipt failure not recorded safely", state, err)
	}
	var savedState, reason string
	if err := db.QueryRow("SELECT state,reason_code FROM retention_jobs WHERE id=?", preview.ID).Scan(&savedState, &reason); err != nil || savedState != "RETRY_PENDING" || reason != "SOURCE_STEP_FAILED" {
		t.Fatal("failure status not durable", savedState, reason, err)
	}
	for _, id := range calls {
		var value string
		if err := db.QueryRow("SELECT provider_receipt FROM model_calls WHERE id=?", id).Scan(&value); err != nil || value != "OLD_PRIVATE_RECEIPT" {
			t.Fatal("source change escaped receipt rollback", err)
		}
	}
	for _, table := range []string{"retention_receipts", "retention_evidence"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial execution proof survived", table, count, err)
		}
	}
	var current int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle WHERE state='CURRENT'").Scan(&current); err != nil || current != 0 {
		t.Fatal("failed work retained CURRENT", err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_cleanup_receipt"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		state, err = executor.AdvanceJob(ctx, preview.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETE" {
			break
		}
	}
	if state != "COMPLETE" {
		t.Fatal("retry did not finish", state)
	}
}

func TestRetentionJobRefusesRestoredOrForgedProgress(t *testing.T) {
	scanner, db, actor, project, preview, _ := prepareCleanupExecution(t)
	executor, ok := any(scanner).(cleanupExecution)
	if !ok {
		t.Fatal("executor missing")
	}
	ctx := context.Background()
	if _, err := executor.StartJob(ctx, actor, project, preview.ID, strings.Repeat("f", 64)); err != domain.ErrConflict {
		t.Fatal("wrong plan accepted", err)
	}
	if _, err := executor.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_jobs SET restored_disabled=TRUE WHERE id=?", preview.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := executor.AdvanceJob(ctx, preview.ID); err != domain.ErrConflict || state != "" {
		t.Fatal("restored job ran or falsely reported durable retry", state, err)
	}
	var state string
	if err := db.QueryRow("SELECT state FROM retention_jobs WHERE id=?", preview.ID).Scan(&state); err != nil || state != "DETACHING" {
		t.Fatal("disabled restoration state changed", state, err)
	}
	if _, err := db.Exec("UPDATE retention_jobs SET restored_disabled=FALSE WHERE id=?", preview.ID); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT plan FROM retention_jobs WHERE id=?", preview.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	plan, err := retention.ValidatePlanRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"DETACHED", "VERIFIED"} {
		if _, err := db.Exec("INSERT INTO retention_receipts(job_id,item_key,phase,result,evidence_hash) VALUES(?,?,?,'SUCCEEDED',?)", preview.ID, plan.Expire[0].ReferenceID, phase, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE retention_jobs SET updated_at=UTC_TIMESTAMP(6) WHERE id=?", preview.ID); err != nil {
		t.Fatal(err)
	}
	state, err = executor.AdvanceJob(ctx, preview.ID)
	if err != nil || state != "BLOCKED" {
		t.Fatal("forged receipts permitted progress", state, err)
	}
	var expired int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE state='EXPIRED'").Scan(&expired); err != nil || expired != 0 {
		t.Fatal("forged progress erased sources", err)
	}
}

func TestRetentionJobRejectsChangedSourcesAndReceiptClocks(t *testing.T) {
	for _, mode := range []string{"source_changed", "receipt_clock", "receipt_schema"} {
		t.Run(mode, func(t *testing.T) {
			scanner, db, actor, project, preview, calls := prepareCleanupExecution(t)
			executor := any(scanner).(cleanupExecution)
			ctx := context.Background()
			if _, err := executor.StartJob(ctx, actor, project, preview.ID, preview.PlanHash); err != nil {
				t.Fatal(err)
			}
			if mode == "source_changed" {
				var raw, owner string
				if err := db.QueryRow("SELECT plan FROM retention_jobs WHERE id=?", preview.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				plan, err := retention.ValidatePlanRecord(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow("SELECT owner_key FROM retention_references WHERE id=?", plan.Expire[0].ReferenceID).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				var parts []string
				if err := json.Unmarshal([]byte(owner), &parts); err != nil || len(parts) != 1 {
					t.Fatal("invalid test owner", err)
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec("UPDATE model_calls SET provider_receipt='NEW_RECEIPT' WHERE id=?", parts[0]); err != nil {
					t.Fatal(err)
				}
				if err := retention.CaptureSourceValues(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": parts[0]}}); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else if mode == "receipt_clock" {
				if state, err := executor.AdvanceJob(ctx, preview.ID); err != nil || state != "DETACHING" {
					t.Fatal(state, err)
				}
				if _, err := db.Exec("UPDATE retention_receipts SET recorded_at='2000-01-01 00:00:00.000000' WHERE job_id=? AND phase='VERIFIED'", preview.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Exec("ALTER TABLE retention_receipts MODIFY recorded_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)"); err != nil {
					t.Fatal(err)
				}
			}
			state, err := executor.AdvanceJob(ctx, preview.ID)
			if mode == "receipt_schema" {
				if err != domain.ErrNotReady || state != "" {
					t.Fatal("receipt schema drift accepted", state, err)
				}
			} else if err != nil || state != "BLOCKED" {
				t.Fatal("changed source/receipt accepted", state, err)
			}
			var cleared int
			if err := db.QueryRow("SELECT COUNT(*) FROM model_calls WHERE provider_receipt='' AND id IN (?,?)", calls[0], calls[1]).Scan(&cleared); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "receipt_clock" {
				want = 1
			}
			if cleared != want {
				t.Fatal("unsafe progress after changed source/receipt", cleared, want)
			}
		})
	}
}
