package qualification_test

import (
	"context"
	"novelstudio/internal/service"
	"testing"
)

func TestBaselinePresetsAreProjectAndKindScoped(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	input := service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "agent", Role: "researcher"}
	first, err := f.svc.InstallBaselines(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.svc.IP.Workspace.CreateProject(ctx, f.actor.ID, "independent baseline project", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.InstallBaselines(ctx, f.actor, other.ID, input)
	if err != nil {
		t.Fatal("second project reused global operation ID", err)
	}
	if second.BaselineID == first.BaselineID {
		t.Fatal("project baseline shared mutable identity")
	}
	input.Kind = "skill"
	third, err := f.svc.InstallBaselines(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal("Skill and Agent baseline keys collide", err)
	}
	again, err := f.svc.InstallBaselines(ctx, f.actor, f.project, input)
	if err != nil || again.BaselineID != third.BaselineID {
		t.Fatal("repeated preset changed fixed baseline", err)
	}
}
