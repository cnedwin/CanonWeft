package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionIndexJobReasonsRollbackAndPreserveTerminalState(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "索引任务记录", "index-job-sources")
	branchID := branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	jobs := &service.IndexJobs{Knowledge: &service.Knowledge{IP: &service.IP{Workspace: &service.Workspace{DB: db}}}}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	create := func(operation string) service.IndexJob {
		t.Helper()
		job, err := jobs.Create(ctx, actor, project, branchID, service.IndexJobInput{OperationID: operation})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	job := create("index-reason-cancel")
	original := capturedVersion(t, db, project, "index_jobs", job.ID, "reason")
	if _, err := jobs.Cancel(ctx, actor, project, branchID, job.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := capturedVersion(t, db, project, "index_jobs", job.ID, "reason")
	if cancelled.Reference.Revision != original.Reference.Revision+1 {
		t.Fatal("cancel reason not captured")
	}
	if state, err := jobs.Fail(ctx, job.ID); err != nil || state != "CANCELLED" {
		t.Fatal("late failure replaced cancellation", state, err)
	}
	after := capturedVersion(t, db, project, "index_jobs", job.ID, "reason")
	if after.Reference.Revision != cancelled.Reference.Revision || !after.Reference.FirstVerifiedAt.Equal(cancelled.Reference.FirstVerifiedAt) {
		t.Fatal("terminal replay renewed reason")
	}
	failed := create("index-reason-failure")
	if _, err := db.Exec("CREATE TRIGGER reject_index_job_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='index_jobs.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected index job reason'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_index_job_reason") })
	if _, err := jobs.Fail(ctx, failed.ID); err == nil {
		t.Fatal("failed metadata capture committed state")
	}
	var state, reason string
	if err := db.QueryRow("SELECT state,reason FROM index_jobs WHERE id=?", failed.ID).Scan(&state, &reason); err != nil || state != "QUEUED" || reason != "" {
		t.Fatal("partial failure state", state, reason, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_index_job_reason"); err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Fail(ctx, failed.ID); err != nil || state != "FAILED" {
		t.Fatal("failure metadata retry", state, err)
	}
	proof := capturedVersion(t, db, project, "index_jobs", failed.ID, "reason")
	if proof.Reference.Revision != 2 {
		t.Fatal("failure metadata not versioned", proof)
	}
}
