package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"os"
	"testing"
	"time"
)

func TestActualIsolatedToolEvaluationNormalNegativeAndQualifiedUse(t *testing.T) {
	root := os.Getenv("STUDIO_ISOLATION_ROOT")
	if root == "" {
		t.Skip("real isolated runtime required")
	}
	f := isolated(t)
	f.svc.RuntimeRoot = root
	f.svc.ToolTrials = &service.ToolTrials{IP: f.svc.IP, RuntimeRoot: root}
	ctx := context.Background()
	presets, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	input := service.QualificationInput{OperationID: service.NewID(), VersionID: presets.BaselineID, BaselineID: presets.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", MaxOutputTokens: 512}
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.ToolTrials.Execute(ctx, prepared.Steps[0].ChildID); err != nil || state != "PAUSED" {
		t.Fatal("paused tool evaluated", state, err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	result := drive(t, f, r.ID)
	if result.State != "REVIEW_REQUIRED" || len(result.Steps) != 18 {
		t.Fatal("actual isolation comparison failed", result.State, result.Reason)
	}
	for _, step := range result.Steps {
		if step.ChildID == "" || step.Metrics.OutputHash == "" || step.Metrics.DurationMS <= 0 {
			t.Fatal("missing real isolation receipt", step)
		}
	}
	if _, err = f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: result.QualificationID, TaskKind: "shared.contract"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("unapproved executable activated", err)
	}
	approved := approveWithRetainedSources(t, f, result)
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: approved.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	toolInput := service.ToolTrialInput{OperationID: service.NewID(), BindingID: binding.ID, TaskKind: "shared.contract", InputText: `{"source":"qualified actual tool"}`}
	job, err := f.svc.ToolTrials.Create(ctx, f.actor, f.project, toolInput)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: approved.QualificationID, TaskKind: "shared.contract", ExpectedBinding: binding.ID, Rollback: true})
	if err != nil {
		t.Fatal(err)
	}
	if replay, e := f.svc.ToolTrials.Create(ctx, f.actor, f.project, toolInput); e != nil || replay.ID != job.ID {
		t.Fatal("existing tool operation replay lost its pin", e)
	}
	if _, err = f.svc.ToolTrials.Create(ctx, f.actor, f.project, service.ToolTrialInput{OperationID: service.NewID(), BindingID: binding.ID, TaskKind: "shared.contract", InputText: `{"source":"new obsolete binding must fail"}`}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("new tool operation bypassed current head", err)
	}
	blobs := f.svc.IP.Blobs
	switchCtx, cancelSwitch := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSwitch()
	switched := make(chan error, 1)
	f.svc.IP.Blobs = &beforeBlobCommit{BlobStore: blobs, before: func() {
		go func() {
			_, e := f.svc.Activate(switchCtx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: approved.QualificationID, TaskKind: "shared.contract", ExpectedBinding: b2.ID, Rollback: true})
			switched <- e
		}()
		// Admission locks the actor before the project. The same actor's real
		// concurrent switch must wait at that first barrier until commit.
		waitReleaseLock(t, switchCtx, f, "users")
	}}
	pinned, e := f.svc.ToolTrials.Create(ctx, f.actor, f.project, service.ToolTrialInput{OperationID: service.NewID(), BindingID: b2.ID, TaskKind: "shared.contract", InputText: `{"source":"switch while preparing tool"}`})
	if e != nil || pinned.Frozen.BindingID != b2.ID {
		t.Fatal("serialized tool creation lost its binding", e)
	}
	select {
	case e := <-switched:
		if e != nil {
			t.Fatal("queued binding switch", e)
		}
	case <-switchCtx.Done():
		t.Fatal("binding switch did not finish after commit", switchCtx.Err())
	}
	f.svc.IP.Blobs = blobs
	if _, e := f.svc.ToolTrials.Create(ctx, f.actor, f.project, service.ToolTrialInput{OperationID: service.NewID(), BindingID: b2.ID, TaskKind: "shared.contract", InputText: `{"source":"old binding after switch"}`}); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("new tool operation ignored completed switch", e)
	}
	if state, err := f.svc.ToolTrials.Execute(ctx, job.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal("qualified tool actual execution", state, err)
	}
	if err = f.svc.RevokeVersion(ctx, f.actor, f.project, approved.QualificationID, "Revoke entire executable capability version"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ToolTrials.Read(ctx, f.actor, f.project, job.ID); err != nil {
		t.Fatal(err)
	}
	// Source reconstruction preserves completed three-arm receipts and the
	// original qualified binding even after revocation; it never executes them.
	w := f.svc.IP.Workspace
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 {
		tx, beginErr := f.db.BeginTx(ctx, nil)
		if beginErr == nil {
			_, detail := retention.RegisterSourceObservations(ctx, tx, f.ledger, f.project, f.svc.IP.Blobs.ReadObject)
			tx.Rollback()
			t.Log("internal source diagnostic", detail)
		}
		t.Fatal("tool qualification provenance incomplete", report, err)
	}
	step := result.Steps[0]
	var original string
	if err := f.db.QueryRow("SELECT metrics FROM qualification_steps WHERE id=?", step.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var changed service.EvaluationMetrics
	if json.Unmarshal([]byte(original), &changed) != nil {
		t.Fatal("metrics unreadable")
	}
	changed.Output = "substituted tool output"
	raw, _ := json.Marshal(changed)
	if _, err := f.db.Exec("UPDATE qualification_steps SET metrics=? WHERE id=?", raw, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("tool metric substitution accepted", err)
	}
	if _, err := f.db.Exec("UPDATE qualification_steps SET metrics=? WHERE id=?", original, step.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 || report.UpdatedReferences != 0 {
		t.Fatal("tool qualification replay changed source history", report, err)
	}
}
