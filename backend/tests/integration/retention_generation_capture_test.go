package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func capturedVersion(t *testing.T, db *sql.DB, project, table, id, field string) retention.ReferenceVersion {
	t.Helper()
	owner, err := retention.RowOwnerKey(table, map[string]string{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table=? AND r.owner_key=? AND r.field_key=?`, project, table, owner, field).Scan(&raw); err != nil {
		t.Fatal("writer did not capture original source", table, field, err)
	}
	var version retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestRetentionGenerationSourceFailurePreservesBillingAndRecovers(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	_, svc, actor, project, original := generationFixture(t, transport)
	db, ctx := svc.IP.Workspace.DB, context.Background()
	installSourceRegistrationFixture(t, db)
	task, err := svc.Prepare(ctx, actor, project, service.GenerationInput{BranchID: original.BranchID, Role: "writer", PriceID: original.Frozen.Price.ID, OperationID: "capture-generation", Request: models.Request{Prompt: "保留原输入的独立创作任务", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	frozen := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen")
	if len(frozen.Reference.Sources) != 1 {
		t.Fatal("frozen generation lacks original request")
	}
	blobs := svc.IP.Blobs
	svc.IP.Blobs = missingCapturedBody{BlobStore: blobs, hash: task.Frozen.InputHash}
	if _, err := svc.Start(ctx, actor, project, task.ID); err == nil {
		t.Fatal("missing original request committed reservation")
	}
	svc.IP.Blobs = blobs
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_calls WHERE project_id=?", project).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed start left a call", count, err)
	}
	budget, err := (&service.Budgets{Workspace: svc.IP.Workspace}).Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 0 {
		t.Fatal("failed start held funds", budget, err)
	}
	started, err := svc.Start(ctx, actor, project, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_generation_result_source BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_LENGTH(JSON_EXTRACT(NEW.snapshot,'$.reference.sources'))>0 AND EXISTS(SELECT 1 FROM retention_references WHERE id=NEW.reference_id AND owner_table='model_calls' AND field_key='output_hash') THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected generation result source'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_generation_result_source") })
	if state, err := svc.Execute(ctx, task.ID); err == nil || state != "UNKNOWN" {
		t.Fatal("result became available without source proof", state, err)
	}
	var callState string
	var charged int64
	if err := db.QueryRow("SELECT state,charged_micros FROM model_calls WHERE id=?", started.CallID).Scan(&callState, &charged); err != nil || callState != "SUCCEEDED" || charged != 31 {
		t.Fatal("source failure lost confirmed fee", callState, charged, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_generation_result_source"); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Recover(ctx, task.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal("known receipt could not recover source capture", state, err)
	}
	output := capturedVersion(t, db, project, "model_calls", started.CallID, "output_hash")
	if len(output.Reference.Sources) != 1 || output.Reference.Sources[0].ReferenceID != frozen.Reference.ID || output.Reference.Sources[0].Revision != frozen.Reference.Revision {
		t.Fatal("result does not pin the prepared request")
	}
	if transport.calls.Load() != 1 {
		t.Fatal("recovering provenance resent provider request")
	}
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("generation lineage incomplete", report, err)
	}
}

func TestRetentionChangePlanAndGenerationCaptureOriginalGraph(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, project, original := generationFixture(t, transport)
	ctx, branch := context.Background(), original.BranchID
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": original.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	base := propose(b, project, branch, "", "capture-base", originalSnapshot())
	publish(b, project, branch, base)
	target := resource(b, project, branch, "旧人物形象所在章节")
	source := dependencyFromCatalog(t, b, project, "entity", "lin-1")
	release := prepareRelease(b, project, branch, base, "", "capture-dependent", []map[string]any{{"resource_id": target, "text": "林舟保持原先的外貌", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "人物外貌"}}}}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	snapshot := originalSnapshot()
	snapshot.Entities[0].Description = "人物外貌发生变化"
	candidate := propose(b, project, branch, base, "capture-candidate", snapshot)
	db := svc.IP.Workspace.DB
	installSourceRegistrationFixture(t, db)
	plan, err := svc.IP.CreateChangePlan(ctx, actor, project, service.ChangePlanInput{OperationID: "capture-impact-plan", BranchID: branch, CandidateID: candidate})
	if err != nil {
		t.Fatal(err)
	}
	proof := capturedVersion(t, db, project, "change_plans", plan.ID, "payload")
	if len(proof.Reference.Sources) < 2 {
		t.Fatal("change plan lacks original graph source pins")
	}
	task, err := svc.Prepare(ctx, actor, project, service.GenerationInput{BranchID: branch, ExpectedIPHead: base, Role: "continuity_editor", PriceID: original.Frozen.Price.ID, OperationID: "capture-impact-generation", ChangePlanID: plan.ID, Request: models.Request{Prompt: "核对人物变更影响", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	input := capturedVersion(t, db, project, "generation_tasks", task.ID, "frozen.input_hash")
	found := false
	for _, pin := range input.Reference.Sources {
		found = found || pin.ReferenceID == proof.Reference.ID && pin.Revision == proof.Reference.Revision
	}
	if !found {
		t.Fatal("generation did not pin original change plan")
	}
	if _, err := svc.IP.Adopt(ctx, actor, project, branch, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IP.Publish(ctx, actor, project, branch, candidate); err != nil {
		t.Fatal(err)
	}
	propagation, err := svc.IP.PropagatePublication(ctx, publicationEvent(t, db, candidate))
	if err != nil || propagation.State != "COMPLETED" {
		t.Fatal("automatic plan source capture", propagation, err)
	}
	if proof := capturedVersion(t, db, project, "change_plans", propagation.PlanID, "payload"); len(proof.Reference.Sources) < 2 {
		t.Fatal("automatic plan lacks original sources")
	}
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("historical generation/plan source chain incomplete", report, err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("preparing or capturing plans called provider")
	}
}
