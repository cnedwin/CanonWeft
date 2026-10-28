package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestToolTrialTransientFailureIsBoundedAndPersisted(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	calls, closed := 0, false
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("temporary read failure")
	}, activity.RegisterOptions{Name: "tool-trial-execute"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) { closed = true; return "FAILED", nil }, activity.RegisterOptions{Name: "tool-trial-failed"})
	env.ExecuteWorkflow(ToolTrialWorkflow, "trial")
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || calls != 3 || !closed || state != "FAILED" {
		t.Fatal(state, calls, closed, err)
	}
}

func TestToolTrialFinalStateWaitsForDatabaseRecovery(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, finalizations := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		executions++
		return "", errors.New("execution storage failed")
	}, activity.RegisterOptions{Name: "tool-trial-execute"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		finalizations++
		if finalizations <= 7 {
			return "", errors.New("database unavailable")
		}
		return "FAILED", nil
	}, activity.RegisterOptions{Name: "tool-trial-failed"})
	env.ExecuteWorkflow(ToolTrialWorkflow, "trial")
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "FAILED" || executions != 3 || finalizations != 8 {
		t.Fatal(state, executions, finalizations, err)
	}
}
