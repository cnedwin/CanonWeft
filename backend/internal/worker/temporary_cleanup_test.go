package worker

import (
	"context"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/executiontemp"
	"testing"
)

func TestTemporaryCleanupBoundsWorkAndKeepsBlockedCounts(t *testing.T) {
	for _, busy := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		calls := 0
		env.RegisterActivityWithOptions(func(context.Context) (temporaryCleanupReport, error) {
			calls++
			return temporaryCleanupReport{Documents: executiontemp.Report{Blocked: 1, More: busy}}, nil
		}, activity.RegisterOptions{Name: "temporary-cleanup"})
		env.ExecuteWorkflow(TemporaryCleanupWorkflow)
		want := 1
		if busy {
			want = 20
		}
		if calls != want || !workflow.IsContinueAsNewError(env.GetWorkflowError()) {
			t.Fatal(calls, env.GetWorkflowError())
		}
	}
}
