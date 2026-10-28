package worker_test

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	platformworker "novelstudio/internal/worker"
	"testing"
)

func TestWorkflowHistoryRollsOver(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(platformworker.ReviewWorkflow)
	env.RegisterWorkflow(platformworker.ReviewStep)
	checks := 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) { checks++; return "WAITING_ADOPTION", nil }, activity.RegisterOptions{Name: "inspect-candidate"})
	env.ExecuteWorkflow(platformworker.ReviewWorkflow, "fixed-candidate-reference")
	var rollover *workflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &rollover) {
		t.Fatal("long wait did not roll over", env.GetWorkflowError())
	}
	if checks != 64 {
		t.Fatalf("checks=%d", checks)
	}
}
