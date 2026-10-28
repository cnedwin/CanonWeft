package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestSemanticReviewKeepsBlockedAndUnknownRequestsRecoverable(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		calls++
		return []string{"BLOCKED", "UNKNOWN", "RUNNING", "COMPLETED"}[calls-1], nil
	}, activity.RegisterOptions{Name: "semantic-review-advance"})
	env.ExecuteWorkflow(SemanticReviewWorkflow, "review")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "COMPLETED" || calls != 4 {
		t.Fatal("review abandoned recoverable state", state, calls, err)
	}
}

func TestSemanticReviewBookkeepingFailureIsDurablyMarkedBlocked(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	attempts, finals := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		attempts++
		if attempts <= 3 {
			return "", errors.New("db down")
		}
		return "CANCELLED", nil
	}, activity.RegisterOptions{Name: "semantic-review-advance"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		finals++
		if finals <= 4 {
			return "", errors.New("db down")
		}
		return "BLOCKED", nil
	}, activity.RegisterOptions{Name: "semantic-review-failed"})
	env.ExecuteWorkflow(SemanticReviewWorkflow, "review")
	if err := env.GetWorkflowError(); err != nil || attempts != 4 || finals != 5 {
		t.Fatal("review lost durable bookkeeping", attempts, finals, err)
	}
}
