package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionPinnedSourcesActualPrivateIndexAndHistoricalProof(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "检索副本来源", "retention-rag-pins")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "草稿资料")
	body := strings.Repeat("她沿着旧地图寻找药材，记录每一次相遇。", 40)
	b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "base_head": "", "text": body}, 200)
	job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "retention-index-build", "kind": "working"}, 202)
	if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	first, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || first.PinnedReferences < 3 || first.UnboundSources != 0 || first.DeletionReady {
		t.Fatal("actual index copies not pinned", first, err)
	}
	var refID, raw string
	if err := db.QueryRow("SELECT r.id,v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_chunks' AND r.field_key='content' ORDER BY r.id LIMIT 1", project).Scan(&refID, &raw); err != nil {
		t.Fatal(err)
	}
	var initial retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &initial); err != nil || initial.LineageState != "PARTIAL" || len(initial.Reference.Sources) != 1 {
		t.Fatal("direct source relation absent", initial, err)
	}
	pin := initial.Reference.Sources[0]
	var parentTable, parentField string
	if err := db.QueryRow("SELECT owner_table,field_key FROM retention_references WHERE id=?", pin.ReferenceID).Scan(&parentTable, &parentField); err != nil || parentTable != "working_indexes" || parentField != "manifest" || pin.Revision != 1 {
		t.Fatal("source bound to wrong row", parentTable, parentField, pin, err)
	}
	if repeated, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || repeated.NewReferences != 0 || repeated.UpdatedReferences != 0 {
		t.Fatal("repeated collection changed pins", repeated, err)
	}
	if err := w.DeleteProject(ctx, actor, project, "检索副本来源", 1, "retention-rag-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", refID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var after retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &after); err != nil || after.Reference.Revision != 2 || len(after.Reference.Sources) != 1 || after.Reference.Sources[0] != pin || !after.Reference.FirstVerifiedAt.Equal(initial.Reference.FirstVerifiedAt) {
		t.Fatal("lifecycle change rebound old copy", after, err)
	}
	var original string
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=?", pin.ReferenceID, pin.Revision).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_reference_versions SET snapshot='{}' WHERE reference_id=? AND revision=?", pin.ReferenceID, pin.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("corrupt historical source ignored", err)
	}
	if _, err := db.Exec("UPDATE retention_reference_versions SET snapshot=? WHERE reference_id=? AND revision=?", original, pin.ReferenceID, pin.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal("valid historical proof could not resume", err)
	}
	if _, err := db.Exec("DELETE FROM retention_reference_versions WHERE reference_id=? AND revision=?", pin.ReferenceID, pin.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("missing historical source rebound to newest", err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM retention_references WHERE id=?", refID).Scan(&revision); err != nil || revision != 2 {
		t.Fatal("failed proof rewrote child", revision, err)
	}
}

func TestRetentionPinnedSourcesLateCancelledOutputRemainsUnbound(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "迟到输出来源", "retention-late-output")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	w, ctx := &service.Workspace{DB: db}, context.Background()
	budgets := &service.Budgets{Workspace: w}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	call, err := budgets.Reserve(ctx, actor, project, "late-output-call", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Cancel(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, call.ID, "SUCCEEDED", 37, "PRIVATE_RECEIPT", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	first, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || first.UnboundSources != 1 || first.DeletionReady {
		t.Fatal("missing original source falsely bound", first, err)
	}
	var state string
	var revision int64
	if err := db.QueryRow("SELECT state,inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&state, &revision); err != nil || state != "BLOCKED" {
		t.Fatal(state, err)
	}
	again, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || again.UnboundSources != 1 || again.NewReferences != 0 || again.UpdatedReferences != 0 {
		t.Fatal("unbound retry fabricated source", again, err)
	}
	var after int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&after); err != nil || after != revision {
		t.Fatal("blocked retry churned inventory", after, revision, err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Spent != 37 || budget.Reserved != 0 {
		t.Fatal("lineage registration changed accounting", budget, err)
	}
}
