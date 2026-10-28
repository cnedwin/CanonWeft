package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionRestoreKeepsAppliedExpiryAndUnknownMoney(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	w, u, p := randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'expiry recovery')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','test','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'expiry recovery')", p, w)
	execTest(t, db, "INSERT INTO project_members VALUES(?,?,'owner')", p, u)
	ledger := provisionTestAuthority(t, db, admin)
	budgets := service.Budgets{Workspace: &service.Workspace{DB: db}}
	actor := domain.Actor{ID: u, WorkspaceID: w, Role: "owner"}
	if _, err := budgets.Configure(ctx, u, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	const receipt = "old scalar receipt"
	var closed, unknown service.Call
	for _, state := range []string{"SUCCEEDED", "UNKNOWN"} {
		call, err := budgets.Reserve(ctx, actor, p, state, 80, "USD", digest([]byte(state)))
		if err != nil {
			t.Fatal(err)
		}
		if err = budgets.Dispatch(ctx, actor, p, call.ID); err != nil {
			t.Fatal(err)
		}
		amount := int64(0)
		if state == "SUCCEEDED" {
			amount = 37
			closed = call
		} else {
			unknown = call
		}
		// Only the accounting service is exercised; no model transport runs.
		if err = budgets.Settle(ctx, call.ID, state, amount, receipt, ""); err != nil {
			t.Fatal(err)
		}
	}
	installSnapshotRetentionFixture(t, db)
	key := retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": closed.ID}}
	owner, err := retention.RowOwnerKey(key.Table, key.Primary)
	if err != nil {
		t.Fatal(err)
	}
	id, err := retention.ReferenceID(p, key.Table, owner, "provider_receipt")
	if err != nil {
		t.Fatal(err)
	}
	// Verified old source observation fixture; the actual expiry writer below
	// must produce both its transformed field and independent expiry evidence.
	execTest(t, db, "INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'model_calls',?,'provider_receipt','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, p, owner)
	execTest(t, db, "INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, digest([]byte(receipt)))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = retention.CaptureSourceValues(ctx, tx, ledger, p, key); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var snapshot, clock string
	var revision int64
	if err = db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? ORDER BY revision DESC LIMIT 1", id).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if err = json.Unmarshal([]byte(snapshot), &version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT inventory_revision,CAST(UTC_TIMESTAMP(6) AS CHAR) FROM project_lifecycle WHERE project_id=?", p).Scan(&revision, &clock); err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "UPDATE project_lifecycle SET state='CURRENT',scanned_at=?,updated_at=? WHERE project_id=?", clock, clock, p)
	expiry := retention.Expiry{ReferenceID: id, Rule: version.Reference.Rule, PayloadHash: version.Reference.PayloadHash, Revision: version.Reference.Revision, Deadline: version.Deadline, Storage: retention.Inline}
	proof, err := retention.ProjectClearedScalar(expiry, []byte(receipt), retention.EvidenceSummary{}, at)
	if err != nil {
		t.Fatal(err)
	}
	write, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer write.Rollback()
	if err = retention.ApplySourceExpiryTx(ctx, write, ledger, p, key, revision, []retention.Expiry{expiry}, []retention.ExpiredProjection{proof}); err != nil {
		t.Fatal(err)
	}
	if err = write.Commit(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "expired.zip")
	if _, err = Create(ctx, db, storage.LocalBlobs{Root: filepath.Join(root, "source")}, path); err != nil {
		t.Fatal(err)
	}
	opt := RestoreOptions{ArchivePath: path, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, "target"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
	if _, err = Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("mysql", opt.TargetDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var actual, state string
	var charged, reserved, spent int64
	if err = target.QueryRow("SELECT provider_receipt,charged_micros FROM model_calls WHERE id=?", closed.ID).Scan(&actual, &charged); err != nil || actual != "" || charged != 37 {
		t.Fatal("expired scalar or settled amount changed", err)
	}
	if err = target.QueryRow("SELECT state,reserved_micros FROM model_calls WHERE id=?", unknown.ID).Scan(&state, &reserved); err != nil || state != "UNKNOWN" || reserved != 80 {
		t.Fatal("unknown bill released", err)
	}
	if err = target.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", w).Scan(&reserved, &spent); err != nil || reserved != 80 || spent != 37 {
		t.Fatal("restore changed actual money totals", err)
	}
	var count int
	if err = target.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE reference_id=?", id).Scan(&count); err != nil || count != 1 {
		t.Fatal("independent expiry proof lost", err)
	}
	var storedProof string
	if err = target.QueryRow("SELECT projection FROM retention_evidence WHERE reference_id=?", id).Scan(&storedProof); err != nil {
		t.Fatal(err)
	}
	var restoredProof retention.ExpiredProjection
	if err = json.Unmarshal([]byte(storedProof), &restoredProof); err != nil || !restoredProof.ExpiredAt.Equal(proof.ExpiredAt) {
		t.Fatal("original expiry clock changed", err)
	}
}
