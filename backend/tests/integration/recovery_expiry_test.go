package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryKeepsExpiredIPHistoryAndActiveWorld(t *testing.T) {
	for _, provenance := range []bool{false, true} {
		name := "ordinary"
		if provenance {
			name = "with historical source"
		}
		t.Run(name, func(t *testing.T) { recoveryKeepsExpiredIPHistory(t, provenance) })
	}
}

func recoveryKeepsExpiredIPHistory(t *testing.T, provenance bool) {
	b, db := setup(t)
	ctx := context.Background()
	name := "包含过期设定的小说"
	project := createProject(b, name, "recover-expired-ip")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "old-expired-world", originalSnapshot())
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
	expireTransientRecord(t, db, actor.ID, project, "ip_revisions", "payload", candidate, 2, 0)
	if provenance {
		attachRecoveryHistoryPin(t, db, project, candidate)
	}
	ip := propagationService(t, db)
	snapshot := originalSnapshot()
	snapshot.Entities[0].Description = "保留的正式人物背景"
	fresh, err := ip.Propose(ctx, actor, project, branchID, "", "request-v2:"+service.NewID(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.Adopt(ctx, actor, project, branchID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.Publish(ctx, actor, project, branchID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	input := service.ProjectRecoveryInput{Name: "恢复后可继续写作", Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()}
	if _, err = db.Exec(`CREATE TRIGGER expiry_recovery_fault BEFORE INSERT ON operations FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='late recovery failure'`); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.RecoverProject(ctx, actor.ID, project, input); err == nil {
		t.Fatal("late recovery failure was ignored")
	}
	var partial int
	if err = db.QueryRow("SELECT COUNT(*) FROM projects WHERE name=?", input.Name).Scan(&partial); err != nil || partial != 0 {
		t.Fatal("failed recovery left partial project", partial, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM retention_references r LEFT JOIN projects p ON p.id=r.project_id WHERE p.id IS NULL").Scan(&partial); err != nil || partial != 0 {
		t.Fatal("failed recovery left expiry references", partial, err)
	}
	if _, err = db.Exec("DROP TRIGGER expiry_recovery_fault"); err != nil {
		t.Fatal(err)
	}
	recovered, err := ip.RecoverProject(ctx, actor.ID, project, input)
	if err != nil {
		t.Fatal("normal expiry blocked whole novel recovery", err)
	}
	again, err := ip.RecoverProject(ctx, actor.ID, project, input)
	if err != nil || again.ID != recovered.ID {
		t.Fatal("recovery retry changed copy", err)
	}
	branches, err := ip.Branches(ctx, actor, recovered.ID)
	if err != nil || len(branches) != 1 {
		t.Fatal("branch history missing", err)
	}
	history, err := ip.Candidates(ctx, actor, recovered.ID, branches[0].ID)
	if err != nil || len(history) != 2 {
		t.Fatal("candidate history missing", err)
	}
	expired := ""
	for _, entry := range history {
		if entry.Retention != nil {
			expired = entry.ID
			if entry.ID == candidate || entry.Status != "PREPARED" {
				t.Fatal("erased candidate identity changed incorrectly", entry)
			}
			if _, err := ip.Adopt(ctx, actor, recovered.ID, branches[0].ID, entry.ID); !errors.Is(err, domain.ErrMaterialExpired) {
				t.Fatal("erased candidate became adoptable", err)
			}
		} else if entry.ID != branches[0].Head || entry.Status != "PUBLISHED" || entry.Snapshot.Entities[0].Description != "保留的正式人物背景" {
			t.Fatal("live world lost", entry)
		}
	}
	if expired == "" {
		t.Fatal("expired history was silently dropped")
	}
	if provenance {
		var proofs int
		if err = db.QueryRow(`SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=? AND JSON_EXTRACT(snapshot,'$.provenance_only')=TRUE`, recovered.ID).Scan(&proofs); err != nil || proofs == 0 {
			t.Fatal("historical source was not preserved", proofs, err)
		}
	}
	private, err := (&service.Knowledge{IP: ip}).PrivateStatus(ctx, actor, recovered.ID, branches[0].ID)
	if err != nil || len(private.Manifest.Sources) != 0 || len(private.Manifest.Omissions) != 1 || private.Manifest.Omissions[0].Ref.ID != expired || !strings.Contains(private.Manifest.Omissions[0].Reason, "保留期限") {
		t.Fatal("expired candidate blocked or contaminated private retrieval", private, err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "recovered-expired-world.zip")); err != nil {
		t.Fatal("recovered expiry archive cannot be verified", err)
	}
	if err = ip.Workspace.DeleteProject(ctx, actor.ID, recovered.ID, input.Name, 1, "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	second, err := ip.RecoverProject(ctx, actor.ID, recovered.ID, service.ProjectRecoveryInput{Name: "再次恢复的小说", Confirmation: input.Name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
	if err != nil {
		t.Fatal("second expiry recovery failed", err)
	}
	secondBranches, err := ip.Branches(ctx, actor, second.ID)
	if err != nil || len(secondBranches) != 1 {
		t.Fatal(err)
	}
	secondHistory, err := ip.Candidates(ctx, actor, second.ID, secondBranches[0].ID)
	if err != nil || len(secondHistory) != 2 {
		t.Fatal("second history lost", err)
	}
	// A marker without independent proof remains corruption, not normal expiry.
	var sourceID, sourceHash string
	owner, err := retention.RowOwnerKey("ip_revisions", map[string]string{"id": candidate})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT v.source_slot_id,v.source_payload_hash FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id AND r.revision=v.revision WHERE r.project_id=? AND r.owner_table='ip_revisions' AND r.owner_key=? AND r.field_key='payload'`, project, owner).Scan(&sourceID, &sourceHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE retention_source_values SET first_verified_at=DATE_ADD(first_verified_at,INTERVAL 1 DAY) WHERE source_id=? AND payload_hash=? AND is_null=FALSE", sourceID, sourceHash); err != nil {
		t.Fatal(err)
	}
	input.Name = "不能恢复的损坏来源"
	input.OperationID = "request-v2:" + service.NewID()
	if _, err = ip.RecoverProject(ctx, actor.ID, project, input); !errors.Is(err, service.ErrRecoveryIncomplete) {
		t.Fatal("changed source observation accepted", err)
	}
	if _, err = db.Exec("UPDATE retention_source_values SET first_verified_at=DATE_SUB(first_verified_at,INTERVAL 1 DAY) WHERE source_id=? AND payload_hash=? AND is_null=FALSE", sourceID, sourceHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	input.Name = "不能恢复的损坏副本"
	input.OperationID = "request-v2:" + service.NewID()
	if _, err = ip.RecoverProject(ctx, actor.ID, project, input); !errors.Is(err, service.ErrRecoveryIncomplete) {
		t.Fatal("unproven expiry accepted", err)
	}
}

// Add an exact historical source edge to the real expiry fixture. This is a
// controlled metadata fixture; the expiry, copy, backup and second recovery all
// execute production paths against MySQL, without recreating cleared content.
func attachRecoveryHistoryPin(t *testing.T, db *sql.DB, project, candidate string) {
	t.Helper()
	var raw, hash string
	if err := db.QueryRow(`SELECT v.snapshot,v.record_hash FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE v.project_id=? AND r.owner_table='branches' AND r.field_key='name' AND v.revision=1 LIMIT 1`, project).Scan(&raw, &hash); err != nil {
		t.Fatal(err)
	}
	source, err := retention.ValidateReferenceVersionRecord(raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := retention.RowOwnerKey("ip_revisions", map[string]string{"id": candidate})
	id, _ := retention.ReferenceID(project, "ip_revisions", owner, "payload")
	parentHash := ""
	for revision := 1; revision <= 2; revision++ {
		if err = db.QueryRow(`SELECT snapshot,record_hash FROM retention_reference_versions WHERE reference_id=? AND revision=?`, id, revision).Scan(&raw, &hash); err != nil {
			t.Fatal(err)
		}
		v, e := retention.ValidateReferenceVersionRecord(raw, hash)
		if e != nil {
			t.Fatal(e)
		}
		v.Reference.Sources = []retention.ReferenceSource{{ReferenceID: source.Reference.ID, Revision: source.Reference.Revision, PayloadHash: source.Reference.PayloadHash}}
		v.LineageState = "PARTIAL"
		if v.Expiration != nil {
			v.Expiration.PriorHash = parentHash
		}
		body, _ := json.Marshal(v)
		digest := sha256.Sum256(body)
		hash = hex.EncodeToString(digest[:])
		if _, e = retention.ValidateReferenceVersionRecord(string(body), hash); e != nil {
			t.Fatal(e)
		}
		if _, err = db.Exec(`UPDATE retention_reference_versions SET snapshot=?,record_hash=? WHERE reference_id=? AND revision=?`, body, hash, id, revision); err != nil {
			t.Fatal(err)
		}
		parentHash = hash
	}
}

func TestRecoveryKeepsExpiredChangePlanHistory(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	name := "保留过期修订计划"
	project := createProject(b, name, "expired-plan-recovery")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "candidate-for-plan", originalSnapshot())
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
	ip := propagationService(t, db)
	plan, err := ip.CreateChangePlan(ctx, actor, project, service.ChangePlanInput{BranchID: branchID, CandidateID: candidate, OperationID: "source-plan"})
	if err != nil {
		t.Fatal(err)
	}
	expireTransientRecord(t, db, actor.ID, project, "change_plans", "payload", plan.ID, 1, 0)
	if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	copy, err := ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: "已恢复修订历史", Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
	if err != nil {
		t.Fatal("expired plan blocked recovery", err)
	}
	branches, err := ip.Branches(ctx, actor, copy.ID)
	if err != nil || len(branches) != 1 {
		t.Fatal(err)
	}
	plans, err := ip.ChangePlans(ctx, actor, copy.ID, branches[0].ID)
	if err != nil || len(plans) != 1 || plans[0].Retention == nil || plans[0].ID == plan.ID {
		t.Fatal("plan history lost", plans, err)
	}
	if _, err = ip.ReadChangePlan(ctx, actor, copy.ID, plans[0].ID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired plan executable", err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-plan-recovery.zip")); err != nil {
		t.Fatal(err)
	}
}
