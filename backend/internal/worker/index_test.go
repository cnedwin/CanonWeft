package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestIndexFinalizationSurvivesDatabaseOutageWithoutRebuilding(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	builds, finalizations := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		builds++
		return "", errors.New("index service unavailable")
	}, activity.RegisterOptions{Name: "index-build"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		finalizations++
		if finalizations <= 7 {
			return "", errors.New("database unavailable")
		}
		return "FAILED", nil
	}, activity.RegisterOptions{Name: "index-failed"})
	env.ExecuteWorkflow(IndexWorkflow, "job")
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "FAILED" || builds != 3 || finalizations != 8 {
		t.Fatal(state, builds, finalizations, err)
	}
}
