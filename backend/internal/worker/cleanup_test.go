package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/domain"
)

func TestRecoveryMaintenanceBoundsBatchAndUsesDurableTimer(t *testing.T) {
	for _, busy := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		calls := 0
		env.RegisterActivityWithOptions(func(context.Context) (bool, error) {
			calls++
			return busy, nil
		}, activity.RegisterOptions{Name: "recovery-cleanup"})
		env.ExecuteWorkflow(RecoveryCleanupWorkflow)
		want := 1
		if busy {
			want = 20
		}
		if calls != want || !workflow.IsContinueAsNewError(env.GetWorkflowError()) {
			t.Fatal("maintenance did not rotate bounded history", calls, env.GetWorkflowError())
		}
	}
}

func TestCleanupWorkflowRetriesAndStopsAtDurableBoundary(t *testing.T) {
	for _, terminal := range []string{"COMPLETE", "BLOCKED", "CANCELLED"} {
		t.Run(terminal, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			calls := 0
			env.RegisterActivityWithOptions(cleanupActivity(func(context.Context, string) (string, error) {
				calls++
				if calls == 1 {
					return "", errors.New("PRIVATE_DATABASE_DETAIL")
				}
				if calls == 2 {
					return "DETACHING", nil
				}
				if calls == 3 {
					return "DELETING", nil
				}
				return terminal, nil
			}, func(context.Context, string) error { t.Fatal("transient failure held job"); return nil }), activity.RegisterOptions{Name: "cleanup-advance"})
			env.ExecuteWorkflow(CleanupWorkflow, "opaque-job")
			var result string
			if err := env.GetWorkflowResult(&result); err != nil || result != terminal || calls != 4 {
				t.Fatal(result, calls, err)
			}
		})
	}
}

func TestCleanupActivityHoldsRevokedJobAndRedactsErrors(t *testing.T) {
	for _, cause := range []error{domain.ErrForbidden, domain.ErrConflict, errors.New("PRIVATE_INTERNAL_ERROR")} {
		held := 0
		fn := cleanupActivity(func(context.Context, string) (string, error) { return "", fmt.Errorf("PRIVATE_SOURCE: %w", cause) }, func(context.Context, string) error { held++; return nil })
		state, err := fn(context.Background(), "opaque-job")
		if errors.Is(cause, domain.ErrForbidden) || errors.Is(cause, domain.ErrConflict) {
			if err != nil || state != "BLOCKED" || held != 1 {
				t.Fatal(state, held, err)
			}
		} else if err == nil || strings.Contains(err.Error(), "PRIVATE") || held != 0 {
			t.Fatal("private data or unexpected hold", held, err)
		}
	}
}
