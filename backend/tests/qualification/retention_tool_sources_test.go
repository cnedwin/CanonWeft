package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionToolControlPinsCaseBeforeExecution(t *testing.T) {
	runtime := os.Getenv("STUDIO_ISOLATION_ROOT")
	if runtime == "" {
		t.Skip("approved runtime required to prepare tool evaluation")
	}
	f := isolated(t)
	f.svc.RuntimeRoot = runtime
	f.svc.ToolTrials = &service.ToolTrials{IP: f.svc.IP, RuntimeRoot: runtime}
	ctx := context.Background()
	presets, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	installQualificationSourceFixture(t, f)
	run, err := f.svc.Create(ctx, f.actor, f.project, service.QualificationInput{OperationID: service.NewID(), VersionID: presets.BaselineID, BaselineID: presets.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = f.svc.Read(ctx, f.actor, f.project, run.ID)
	if err != nil || len(run.Steps) == 0 || run.Steps[0].Arm != "no_method" || run.Steps[0].ChildID == "" {
		t.Fatal("control was not prepared", err)
	}
	assertSemanticCaptured(t, f, "tool_trials", run.Steps[0].ChildID, "frozen.input_hash", 2)
	w := f.svc.IP.Workspace
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 {
		t.Fatal("control provenance incomplete", report, err)
	}
	var original string
	if err := f.db.QueryRow("SELECT frozen FROM qualification_runs WHERE id=?", run.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"control_hash", "case_input", "runtime"} {
		var frozen service.QualificationFrozen
		if json.Unmarshal([]byte(original), &frozen) != nil {
			t.Fatal("frozen evaluation unreadable")
		}
		switch field {
		case "control_hash":
			frozen.NoMethod.Hash = strings.Repeat("a", 64)
		case "case_input":
			for i := range frozen.Suite.Cases {
				if frozen.Suite.Cases[i].ID == run.Steps[0].CaseID {
					frozen.Suite.Cases[i].Input = json.RawMessage(`{"source":"substituted input"}`)
				}
			}
		case "runtime":
			frozen.RuntimeHash = strings.Repeat("b", 64)
		}
		raw, _ := json.Marshal(frozen)
		if _, err := f.db.Exec("UPDATE qualification_runs SET frozen=? WHERE id=?", raw, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("substituted control accepted", field, err)
		}
	}
	if _, err := f.db.Exec("UPDATE qualification_runs SET frozen=? WHERE id=?", original, run.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 || report.UpdatedReferences != 0 {
		t.Fatal("prepared control replay changed history", report, err)
	}
	var state, output string
	if err := f.db.QueryRow("SELECT state,COALESCE(output_hash,'') FROM tool_trials WHERE id=?", run.Steps[0].ChildID).Scan(&state, &output); err != nil || state != "QUEUED" || output != "" {
		t.Fatal("source registration executed control", state, output, err)
	}
}
