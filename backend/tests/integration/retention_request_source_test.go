package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionRequestSourceReadsOriginalIdentityAfterKeyRetirement(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	me := b.request("GET", "/api/me", nil, 200)
	actor, workspace := me["id"].(string), me["workspace_id"].(string)
	const key = "private-request-source-original"
	svc := &service.Workspace{DB: db}
	project, err := svc.CreateProject(ctx, actor, "原请求读取", key)
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	owner, _ := retention.RowOwnerKey("operations", map[string]string{"actor_id": actor, "operation_id": key})
	id, _ := retention.ReferenceID(project.ID, "operations", owner, "operation_id")
	digest := func(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
	originalHash := digest([]byte(key))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'operations',?,'operation_id','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project.ID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, originalHash); err != nil {
		t.Fatal(err)
	}
	target := retention.SourceKey{Table: "operations", Primary: map[string]string{"actor_id": actor, "operation_id": key}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project.ID, target); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var parentRaw, parentHash, fingerprint string
	if err := db.QueryRow("SELECT snapshot,record_hash FROM retention_reference_versions WHERE reference_id=? AND revision=1", id).Scan(&parentRaw, &parentHash); err != nil {
		t.Fatal(err)
	}
	prior, err := retention.ValidateReferenceVersionRecord(parentRaw, parentHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT request_hash FROM operations WHERE actor_id=? AND operation_id=?", actor, key).Scan(&fingerprint); err != nil {
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
	input := retention.RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor, Table: "operations", OriginalKey: key, SourceID: project.ID, RequestHash: fingerprint, Kind: "project.create", OriginalOwnerKey: owner}
	expiry := retention.Expiry{ReferenceID: id, Rule: prior.Reference.Rule, PayloadHash: originalHash, Revision: 1, Deadline: prior.Deadline, Storage: retention.Inline}
	proof, err := retention.ProjectRetiredRequestKey(project.ID, expiry, input, at)
	if err != nil {
		t.Fatal(err)
	}
	var inventoryRevision int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project.ID).Scan(&inventoryRevision); err != nil {
		t.Fatal(err)
	}
	var retired retention.RetiredRequestKey
	apply := func(revision int64, commit bool) error {
		writeTx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer writeTx.Rollback()
		retired, err = retention.ApplyRequestKeyExpiryTx(ctx, writeTx, ledger, project.ID, input, revision, expiry, proof)
		if err != nil {
			return err
		}
		if commit {
			return writeTx.Commit()
		}
		return writeTx.Rollback()
	}
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("incomplete source inventory authorized key expiry")
	}
	// Isolated completed-scan fixture: the production full scanner remains a
	// separate prerequisite. Every actual source/alias/proof write below uses
	// the real atomic executor, including fault injection and exact retries.
	if _, err := db.Exec("UPDATE project_lifecycle SET state='CURRENT',scanned_at=UTC_TIMESTAMP(6) WHERE project_id=?", project.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision+1, true); err == nil {
		t.Fatal("stale inventory revision authorized key expiry")
	}
	proof.ExpiredAt = at.Add(24 * time.Hour)
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("future proof clock authorized key expiry")
	}
	proof.ExpiredAt = at
	if err := apply(inventoryRevision, false); err != nil {
		t.Fatal("rollback execution failed", err)
	}
	assertOriginal := func() {
		t.Helper()
		var original string
		if err := db.QueryRow("SELECT operation_id FROM operations WHERE actor_id=? AND result_id=?", actor, project.ID).Scan(&original); err != nil || original != key {
			t.Fatal("failed expiry changed original key", err)
		}
		var aliases, evidence, closed, versions int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_operation_keys WHERE actor_id=?", actor).Scan(&aliases); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE reference_id=?", id).Scan(&evidence); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_identity_policies WHERE workspace_id=? AND legacy_writes_closed=TRUE", workspace).Scan(&closed); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE reference_id=?", id).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if aliases != 0 || evidence != 0 || closed != 0 || versions != 1 {
			t.Fatal("failed expiry partially committed identity/proof/policy", aliases, evidence, closed, versions)
		}
	}
	assertOriginal()
	if _, err := db.Exec("CREATE TRIGGER reject_request_expiry BEFORE INSERT ON retention_evidence FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected request expiry proof'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_request_expiry") })
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("key retirement committed without its proof")
	}
	assertOriginal()
	if _, err := db.Exec("DROP TRIGGER reject_request_expiry"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_request_current BEFORE UPDATE ON retention_references FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected current reference update'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_request_current") })
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("key retirement committed without current reference transition")
	}
	assertOriginal()
	if _, err := db.Exec("DROP TRIGGER reject_request_current"); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, true); err != nil {
		t.Fatal("atomic request expiry failed", err)
	}
	if err := apply(inventoryRevision, true); err != nil {
		t.Fatal("exact expiry replay failed", err)
	}
	var evidenceCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE reference_id=?", id).Scan(&evidenceCount); err != nil || evidenceCount != 1 {
		t.Fatal("retry duplicated expiry proof", evidenceCount, err)
	}
	var scanState string
	var currentInventory int64
	if err := db.QueryRow("SELECT state,inventory_revision FROM project_lifecycle WHERE project_id=?", project.ID).Scan(&scanState, &currentInventory); err != nil || scanState != "UNSCANNED" || currentInventory != inventoryRevision+1 {
		t.Fatal("expiry did not invalidate inventory exactly once", scanState, currentInventory, err)
	}
	read := func() error {
		readTx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return err
		}
		defer readTx.Rollback()
		snapshot, err := retention.ScanSources(ctx, readTx)
		if err != nil {
			return err
		}
		observations, err := snapshot.Index.SourceObservations(project.ID)
		if err != nil {
			return err
		}
		for _, o := range observations {
			if o.Table == "operations" && o.Field == "operation_id" {
				t.Fatal("anonymous key became new observed source")
			}
		}
		return readTx.Commit()
	}
	if err := read(); err != nil {
		t.Fatal("read-only snapshot rejected original identity", err)
	}
	target.Primary["operation_id"] = retired.AnonymousKey
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project.ID, target); err != nil {
		tx.Rollback()
		t.Fatal("selected recapture rejected original identity", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots WHERE project_id=? AND owner_table='operations' AND field_key='operation_id'", project.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("anonymous source renewed identity", count, err)
	}
	replayed, err := svc.CreateProject(ctx, actor, project.Name, key)
	if err != nil || replayed.ID != project.ID {
		t.Fatal("retired request lost original result", err)
	}
	// A capture already owns the project lock. It must not wait behind a
	// retirement holding actor/policy locks and waiting for that same project.
	policyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer policyTx.Rollback()
	var policyRevision int64
	if err := policyTx.QueryRow("SELECT revision FROM retention_identity_policies WHERE workspace_id=? FOR UPDATE", workspace).Scan(&policyRevision); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	writeTx, err := db.BeginTx(bounded, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	conflict := retention.CaptureSourceValues(bounded, writeTx, ledger, project.ID, target)
	writeTx.Rollback()
	policyTx.Rollback()
	if conflict == nil || time.Since(started) >= time.Second {
		t.Fatal("capture waited behind reverse-order policy lock", conflict, time.Since(started))
	}
	oldSnapshot, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer oldSnapshot.Rollback()
	if err := oldSnapshot.QueryRow("SELECT COUNT(*) FROM retention_operation_keys WHERE actor_id=?", actor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_operation_keys SET operation_kind='project.delete' WHERE actor_id=?", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := retention.ScanSources(ctx, oldSnapshot); err != nil {
		t.Fatal("read-only scan mixed newer alias into older source snapshot", err)
	}
	if err := oldSnapshot.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := read(); err == nil {
		t.Fatal("read-only inventory accepted altered independent alias")
	}
	if _, err := db.Exec("UPDATE retention_operation_keys SET operation_kind='project.create' WHERE actor_id=?", actor); err != nil {
		t.Fatal(err)
	}
	missing, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Rollback()
	if _, err := missing.Exec("DELETE FROM retention_references WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, missing, ledger, project.ID, target); err == nil {
		t.Fatal("selected capture registered anonymous key after original current proof disappeared")
	}
	missing.Rollback()
}
