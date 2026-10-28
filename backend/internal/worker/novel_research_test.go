package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestResearchCheckpointRetriesWithoutChangingEventIdentity(t *testing.T) {
	for _, state := range []string{"QUEUED", "SUPERSEDED", "CANCELLED", "WAITING_SELECTION", "WAITING_RETRY", "COMPLETED"} {
		t.Run(state, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			attempts := 0
			env.RegisterActivityWithOptions(func(_ context.Context, id string) (string, error) {
				if id != "opaque-research-event" {
					t.Fatal("event changed")
				}
				attempts++
				if attempts < 3 {
					return "", errors.New("checkpoint receipt unavailable")
				}
				return state, nil
			}, activity.RegisterOptions{Name: "research-checkpoint"})
			env.ExecuteWorkflow(ResearchExecutionWorkflow, "opaque-research-event")
			var result string
			if err := env.GetWorkflowResult(&result); err != nil || result != state || attempts != 3 {
				t.Fatal(result, attempts, err)
			}
		})
	}
}
