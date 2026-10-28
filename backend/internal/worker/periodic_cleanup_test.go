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
	"novelstudio/internal/service"
)

func TestPeriodicCleanupBoundsCursorAndKeepsFailurePrivate(t *testing.T) {
	for _, finite := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		calls := 0
		env.RegisterActivityWithOptions(periodicCleanupActivity(func(_ context.Context, cursor string) (service.PeriodicCleanupResult, error) {
			calls++
			if calls > 1 && cursor != fmt.Sprintf("%032x", calls-1) {
				t.Error("cursor lost", cursor)
			}
			if finite && calls == 3 {
				return service.PeriodicCleanupResult{Done: true, State: "COMPLETE"}, nil
			}
			id := fmt.Sprintf("%032x", calls)
			return service.PeriodicCleanupResult{Cursor: id, ProjectID: id}, errors.New("PRIVATE_DATABASE_SOURCE")
		}), activity.RegisterOptions{Name: "periodic-cleanup"})
		env.ExecuteWorkflow(PeriodicCleanupWorkflow, "")
		want := 20
		if finite {
			want = 3
		}
		if calls != want || !workflow.IsContinueAsNewError(env.GetWorkflowError()) {
			t.Fatal(calls, env.GetWorkflowError())
		}
	}
	fn := periodicCleanupActivity(func(context.Context, string) (service.PeriodicCleanupResult, error) {
		return service.PeriodicCleanupResult{}, fmt.Errorf("PRIVATE: %w", domain.ErrForbidden)
	})
	status, err := fn(context.Background(), "")
	if err != nil || !status.Last.Done || status.Reason != "AUTHORITY_UNAVAILABLE" || strings.Contains(fmt.Sprint(status), "PRIVATE") {
		t.Fatal(status, err)
	}
}
