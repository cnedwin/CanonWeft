package backup_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestSemanticReviewRestoreRetainsHistoryButDisablesAdoption(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	branch, candidate, completed, pending, receipt := service.NewID(), service.NewID(), service.NewID(), service.NewID(), service.NewID()
	input := f.put(t, "审查当时使用的完整固定输入")
	report := f.put(t, "历史独立审查报告")
	payload := `{"schema":1,"changes":[]}`
	digest := sha256.Sum256([]byte(payload))
	deltaHash := hex.EncodeToString(digest[:])
	targetHash := f.put(t, `{"schema":1,"entities":[]}`)
	frozen, _ := json.Marshal(domain.SemanticReviewFrozen{Schema: 1, InputHash: input, Scope: "fixed IP candidate"})
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	execute(t, f.db, "INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,status,created_by) VALUES(?,?,'',?,?,'PREPARED',?)", candidate, branch, `{"schema":1,"entities":[]}`, targetHash, f.user)
	execute(t, f.db, "INSERT INTO ip_branch_deltas(candidate_id,project_id,branch_id,base_revision,parent_revision,delta_hash,payload,created_by) VALUES(?,?,?,'','',?,?,?)", candidate, f.project, branch, deltaHash, payload, f.user)
	for _, entry := range []struct{ id, state, verdict, report string }{{completed, "COMPLETED", "PASS", report}, {pending, "RUNNING", "PENDING", ""}} {
		execute(t, f.db, "INSERT INTO semantic_review_runs(id,project_id,branch_id,actor_id,operation_id,request_hash,target_kind,target_id,target_hash,frozen,state,verdict,report_hash) VALUES(?,?,?,?,?,?,'ip_candidate',?,?,?,?,?,?)", entry.id, f.project, branch, f.user, entry.id, targetHash, candidate, targetHash, string(frozen), entry.state, entry.verdict, entry.report)
	}
	execute(t, f.db, "INSERT INTO semantic_review_acceptances(id,project_id,target_kind,target_id,target_hash,revision,review_id,report_hash,actor_id) VALUES(?,?,'ip_candidate',?,?,1,?,?,?)", receipt, f.project, candidate, targetHash, completed, report, f.user)
	path := filepath.Join(f.root, "semantic-review.zip")
	if _, err := backup.Create(ctx, f.db, f.blobs, path); err != nil {
		t.Fatal(err)
	}
	opt := f.options(t, path, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err := backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	db := openTarget(t, opt)
	var disabled, accepted, cancelled int
	if err := db.QueryRow("SELECT COUNT(*) FROM semantic_review_runs WHERE restored_disabled=TRUE AND cancel_requested=TRUE AND paused=FALSE").Scan(&disabled); err != nil || disabled != 2 {
		t.Fatal("restored review may authorize adoption", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM semantic_review_runs WHERE id=? AND state='CANCELLED'", pending).Scan(&cancelled); err != nil || cancelled != 1 {
		t.Fatal("pending review may resume", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM semantic_review_acceptances a JOIN semantic_review_runs r ON r.id=a.review_id WHERE r.state='COMPLETED' AND r.verdict='PASS' AND a.report_hash=r.report_hash").Scan(&accepted); err != nil || accepted != 1 {
		t.Fatal("historical receipt lost", err)
	}
	for _, hash := range []string{input, report, targetHash} {
		if _, err := (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, hash); err != nil {
			t.Fatal("review evidence missing", err)
		}
	}
}
