package worker

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"novelstudio/internal/service"
)

func TestPublicationPropagationRetriesLostReceiptAndCancellationIsTerminal(t *testing.T) {
	for _, state := range []string{"COMPLETED", "CANCELLED"} {
		t.Run(state, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			attempts := 0
			env.RegisterActivityWithOptions(func(_ context.Context, id string) (service.PropagationResult, error) {
				if id != "event-id" {
					t.Fatal("workflow replaced event identity", id)
				}
				attempts++
				if attempts < 5 {
					return service.PropagationResult{}, errors.New("commit receipt unavailable")
				}
				return service.PropagationResult{State: state}, nil
			}, activity.RegisterOptions{Name: "publication-propagate"})
			env.ExecuteWorkflow(ChangePropagationWorkflow, "event-id")
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var result service.PropagationResult
			if err := env.GetWorkflowResult(&result); err != nil || result.State != state || attempts != 5 {
				t.Fatal(result, attempts, err)
			}
		})
	}
}
