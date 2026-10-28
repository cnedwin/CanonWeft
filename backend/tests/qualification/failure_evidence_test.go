package qualification_test

import (
	"context"
	"testing"
)

func TestInvalidOutputRetainsActualUsageAndFailureArtifact(t *testing.T) {
	f, p, input := modelFixture(t)
	p.invalid = true
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	if r.State != "FAILED" || r.QualificationID != "" || len(r.Steps) != 18 {
		t.Fatal("invalid output self-qualified", r.State, r.QualificationID)
	}
	for _, step := range r.Steps {
		if step.State != "INVALID_OUTPUT" || !step.Metrics.UsageKnown || step.Metrics.InputTokens != 20 || step.Metrics.OutputTokens != 10 || step.Metrics.OutputHash == "" || step.Metrics.Output != "invalid JSON candidate" {
			t.Fatal("actual failure usage/artifact discarded", step.State, step.Metrics)
		}
	}
}
