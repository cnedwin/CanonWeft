package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionExpiryTransactionRollbackReplayAndRuntimeRefusal(t *testing.T) {
	testRetentionExpiryTransaction(t, true)
}

func TestRetentionExpiredContainerPreservesLiveNestedReference(t *testing.T) {
	testRetentionExpiryTransaction(t, false)
}

func testRetentionExpiryTransaction(t *testing.T, expireNested bool) {
	t.Helper()
	transport := &generationTransport{response: generationSuccess}
	_, svc, actor, project, task := generationFixture(t, transport)
	db, ctx := svc.IP.Workspace.DB, context.Background()
	budgets := &service.Budgets{Workspace: svc.IP.Workspace}
	call, err := budgets.Reserve(ctx, actor, project, "unresolved-budget", 37, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	budgetBefore, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	var raw string
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	owner, _ := retention.RowOwnerKey("generation_tasks", map[string]string{"id": task.ID})
	id, _ := retention.ReferenceID(project, "generation_tasks", owner, "frozen")
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'generation_tasks',?,'frozen','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	key := retention.SourceKey{Table: "generation_tasks", Primary: map[string]string{"id": task.ID}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValuesWithObjects(ctx, tx, ledger, project, svc.IP.Blobs.ReadObject, key); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	v := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen")
	var inventoryRevision int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&inventoryRevision); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	expiry := retention.Expiry{ReferenceID: id, Rule: "generation_tasks.frozen", PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: retention.Inline}
	contractHash := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var canonical any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&canonical); err != nil {
			t.Fatal(err)
		}
		encoded, err = json.Marshal(canonical)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		return hex.EncodeToString(sum[:])
	}
	binding := &retention.GenerationBindingSummary{Schema: 1, RecipeRunID: task.Frozen.RecipeRunID, RecipeStepID: task.Frozen.RecipeStepID, RecipeInputHash: task.Frozen.RecipeInputHash,
		Role: task.Frozen.Role, BaseHead: task.Frozen.BaseHead, ContextID: task.Frozen.ContextID, SemanticReviewID: task.Frozen.SemanticReviewID, ChangePlanID: task.Frozen.ChangePlanID, PriceHash: contractHash(task.Frozen.Price)}
	summary := retention.EvidenceSummary{Generation: binding, InputHash: task.Frozen.InputHash, State: task.State, CallID: task.CallID,
		ConfigID: task.Frozen.Config.ID, ConfigRevision: task.Frozen.Config.Revision, ConfigHash: contractHash(task.Frozen.Config), Price: &retention.PriceSummary{Currency: task.Frozen.Price.Currency}}
	if use := task.Frozen.Capability; use != nil {
		binding.AgentVersionID, binding.ManifestHash = use.AgentVersionID, use.ManifestHash
		summary.Capability = &retention.CapabilitySummary{Mode: use.Mode, EvaluationID: use.EvaluationID, EvaluationStepID: use.EvaluationStepID, SuiteHash: use.SuiteHash}
	}
	proof, err := retention.ProjectExpired(expiry, []byte(raw), summary, at)
	if err != nil {
		t.Fatal(err)
	}
	inputVersion := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen.input_hash")
	inputExpiry := retention.Expiry{ReferenceID: inputVersion.Reference.ID, Rule: inputVersion.Reference.Rule, PayloadHash: inputVersion.Reference.PayloadHash, Revision: inputVersion.Reference.Revision, Deadline: inputVersion.Deadline, Storage: retention.Blob}
	inputProof, err := retention.ProjectExpired(inputExpiry, []byte(task.Frozen.InputHash), retention.EvidenceSummary{ValueHash: task.Frozen.InputHash}, at)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(revision int64, commit bool) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		expiries, proofs := []retention.Expiry{expiry}, []retention.ExpiredProjection{proof}
		if expireNested {
			expiries, proofs = append(expiries, inputExpiry), append(proofs, inputProof)
		}
		if err = retention.ApplySourceExpiryTx(ctx, tx, ledger, project, key, revision, expiries, proofs); err != nil {
			return err
		}
		if commit {
			return tx.Commit()
		}
		return tx.Rollback()
	}
	if err := apply(inventoryRevision, false); err == nil {
		t.Fatal("partial source registration authorized expiry without a complete scan")
	}
	// Model the trusted coordinator's completed scan in this isolated fixture.
	// Production registration never sets CURRENT; the real scanner/job path is
	// still required before this internal write adapter can be exposed.
	if _, err := db.Exec("UPDATE project_lifecycle SET state='CURRENT',scanned_at=UTC_TIMESTAMP(6) WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	proof.ExpiredAt = at.Add(24 * time.Hour)
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("future expiry clock accepted")
	}
	proof.ExpiredAt = at
	if _, err := db.Exec("UPDATE generation_tasks SET state='RUNNING' WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("running source was replaced")
	}
	if _, err := db.Exec("UPDATE generation_tasks SET state=? WHERE id=?", task.State, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, false); err != nil {
		t.Fatal("real source expiry transaction unavailable", err)
	}
	var actual string
	var count int
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&actual); err != nil || actual != raw {
		t.Fatal("rollback changed source", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE reference_id=?", id).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback left evidence", count, err)
	}
	if err := apply(inventoryRevision+1, true); err == nil {
		t.Fatal("wrong inventory revision accepted")
	}
	if _, err := db.Exec("CREATE TRIGGER reject_expiry_evidence BEFORE INSERT ON retention_evidence FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected expiry evidence'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_expiry_evidence") })
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("source replacement committed without proof")
	}
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&actual); err != nil || actual != raw {
		t.Fatal("proof failure left replaced source", err)
	}
	if capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen").Reference.Revision != v.Reference.Revision {
		t.Fatal("proof failure left partial history")
	}
	if _, err := db.Exec("DROP TRIGGER reject_expiry_evidence"); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, true); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, true); err != nil {
		t.Fatal("exact retry failed", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_evidence WHERE reference_id=?", id).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry wrote another expiry", count, err)
	}
	next := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen")
	if next.Reference.State != retention.Expired || next.Reference.Revision != v.Reference.Revision+1 || !next.Reference.FirstVerifiedAt.Equal(v.Reference.FirstVerifiedAt) {
		t.Fatal("expiry lost immutable history or renewed age")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := retention.RegisterSourceObservations(ctx, tx, ledger, project, svc.IP.Blobs.ReadObject)
	if err != nil {
		tx.Rollback()
		t.Fatal("full registration cannot read its own expired records", err)
	}
	if report.UnresolvedReferences != 0 {
		tx.Rollback()
		t.Fatal("verified expiry treated as unexplained disappearance", report)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if report.ExpiredReferences != 1 && !expireNested || report.ExpiredReferences != 2 && expireNested {
		t.Fatal("expired inventory count is incorrect", report)
	}
	// A later selected write must preserve the same erased input and live
	// nested reference without attempting to reconstruct frozen execution data.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	observedKey := key
	observedKey.ObserveOnly = true
	if err := retention.CaptureSourceValuesWithObjects(ctx, tx, ledger, project, nil, observedKey, key); err != nil {
		tx.Rollback()
		t.Fatal("selected capture cannot retain expired generation history", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !expireNested {
		retainedInput := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen.input_hash")
		if retainedInput.Reference.State != retention.Retained || retainedInput.Reference.Revision != inputVersion.Reference.Revision || retainedInput.SourcePayloadHash != inputVersion.SourcePayloadHash || !retainedInput.Reference.FirstVerifiedAt.Equal(inputVersion.Reference.FirstVerifiedAt) {
			t.Fatal("enclosing expiry changed the retained nested reference")
		}
	}
	afterScan := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen")
	if afterScan.Reference.Revision != next.Reference.Revision || afterScan.Reference.State != retention.Expired {
		t.Fatal("rescan resurrected expired source")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values WHERE source_id=?", v.SourceID).Scan(&count); err != nil || count != 1 {
		t.Fatal("expiry envelope registered as new author data", count, err)
	}
	preview, err := svc.IP.Workspace.RetentionInventory(ctx, actor.ID)
	if err != nil || preview.DeletionReady || preview.ProjectCount != 1 || preview.State != "INVENTORIED" {
		t.Fatal("read-only owner preview lost expired history or granted cleanup", preview, err)
	}
	if _, err := svc.Start(ctx, actor, project, task.ID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired source could execute", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("expiry sent a provider request")
	}
	var storedProof string
	if err := db.QueryRow("SELECT projection FROM retention_evidence WHERE reference_id=?", id).Scan(&storedProof); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(proof)
	if storedProof != string(encoded) {
		t.Fatal("proof changed during persistence")
	}
	archivedMaterials := archivedCurrentMaterials(t, db, project)
	keptInput := false
	for _, material := range archivedMaterials {
		if material.ID == id {
			t.Fatal("archive retained erased frozen body as current content")
		}
		if material.ID == inputVersion.Reference.ID {
			keptInput = true
			if material.ObjectHash != inputVersion.Reference.ObjectHash || material.SourcePayloadHash != inputVersion.SourcePayloadHash {
				t.Fatal("archive changed original nested input identity")
			}
		}
	}
	if keptInput == expireNested {
		t.Fatal("archive nested object retention disagrees with actual expiry")
	}
	// Exercise offline backup validation with actual SQL records, not invented
	// fixture history. The serialized journal must include the original parent.
	var journal retention.ExpiryJournalRecord
	if err := db.QueryRow("SELECT snapshot,record_hash FROM retention_reference_versions WHERE reference_id=? AND revision=?", id, next.Reference.Revision).Scan(&journal.Version, &journal.VersionHash); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT snapshot,record_hash FROM retention_reference_versions WHERE reference_id=? AND revision=?", id, v.Reference.Revision).Scan(&journal.Parent, &journal.ParentHash); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id,projection,projection_hash,original_payload_hash,CAST(expired_at AS CHAR) FROM retention_evidence WHERE reference_id=? AND reference_revision=?", id, next.Reference.Revision).Scan(&journal.EvidenceID, &journal.Evidence, &journal.EvidenceHash, &journal.OriginalPayloadHash, &journal.EvidenceExpiredAt); err != nil {
		t.Fatal(err)
	}
	transported, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	var detached retention.ExpiryJournalRecord
	if err := json.Unmarshal(transported, &detached); err != nil {
		t.Fatal(err)
	}
	archivedOwner := retention.StoredOwner{ProjectID: project, Table: "generation_tasks", Key: owner, Field: "frozen"}
	if err := db.QueryRow("SELECT frozen,call_id FROM generation_tasks WHERE id=?", task.ID).Scan(&archivedOwner.Value, &archivedOwner.CallID); err != nil {
		t.Fatal(err)
	}
	archivedRef := retention.StoredReference{ID: id, ProjectID: project, Table: "generation_tasks", OwnerKey: owner, Field: "frozen", Storage: retention.Inline, State: retention.Expired, Revision: next.Reference.Revision, EvidenceRevision: next.Reference.Revision, PayloadHash: next.Reference.PayloadHash, ProjectionHash: next.Expiration.EvidenceHash, Deadline: next.Deadline, ExpiredAt: next.Reference.ExpiredAt, EvidenceExpiredAt: proof.ExpiredAt}
	if read, err := backup.ValidateExpiredSourceJournal(archivedRef, archivedOwner, detached); err != nil || read.Summary.Generation == nil || *read.Summary.Generation != *binding {
		t.Fatal("offline archive rejected actual expiry journal", err)
	}
	withoutParent := detached
	withoutParent.Parent = ""
	if _, err := backup.ValidateExpiredSourceJournal(archivedRef, archivedOwner, withoutParent); err == nil {
		t.Fatal("archive accepted proof without original parent")
	}
	wrongRevision := archivedRef
	wrongRevision.Revision++
	if _, err := backup.ValidateExpiredSourceJournal(wrongRevision, archivedOwner, detached); err == nil {
		t.Fatal("archive current reference ignored journal revision")
	}
	nestedReplay := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return retention.ApplySourceExpiryTx(ctx, tx, ledger, project, key, inventoryRevision, []retention.Expiry{inputExpiry}, []retention.ExpiredProjection{inputProof})
	}
	if expireNested {
		if err := nestedReplay(); err != nil {
			t.Fatal("nested replay lost original input proof", err)
		}
		if _, err := db.Exec("UPDATE generation_tasks SET frozen=JSON_SET(frozen,'$.summary.input_hash',?) WHERE id=?", strings.Repeat("f", 64), task.ID); err != nil {
			t.Fatal(err)
		}
		if err := nestedReplay(); err == nil {
			t.Fatal("nested replay ignored altered enclosing proof")
		}
		if _, err := db.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", encoded, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	budgetAfter, err := budgets.Read(ctx, actor.ID)
	if err != nil || budgetAfter.Reserved != budgetBefore.Reserved || budgetAfter.Spent != budgetBefore.Spent || budgetAfter.Reserved != 37 {
		t.Fatal("cleanup changed unresolved billing", budgetAfter, err)
	}
	if _, err := db.Exec("UPDATE retention_evidence SET projection_hash=? WHERE reference_id=?", strings.Repeat("0", 64), id); err != nil {
		t.Fatal(err)
	}
	if err := apply(inventoryRevision, true); err == nil {
		t.Fatal("corrupt expiry proof accepted on replay")
	}
	if _, err := svc.IP.Workspace.RetentionInventory(ctx, actor.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("owner preview accepted corrupt independent expiry proof", err)
	}
}
