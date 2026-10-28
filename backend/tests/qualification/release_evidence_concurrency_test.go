package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

func waitReleaseLock(t *testing.T, ctx context.Context, f *fixture, table string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := f.admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM performance_schema.data_lock_waits w
 JOIN performance_schema.data_locks l ON l.ENGINE_LOCK_ID=w.REQUESTING_ENGINE_LOCK_ID AND l.ENGINE=w.ENGINE
 WHERE l.OBJECT_SCHEMA=? AND l.OBJECT_NAME=?`, f.config.DBName, table).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("did not observe requested lock barrier", table)
}

func releaseActorInterleaving(t *testing.T, ctx context.Context, f *fixture, job service.Generation, release domain.Release, busyActor domain.Actor) {
	t.Helper()
	gate, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	var id string
	if err = gate.QueryRowContext(ctx, "SELECT id FROM generation_tasks WHERE id=? FOR UPDATE", job.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	adopted := make(chan error, 1)
	go func() { _, e := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); adopted <- e }()
	waitReleaseLock(t, ctx, f, "generation_tasks")
	read := make(chan error, 1)
	go func() {
		_, e := f.svc.IP.ReadResource(ctx, busyActor, f.project, release.Manifest.Artifacts[0].ResourceID)
		read <- e
	}()
	waitReleaseLock(t, ctx, f, "projects")
	if err = gate.Commit(); err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]<-chan error{"owner adoption": adopted, "editor read": read} {
		select {
		case e := <-result:
			if e != nil {
				t.Errorf("%s failed under ordinary cross-author concurrency: %v", name, e)
			}
		case <-ctx.Done():
			t.Fatal("cross-author operations did not complete", ctx.Err())
		}
	}
}

// The gate deliberately creates the production interleaving: owner has the
// project row, source editor has their user row and waits for the project.
// No production hook, mocked database, or timing-only probabilistic stress.
func TestReleaseCrossAuthorAdoptionDoesNotDeadlockAndRechecksPermission(t *testing.T) {
	f, first, binding := releaseGenerationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	editor, err := f.svc.IP.Workspace.CreateMember(ctx, f.actor.ID, "source-editor", "source-editor-password-123", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, f.actor.ID, f.project, editor.ID, "editor", false); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, editor, f.project, service.GenerationInput{BindingID: binding.ID, TaskKind: binding.TaskKind, BranchID: f.branch, Role: "researcher", PriceID: first.Frozen.Price.ID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"协作作者的资料"}`), Request: models.Request{Prompt: "检查资料", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, editor, f.project, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = f.svc.Generations.Read(ctx, editor, f.project, job.ID)
	if err != nil || job.Output == nil {
		t.Fatal("editor generation missing", err)
	}
	release := prepareGenerationRelease(t, f, job, job.Output.Text)

	releaseActorInterleaving(t, ctx, f, job, release, editor)
	if t.Failed() {
		return
	}
	// The concurrency repair must not remove current author authority checks.
	if err = f.svc.IP.Workspace.SetMember(ctx, f.actor.ID, f.project, editor.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("removed source editor still authorized", err)
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, f.actor.ID, f.project, editor.ID, "editor", false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, "UPDATE users SET enabled=FALSE WHERE id=?", editor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("disabled source editor still authorized", err)
	}
}

func TestReleaseQualificationAuthorConcurrencyUsesCurrentAuthority(t *testing.T) {
	f, _, input := modelFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	qualifier, err := f.svc.IP.Workspace.CreateMember(ctx, f.actor.ID, "method-reviewer", "method-reviewer-password-123", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, f.actor.ID, f.project, qualifier.ID, "editor", false); err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.Create(ctx, qualifier, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	qualified := drive(t, f, run.ID)
	if qualified.State != "PASSED" {
		t.Fatal("qualification did not pass", qualified.State)
	}
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: qualified.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	sourceAuthor, err := f.svc.IP.Workspace.CreateMember(ctx, f.actor.ID, "source-author", "source-author-password-123", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, f.actor.ID, f.project, sourceAuthor.ID, "editor", false); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, sourceAuthor, f.project, service.GenerationInput{BindingID: binding.ID, TaskKind: binding.TaskKind, BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"第三位参与者的固定资格"}`), Request: models.Request{Prompt: "检查资料", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, sourceAuthor, f.project, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = f.svc.Generations.Read(ctx, sourceAuthor, f.project, job.ID)
	if err != nil || job.Output == nil {
		t.Fatal("source missing", err)
	}
	release := prepareGenerationRelease(t, f, job, job.Output.Text)
	releaseActorInterleaving(t, ctx, f, job, release, qualifier)
	if t.Failed() {
		return
	}
	if _, err = f.db.ExecContext(ctx, "UPDATE users SET enabled=FALSE WHERE id=?", qualifier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("disabled qualification author remained authorized", err)
	}
}
