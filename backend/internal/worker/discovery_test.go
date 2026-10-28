package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"testing"
	"time"
)

func TestDiscoveryDailyScheduleSurvivesTemporaryDatabaseOutage(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, string, string) error { calls++; return errors.New("database unavailable") }, activity.RegisterOptions{Name: "discovery-schedule"})
	start := env.Now()
	env.ExecuteWorkflow(DiscoveryScheduleWorkflow, "source")
	if !workflow.IsContinueAsNewError(env.GetWorkflowError()) {
		t.Fatalf("daily schedule permanently stopped after transient outage: %v", env.GetWorkflowError())
	}
	if calls != 5 || env.Now().Sub(start) < time.Hour {
		t.Fatal("missing bounded retry and durable recovery timer", calls, env.Now().Sub(start))
	}
}

func TestDiscoveryNetworkFailureIsBoundedAndVisible(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	calls := 0
	failed := false
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		calls++
		return "RUNNING", errors.New("public source unavailable")
	}, activity.RegisterOptions{Name: "discovery-execute"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) { failed = true; return "FAILED", nil }, activity.RegisterOptions{Name: "discovery-failed"})
	env.ExecuteWorkflow(DiscoveryWorkflow, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "FAILED" || calls != 3 || !failed {
		t.Fatal("failure not recorded after bounded retries", state, calls, failed, err)
	}
}

func TestDiscoveryFinalizerWaitsForDatabaseRecoveryWithoutRefetch(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, finalizations := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		executions++
		return "RUNNING", errors.New("public request failed")
	}, activity.RegisterOptions{Name: "discovery-execute"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		finalizations++
		if finalizations <= 8 {
			return "", errors.New("database still unavailable")
		}
		return "FAILED", nil
	}, activity.RegisterOptions{Name: "discovery-failed"})
	start := env.Now()
	env.ExecuteWorkflow(DiscoveryWorkflow, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("orphaned active run before database recovered: %v", err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "FAILED" || executions != 3 || finalizations != 9 {
		t.Fatal("recovery refetched or failed to persist final state", state, executions, finalizations, err)
	}
	if env.Now().Sub(start) < time.Minute {
		t.Fatal("missing durable retry delay")
	}
}

func TestDiscoverySupersededActivityContinuesWithoutFinalizing(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, finalizations := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) {
		executions++
		if executions == 1 {
			return "RUNNING", temporal.NewNonRetryableApplicationError("progress advanced", "discovery-superseded", nil)
		}
		return "COMPLETED", nil
	}, activity.RegisterOptions{Name: "discovery-execute"})
	env.RegisterActivityWithOptions(func(context.Context, string) (string, error) { finalizations++; return "FAILED", nil }, activity.RegisterOptions{Name: "discovery-failed"})
	start := env.Now()
	env.ExecuteWorkflow(DiscoveryWorkflow, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := env.GetWorkflowResult(&state); err != nil || state != "COMPLETED" || executions != 2 || finalizations != 0 {
		t.Fatal("superseded activity incorrectly finalized shared task", state, executions, finalizations, err)
	}
	if env.Now().Sub(start) < time.Second {
		t.Fatal("missing durable wait before resuming progress")
	}
}
