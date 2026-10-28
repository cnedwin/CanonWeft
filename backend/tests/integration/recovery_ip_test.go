package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type corruptRecoveryIPBlobs struct{ storage.LocalBlobs }

func (b corruptRecoveryIPBlobs) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	return []byte("mismatched immutable source bytes"), nil
}

func TestRecoveryIPCopiesBranchHistoryDraftsAndPendingChanges(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	actorID := b.request("GET", "/api/me", nil, 200)["id"].(string)
	original := createProject(b, "恢复设定原作", "recovery-ip-original")
	canon := branch(b, original, "正史")
	baseSnapshot := originalSnapshot()
	baseSnapshot.Assertions[0].Locked = true
	base := propose(b, original, canon, "", "recovery-ip-base", baseSnapshot)
	publish(b, original, canon, base)
	child := b.request("POST", "/api/projects/"+original+"/branches", map[string]any{"name": "改编", "operation_id": "recovery-ip-child", "parent_id": canon, "base_revision": base}, 201)["id"].(string)
	adapted := originalSnapshot()
	adapted.Assertions[0].Locked = true
	adapted.Entities[0].Name = "改编林舟"
	adapted.Entities = append(adapted.Entities, domain.Entity{ID: "herb", Kind: "item", Name: "返照草"})
	childHead := propose(b, original, child, base, "recovery-ip-child-head", adapted)
	publish(b, original, child, childHead)
	latest := originalSnapshot()
	latest.Assertions[0].Locked = true
	latest.Entities[0].Description = "正史新增的医师经历"
	canonHead := propose(b, original, canon, base, "recovery-ip-canon-head", latest)
	publish(b, original, canon, canonHead)
	canonRoot := "/api/projects/" + original + "/branches/" + canon
	childRoot := "/api/projects/" + original + "/branches/" + child
	merge := map[string]any{"base_head": childHead, "upstream_head": canonHead, "operation_id": "recovery-ip-merge", "resolutions": map[string]string{"entity:lin-1": "local"}}
	merge["change_reasons"] = deltaReasons(b.request("POST", childRoot+"/merge-delta-preview", merge, 200))
	mergeID := b.request("POST", childRoot+"/merge", merge, 201)["id"].(string)
	promotion := map[string]any{"base_head": canonHead, "source_branch": child, "source_head": childHead, "operation_id": "recovery-ip-promotion", "reason": "采用药草设定", "selection": map[string]any{"entities": []string{"herb"}}}
	promotion["change_reasons"] = deltaReasons(b.request("POST", canonRoot+"/promotion-delta-preview", promotion, 200))
	promotionID := b.request("POST", canonRoot+"/promotions", promotion, 201)["id"].(string)
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	svc := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	modified := originalSnapshot()
	modified.Assertions[0].Locked = true
	modified.Assertions[0].Value = "游医"
	reasons := deltaReasons(b.request("POST", canonRoot+"/delta-preview", map[string]any{"base_head": canonHead, "snapshot": modified}, 200))
	amendment, err := svc.ProposeAmendmentChanges(ctx, domain.Actor{ID: actorID}, original, canon, canonHead, "recovery-ip-amendment", "作者尚未采用的身份修订", modified, reasons)
	if err != nil {
		t.Fatal(err)
	}
	draft := service.IPDraft{BaseHead: canonHead, Snapshot: latest, Amendment: true, AmendmentReason: "尚在斟酌", BranchActions: &domain.IPBranchActionDraft{Promotion: &domain.PromotionInput{SourceBranch: child, SourceHead: childHead, BaseHead: canonHead, OperationID: "old-draft-request", Reason: "保留我的规划意见", Selection: domain.PromotionSelection{Entities: []string{"herb"}}}, Merge: &domain.MergeInput{BaseHead: canonHead, UpstreamHead: "尚未选择", OperationID: "unfinished-merge"}}}
	b.request("PUT", canonRoot+"/draft", draft, 200)
	target := createProject(b, "设定独立副本", "recovery-ip-target")
	ids := service.RecoveryIPIDs{SourceProject: original, TargetProject: target, Branches: map[string]string{canon: service.NewID(), child: service.NewID()}, Revisions: map[string]string{}}
	for _, id := range []string{base, childHead, canonHead, mergeID, promotionID, amendment.ID} {
		ids.Revisions[id] = service.NewID()
	}
	var revision int64
	if err = db.QueryRow("SELECT revision FROM projects WHERE id=?", original).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+original, map[string]any{"confirm_name": "恢复设定原作", "revision": revision, "operation_id": "recovery-ip-delete"}, 200)
	// Missing historical content cannot silently become a latest-version copy.
	badIDs := ids
	badIDs.Revisions = map[string]string{}
	for source, destination := range ids.Revisions {
		badIDs.Revisions[source] = destination
	}
	delete(badIDs.Revisions, base)
	badIDs.Revisions[service.NewID()] = service.NewID()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CopyRecoveryIPTx(ctx, tx, actorID, badIDs)
	tx.Rollback()
	if err == nil {
		t.Fatal("missing historical version accepted")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := &service.IP{Workspace: svc.Workspace, Blobs: corruptRecoveryIPBlobs{LocalBlobs: blobs}}
	_, err = corrupt.CopyRecoveryIPTx(ctx, tx, actorID, ids)
	tx.Rollback()
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("unverified source bytes accepted", err)
	}
	// Deliberately fail after branches, versions and adoption records exist.
	if _, err = db.Exec("CREATE TRIGGER recovery_ip_fault BEFORE INSERT ON ip_branch_deltas FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test recovery fault'"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, copyErr := svc.CopyRecoveryIPTx(ctx, tx, actorID, ids)
	if copyErr == nil || counts != (service.RecoveryIPCounts{}) {
		tx.Rollback()
		t.Fatal("late failure did not reject copy", counts, copyErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TRIGGER recovery_ip_fault"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT COUNT(*) FROM branches WHERE project_id=?", target).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial branch graph visible", n, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = svc.CopyRecoveryIPTx(ctx, tx, actorID, ids)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if counts != (service.RecoveryIPCounts{Branches: 2, Versions: 6, Drafts: 1}) {
		tx.Rollback()
		t.Fatal("incomplete IP history", counts)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	newRoot := "/api/projects/" + target + "/branches/" + ids.Branches[canon]
	newChild := "/api/projects/" + target + "/branches/" + ids.Branches[child]
	state := b.request("GET", newChild, nil, 200)
	if state["parent_id"] != ids.Branches[canon] || state["base_revision"] != ids.Revisions[base] || state["head"] != ids.Revisions[childHead] || state["snapshot"].(map[string]any)["entities"].([]any)[0].(map[string]any)["name"] != "改编林舟" {
		t.Fatal("adaptation lost its separate ancestry", state)
	}
	loaded := b.request("GET", newRoot+"/draft", nil, 200)
	actions := loaded["branch_actions"].(map[string]any)
	p := actions["promotion"].(map[string]any)
	if loaded["base_head"] != ids.Revisions[canonHead] || loaded["amendment_reason"] != "尚在斟酌" || p["source_branch"] != ids.Branches[child] || p["source_head"] != ids.Revisions[childHead] || p["operation_id"] == "old-draft-request" || actions["merge"].(map[string]any)["upstream_head"] != "尚未选择" {
		t.Fatal("unfinished draft changed or retained executable old request", loaded)
	}
	history := b.request("GET", newRoot+"/candidates", nil, 200)["items"].([]any)
	if len(history) != 4 {
		t.Fatal("canon history flattened", len(history))
	}
	var sawPromotion, sawAmendment bool
	for _, raw := range history {
		item := raw.(map[string]any)
		if item["id"] == ids.Revisions[promotionID] {
			sawPromotion = item["requires_semantic_review"] == true && item["promotion"].(map[string]any)["source_branch"] == ids.Branches[child]
		}
		if item["id"] == ids.Revisions[amendment.ID] {
			sawAmendment = item["amendment_reason"] == "作者尚未采用的身份修订"
		}
	}
	if !sawPromotion || !sawAmendment {
		t.Fatal("pending change metadata lost")
	}
	b.request("POST", newRoot+"/candidates/"+ids.Revisions[promotionID]+"/adopt", map[string]any{}, 503)
	changed := latest
	changed.Entities[0].Description = "只修改新副本"
	fresh := propose(b, target, ids.Branches[canon], ids.Revisions[canonHead], "recovery-new-edit", changed)
	publish(b, target, ids.Branches[canon], fresh)
	var originalHead string
	if err = db.QueryRow("SELECT head_revision FROM branches WHERE id=?", canon).Scan(&originalHead); err != nil || originalHead != canonHead {
		t.Fatal("new edit modified source", originalHead, err)
	}
	if _, err = backup.Create(ctx, db, blobs, filepath.Join(t.TempDir(), "ip-recovered.zip")); err != nil {
		t.Fatal("recovered history cannot be archived", err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CopyRecoveryIPTx(ctx, tx, actorID, ids)
	tx.Rollback()
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("replay overwrote edited copy", err)
	}
}
