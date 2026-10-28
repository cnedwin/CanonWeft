package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionInventoryScansAllColumnsAndSeparatesProjects(t *testing.T) {
	b, db := setup(t)
	projects := []string{createProject(b, "库存甲", "inventory-a"), createProject(b, "库存乙", "inventory-b"), createProject(b, "库存丙", "inventory-c")}
	branches := map[string]string{}
	for _, project := range projects {
		branches[branch(b, project, "正史")] = project
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := retention.ScanSources(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tables) != len(retention.KnownTables()) || snapshot.Hash == "" || snapshot.CapturedAt.IsZero() {
		t.Fatal("partial scan advertised")
	}
	again, err := retention.ScanSources(ctx, tx)
	if err != nil || again.Hash != snapshot.Hash {
		t.Fatal("stable snapshot digest drifted", err)
	}
	found := map[string]int{}
	for _, row := range snapshot.Index.Records() {
		owner, err := snapshot.Index.Owner(row.Table, row.Values)
		if err != nil {
			t.Fatal(row.Table, err)
		}
		if row.Table == "branches" && owner.ProjectID != branches[row.Values["id"]] {
			t.Fatal("wrong branch owner")
		}
		if owner.ProjectID != "" {
			found[owner.ProjectID]++
		} else if !owner.Shared {
			t.Fatal("ownerless record accepted")
		}
	}
	for _, project := range projects {
		if found[project] < 4 {
			t.Fatal("source rows or operations missing", found)
		}
	}
}

func TestRetentionInventoryChecksActualForkAndSemanticTargetPointers(t *testing.T) {
	b, db := setup(t)
	local := createProject(b, "来源归属甲", "pointer-local")
	foreign := createProject(b, "来源归属乙", "pointer-foreign")
	localBranch, foreignBranch := branch(b, local, "正史"), branch(b, foreign, "正史")
	localRevision := propose(b, local, localBranch, "", "pointer-local-candidate", originalSnapshot())
	foreignRevision := propose(b, foreign, foreignBranch, "", "pointer-foreign-candidate", originalSnapshot())
	publish(b, local, localBranch, localRevision)
	publish(b, foreign, foreignBranch, foreignRevision)
	child := service.NewID()
	if _, err := db.Exec("INSERT INTO branches(id,project_id,name,parent_id,base_revision,head_revision) VALUES(?,?,'继承分支',?,?,?)", child, local, localBranch, localRevision, localRevision); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if _, err := db.Exec("UPDATE branches SET head_revision=? WHERE id=?", foreignRevision, child); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 503)
	if _, err := db.Exec("UPDATE branches SET head_revision=? WHERE id=?", localRevision, child); err != nil {
		t.Fatal(err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	review := service.NewID()
	if _, err := db.Exec("INSERT INTO semantic_review_runs(id,project_id,branch_id,actor_id,operation_id,request_hash,target_kind,target_id,target_hash,frozen) VALUES(?,?,?,?,'pointer-review',?,'ip_candidate',?,?,?)", review, local, localBranch, actor, strings.Repeat("a", 64), localRevision, strings.Repeat("b", 64), `{}`); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if _, err := db.Exec("UPDATE semantic_review_runs SET target_id=? WHERE id=?", foreignRevision, review); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 503)
}

func TestRetentionInventoryProtectsActualAdoptedPublicationHistory(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "内容历史保护", "content-roots-project")
	branchID := branch(b, project, "正史")
	ip := propose(b, project, branchID, "", "content-roots-ip", originalSnapshot())
	publish(b, project, branchID, ip)
	resourceID := resource(b, project, branchID, "已采用资料")
	first := prepareRelease(b, project, branchID, ip, "", "content-roots-first", []map[string]any{{"resource_id": resourceID, "expected_head": "", "text": "必须保留的旧正文"}})
	releaseAdopt(b, project, first)
	releasePublish(b, project, first)
	second := prepareRelease(b, project, branchID, ip, "", "content-roots-second", []map[string]any{{"resource_id": resourceID, "expected_head": first, "text": "用户确认的新正文"}})
	releaseAdopt(b, project, second)
	// The pending adopted revision is a preservation root before publication;
	// the first publication remains a root after its resource head is replaced.
	before := b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if before["adopted_content_references"] != float64(9) || before["adopted_content_objects"] != float64(5) {
		t.Fatal("adopted revision not protected", before)
	}
	releasePublish(b, project, second)
	after := b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if after["adopted_content_references"] != before["adopted_content_references"] || after["adopted_content_objects"] != before["adopted_content_objects"] {
		t.Fatal("publishing dropped historical roots", after)
	}
	parent := prepareRelease(b, project, branchID, ip, "", "content-roots-parent", []map[string]any{{"resource_id": resourceID, "expected_head": second, "text": "尚未单独采用但被后续改稿引用的候选"}})
	derived := prepareRelease(b, project, branchID, ip, "", "content-roots-derived", []map[string]any{{"resource_id": resourceID, "expected_head": second, "text": "用户确认的派生改稿", "origin": map[string]any{"parent_release_id": parent}}})
	releaseAdopt(b, project, derived)
	withParent := b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if withParent["adopted_content_references"] != float64(15) || withParent["adopted_content_objects"] != float64(9) {
		t.Fatal("unpublished source ancestor lost", withParent)
	}
	// This is source corruption in a disposable database, not a public edit.
	if _, err := db.Exec("UPDATE resources SET content_hash=? WHERE id=?", strings.Repeat("f", 64), resourceID); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 503)
}

func TestRetentionInventoryHTTPIsOwnerOnlyAndContainsNoSourceBytes(t *testing.T) {
	owner, db := setup(t)
	project := createProject(owner, "PRIVATE_INVENTORY_CANARY", "inventory-preview")
	branch(owner, project, "正史")
	owner.request("POST", "/api/members", map[string]any{"username": "inventory-editor", "password": "editor-password-123", "role": "editor"}, 201)
	anonymous := &browser{t: t, handler: owner.handler}
	anonymous.request("GET", "/api/operations/retention-inventory", nil, 401)
	editor := &browser{t: t, handler: owner.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "inventory-editor", "password": "editor-password-123"}, 200)
	editor.request("GET", "/api/operations/retention-inventory", nil, 403)
	var before int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&before); err != nil {
		t.Fatal(err)
	}
	report := owner.request("GET", "/api/operations/retention-inventory", nil, 200)
	if report["state"] != "INVENTORIED" || report["deletion_ready"] != false || report["project_count"] != float64(1) || report["project_records"].(float64) < 4 || report["shared_records"].(float64) < 1 {
		t.Fatal("incomplete or misleading inventory", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_INVENTORY_CANARY", project, "password_hash", "token_hash", "inventory-editor"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("source bytes escaped")
		}
	}
	var after int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&after); err != nil || after != before {
		t.Fatal("read-only inventory changed database", err)
	}
	if _, err := db.Exec("UPDATE operations SET kind='UNRECOGNIZED_FUTURE_KIND'"); err != nil {
		t.Fatal(err)
	}
	owner.request("GET", "/api/operations/retention-inventory", nil, 503)
}

func TestRetentionInventoryRejectsNewUnclassifiedSQLColumn(t *testing.T) {
	_, db := setup(t)
	if _, err := db.Exec("ALTER TABLE projects ADD COLUMN future_private TEXT NULL"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ALTER TABLE projects DROP COLUMN future_private")
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = retention.ScanSources(context.Background(), tx); err == nil {
		t.Fatal("unknown actual source column accepted")
	}
}

func TestRetentionInventoryIncludesRealRecipeStartEvent(t *testing.T) {
	b, recipes, actor, project, input := recipeFixture(t, &generationTransport{})
	run, err := recipes.Prepare(context.Background(), actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Start(context.Background(), actor, project, run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 200)
}
