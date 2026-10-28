package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func expireFrozenFixture(t *testing.T, db *sql.DB, project, table, id string) string {
	t.Helper()
	allowed := map[string]bool{"qualification_runs": true, "tool_trials": true, "recipe_runs": true, "semantic_review_runs": true}
	if !allowed[table] {
		t.Fatal("unsupported fixture table")
	}
	ddl, err := os.ReadFile("../../migrations/retention.sql.pending")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(ddl), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	var raw []byte
	if err = db.QueryRow("SELECT frozen FROM "+table+" WHERE id=? AND project_id=?", id, project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	key, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
	ref, _ := retention.ReferenceID(project, table, key, "frozen")
	at := time.Now().UTC().Truncate(time.Microsecond)
	projection, err := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: table + ".frozen", PayloadHash: hash(raw), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{}, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO retention_references(id,project_id,owner_table,owner_key,field_key,class,storage_kind,payload_hash,source_created_at,time_basis,first_verified_at,deadline,state,expired_at) VALUES(?,?,?,?,'frozen','RAW_RUN','INLINE',?,?,'SOURCE_UTC',?,?,'EXPIRED',?)", ref, project, table, key, hash(raw), at.Add(-30*24*time.Hour), at, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO retention_evidence(id,project_id,reference_id,reference_revision,original_payload_hash,projection_hash,projection,expired_at) VALUES(?,?,?,1,?,?,?,?)", hash([]byte(ref)), project, ref, hash(raw), hash(proof), proof, at); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE "+table+" SET frozen=? WHERE id=? AND project_id=?", proof, id, project); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestRetentionRuntimeOtherTaskListsUseBoundProofs(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "过期工作记录", "history-project")
	branchID := branch(b, project, "正史")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	tool := createTrialTool(b, project)
	job := b.request("POST", "/api/projects/"+project+"/tool-trials", map[string]any{"operation_id": "expiry-trial", "version_id": tool, "input_text": `{"source":"不应出现在历史中的私人文本"}`}, 202)
	toolID := job["id"].(string)
	expireFrozenFixture(t, db, project, "tool_trials", toolID)
	tools := &service.ToolTrials{IP: ip}
	list, _, err := tools.List(ctx, actor, project, "")
	if err != nil || len(list) != 1 || list[0].Retention == nil || list[0].CanCancel || list[0].Frozen.VersionID != "" {
		t.Fatal("tool history not reduced", err)
	}
	if _, err = tools.Cancel(ctx, actor, project, toolID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("tool expired material became executable", err)
	}
	// These two rows model historical SQL records only. They do not claim that
	// a real semantic model or qualification benchmark has run or passed.
	raw := `{"private_text":"历史私有输入"}`
	qualificationID := service.NewID()
	if _, err = db.Exec("INSERT INTO qualification_runs(id,project_id,branch_id,actor_id,frozen,state) VALUES(?,?,?,?,?,'FAILED')", qualificationID, project, branchID, actor.ID, raw); err != nil {
		t.Fatal(err)
	}
	expireFrozenFixture(t, db, project, "qualification_runs", qualificationID)
	qualifications := &service.Qualifications{IP: ip}
	runs, err := qualifications.List(ctx, actor, project)
	if err != nil || len(runs) != 1 || runs[0].Retention == nil || runs[0].QualificationID != "" || len(runs[0].Steps) != 0 {
		t.Fatal("expired evaluation became qualification or hid history", err)
	}
	if _, err = qualifications.Control(ctx, actor, project, qualificationID, "resume"); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired evaluation resumed", err)
	}
	reviewID, targetID := service.NewID(), service.NewID()
	if _, err = db.Exec("INSERT INTO semantic_review_runs(id,project_id,branch_id,actor_id,operation_id,request_hash,target_kind,target_id,target_hash,frozen,state,verdict) VALUES(?,?,?,?,'historical-review',?,'ip_candidate',?,?,?,'COMPLETED','FAIL')", reviewID, project, branchID, actor.ID, strings.Repeat("a", 64), targetID, strings.Repeat("b", 64), raw); err != nil {
		t.Fatal(err)
	}
	ref := expireFrozenFixture(t, db, project, "semantic_review_runs", reviewID)
	reviews := &service.SemanticReviews{Generations: &service.Generations{IP: ip}}
	reviewList, err := reviews.List(ctx, actor, project, branchID, "ip_candidate", targetID)
	if err != nil || len(reviewList) != 1 || reviewList[0].Retention == nil || reviewList[0].Selected || reviewList[0].SelectionReady || reviewList[0].OperationID != "" {
		t.Fatal("expired semantic history leaked or authorized", err)
	}
	if _, err = db.Exec("UPDATE retention_evidence SET project_id=? WHERE reference_id=?", service.NewID(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err = reviews.List(ctx, actor, project, branchID, "ip_candidate", targetID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("foreign evidence accepted", err)
	}
}

func TestRetentionRuntimeRecipeHistoryChecksOriginalHash(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	db := recipes.Generations.IP.Workspace.DB
	expireFrozenFixture(t, db, project, "recipe_runs", run.ID)
	items, err := recipes.List(ctx, actor, project, input.BranchID)
	if err != nil || len(items) != 1 || items[0].Retention == nil || len(items[0].Steps) != 0 || items[0].HeldMicros != 0 || items[0].SpentMicros != 0 {
		t.Fatal("recipe history not reduced or financial facts changed", err)
	}
	if _, err = recipes.Read(ctx, actor, project, run.ID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("recipe expired inputs readable", err)
	}
	if _, err = db.Exec("UPDATE recipe_runs SET frozen_hash=? WHERE id=?", strings.Repeat("0", 64), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.List(ctx, actor, project, input.BranchID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("mismatched source hash accepted", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("history read called a model")
	}
}
