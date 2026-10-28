package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"testing"
)

func TestQualificationFinalizerRetriesPastLongDatabaseOutage(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	advance, final := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		advance++
		return "", errors.New("database unavailable")
	}, activity.RegisterOptions{Name: "qualification-advance"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		final++
		if final <= 4 {
			return "", errors.New("database unavailable")
		}
		return "FAILED", nil
	}, activity.RegisterOptions{Name: "qualification-failed"})
	env.ExecuteWorkflow(QualificationWorkflow, "test-run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "FAILED" || advance != 3 || final != 5 {
		t.Fatal("terminal state orphaned after outage", state, advance, final, err)
	}
}

func TestGenerationReceiptRecoveryRetriesPastLongDatabaseOutageWithoutResend(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	sent, recovered := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) { return "QUEUED", nil }, activity.RegisterOptions{Name: "generation-inspect"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		sent++
		return "UNKNOWN", errors.New("receipt persistence unavailable")
	}, activity.RegisterOptions{Name: "generation-call-once"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		recovered++
		if recovered <= 4 {
			return "", errors.New("database unavailable")
		}
		return "UNKNOWN", nil
	}, activity.RegisterOptions{Name: "generation-recover"})
	env.ExecuteWorkflow(GenerationWorkflow, "test-generation")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "UNKNOWN" || sent != 1 || recovered != 5 {
		t.Fatal("provider resend or lost recovery", state, sent, recovered, err)
	}
}

func TestQualificationReceiptOnlyWorkflowRetriesOutageWithoutEvaluation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	collected := 0
	env.RegisterActivityWithOptions(func(context.Context, string) error {
		collected++
		if collected <= 4 {
			return errors.New("receipt database unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: "qualification-receipts"})
	env.ExecuteWorkflow(QualificationReceiptWorkflow, "terminal-run")
	if err := env.GetWorkflowError(); err != nil || collected != 5 {
		t.Fatal("receipt-only workflow lost durable retry", collected, err)
	}
}
