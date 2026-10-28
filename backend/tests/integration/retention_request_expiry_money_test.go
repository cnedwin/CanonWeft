package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionRequestExpiryKeepsUnknownReservationAndOriginalCall(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "编号清理费用保护", "request-key-money-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string)}
	ctx := context.Background()
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	const key = "private-model-request-key"
	call, err := budgets.Reserve(ctx, actor, project, key, 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	owner, _ := retention.RowOwnerKey("model_calls", map[string]string{"id": call.ID})
	id, _ := retention.ReferenceID(project, "model_calls", owner, "operation_id")
	h := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(h[:])
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'model_calls',?,'operation_id','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hash); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": call.ID}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var inputHash string
	if err := db.QueryRow("SELECT input_hash FROM model_calls WHERE id=?", call.ID).Scan(&inputHash); err != nil {
		t.Fatal(err)
	}
	input := retention.RequestKeyRetirement{WorkspaceID: actor.WorkspaceID, ActorID: actor.ID, Table: "model_calls", OriginalKey: key, SourceID: call.ID, RequestHash: inputHash, OriginalOwnerKey: owner}
	var revision int64
	var expiry retention.Expiry
	var proof retention.ExpiredProjection
	prepare := func() {
		t.Helper()
		v := capturedVersion(t, db, project, "model_calls", call.ID, "operation_id")
		expiry = retention.Expiry{ReferenceID: id, Rule: v.Reference.Rule, PayloadHash: hash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: retention.Inline}
		if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		// Completed-scan fixture only; registration itself cannot grant CURRENT.
		if _, err := db.Exec("UPDATE project_lifecycle SET state='CURRENT',scanned_at=UTC_TIMESTAMP(6) WHERE project_id=?", project); err != nil {
			t.Fatal(err)
		}
		var clock string
		if err := db.QueryRow("SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
			t.Fatal(err)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		proof, err = retention.ProjectRetiredRequestKey(project, expiry, input, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := retention.ApplyRequestKeyExpiryTx(ctx, tx, ledger, project, input, revision, expiry, proof); err != nil {
			return err
		}
		return tx.Commit()
	}
	prepare()
	if err := apply(); err == nil {
		t.Fatal("reserved request key expired while active")
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	prepare()
	if err := apply(); err == nil {
		t.Fatal("dispatched request key expired while active")
	}
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "provider-unknown", ""); err != nil {
		t.Fatal(err)
	}
	prepare()
	if err := apply(); err != nil {
		t.Fatal("unknown request key expiry failed", err)
	}
	if err := apply(); err != nil {
		t.Fatal("unknown request expiry replay failed", err)
	}
	var state, actualKey string
	var reserved, charged, spent, accountReserved int64
	if err := db.QueryRow("SELECT state,operation_id,reserved_micros,charged_micros FROM model_calls WHERE id=?", call.ID).Scan(&state, &actualKey, &reserved, &charged); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT spent_micros,reserved_micros FROM model_budgets WHERE workspace_id=?", actor.WorkspaceID).Scan(&spent, &accountReserved); err != nil {
		t.Fatal(err)
	}
	if state != "UNKNOWN" || actualKey != "retired-v1:"+hash || reserved != 80 || charged != 0 || spent != 0 || accountReserved != 80 {
		t.Fatal("identifier expiry changed unknown accounting", state, reserved, charged, spent, accountReserved)
	}
	replay, err := budgets.Reserve(ctx, actor, project, key, 80, "USD", strings.Repeat("a", 64))
	if err != nil || replay.ID != call.ID || replay.State != "UNKNOWN" {
		t.Fatal("old request created another model call", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_calls WHERE project_id=?", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate call after key expiry", count, err)
	}
	missing, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Rollback()
	if _, err := missing.Exec("DELETE FROM retention_operation_keys WHERE actor_id=? AND owner_table='model_calls'", actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := retention.ApplyRequestKeyExpiryTx(ctx, missing, ledger, project, input, revision, expiry, proof); err == nil {
		t.Fatal("exact retry recreated missing independent alias")
	}
	missing.Rollback()
}
