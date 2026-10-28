package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestRecipeWaitsForConfirmationAndPreservesUnknown(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	states := []string{"RUNNING", "WAITING_CONFIRMATION", "WAITING_CONFIRMATION", "PAUSED", "UNKNOWN", "BLOCKED", "RUNNING", "COMPLETED"}
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, id string) (string, error) {
		if id != "only-task-id" {
			t.Fatal("workflow changed authority input")
		}
		state := states[calls]
		calls++
		return state, nil
	}, activity.RegisterOptions{Name: "recipe-advance"})
	env.ExecuteWorkflow(RecipeWorkflow, "only-task-id")
	var state string
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if err := env.GetWorkflowResult(&state); err != nil || state != "COMPLETED" || calls != len(states) {
		t.Fatal(state, calls, err)
	}
}

func TestRecipeBookkeepingFailureNeverBecomesSuccessfulOutput(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	advances, failures := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		advances++
		if advances <= 3 {
			return "", errors.New("db unavailable")
		}
		return "CANCELLED", nil
	}, activity.RegisterOptions{Name: "recipe-advance"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		failures++
		if failures <= 3 {
			return "", errors.New("still unavailable")
		}
		return "BLOCKED", nil
	}, activity.RegisterOptions{Name: "recipe-failed"})
	env.ExecuteWorkflow(RecipeWorkflow, "only-task-id")
	if err := env.GetWorkflowError(); err != nil || advances != 4 || failures != 4 {
		t.Fatal(advances, failures, err)
	}
}
