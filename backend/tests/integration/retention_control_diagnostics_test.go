package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionRecipeCancellationCapturesReasonWithoutInputRead(t *testing.T) {
	_, svc, actor, project, input := recipeFixture(t, &generationTransport{})
	ctx, db := context.Background(), svc.Generations.IP.Workspace.DB
	run, err := svc.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	svc.Generations.IP.Blobs = nil
	if _, err := svc.Control(ctx, actor, project, run.ID, "cancel", run.Revision); err != nil {
		t.Fatal(err)
	}
	if capturedVersion(t, db, project, "recipe_runs", run.ID, "reason").Reference.Revision != 2 {
		t.Fatal("cancellation reason history missing")
	}
}

func TestRetentionToolCancellationAndFailureCaptureReasonsWithoutExecution(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "工具控制记录", "tool-control-capture")
	tool := createTrialTool(b, project)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	root := "/api/projects/" + project + "/tool-trials"
	first := b.request("POST", root, map[string]any{"operation_id": "cancel", "version_id": tool, "input_text": `{"source":"不会执行"}`}, 202)["id"].(string)
	second := b.request("POST", root, map[string]any{"operation_id": "fail", "version_id": tool, "input_text": `{"source":"不会执行"}`}, 202)["id"].(string)
	installSourceRegistrationFixture(t, db)
	svc := &service.ToolTrials{IP: &service.IP{Workspace: &service.Workspace{DB: db}}, RuntimeRoot: "must-not-launch"}
	ctx := context.Background()
	if _, err := svc.Cancel(ctx, actor, project, first); err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project, "tool_trials", first, "reason")
	if _, err := db.Exec("CREATE TRIGGER reject_tool_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='tool_trials.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected tool diagnostic'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_tool_reason") })
	if _, err := svc.Fail(ctx, second); err == nil {
		t.Fatal("failure committed without diagnostic")
	}
	var state string
	if err := db.QueryRow("SELECT state FROM tool_trials WHERE id=?", second).Scan(&state); err != nil || state != "QUEUED" {
		t.Fatal("partial tool failure", state, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_tool_reason"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Fail(ctx, second); err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project, "tool_trials", second, "reason")
	if state, err := svc.Fail(ctx, first); err != nil || state != "CANCELLED" {
		t.Fatal("late failure replaced terminal cancellation", state, err)
	}
}
