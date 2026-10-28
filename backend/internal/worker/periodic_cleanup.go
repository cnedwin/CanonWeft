package worker

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

type periodicCleanupStatus struct {
	Failures int                           `json:"failures"`
	Last     service.PeriodicCleanupResult `json:"last"`
	Reason   string                        `json:"reason,omitempty"`
}

func periodicCleanupActivity(step func(context.Context, string) (service.PeriodicCleanupResult, error)) func(context.Context, string) (periodicCleanupStatus, error) {
	return func(ctx context.Context, cursor string) (periodicCleanupStatus, error) {
		result, err := step(ctx, cursor)
		status := periodicCleanupStatus{Last: result}
		if err == nil {
			return status, nil
		}
		if ctx.Err() != nil {
			return status, ctx.Err()
		}
		status.Last.State, status.Reason = "BLOCKED", "SCAN_UNAVAILABLE"
		if errors.Is(err, domain.ErrForbidden) {
			status.Reason = "AUTHORITY_UNAVAILABLE"
		}
		if errors.Is(err, domain.ErrConflict) {
			status.Reason = "PLAN_CHANGED"
		}
		// A bad project must not starve later projects. The next hourly pass can
		// retry this scan; existing BLOCKED jobs still cannot be restarted.
		status.Last.Done = result.ProjectID == ""
		return status, nil
	}
}

func registerPeriodicCleanup(w temporalworker.Worker, svc *service.RetentionScanner) {
	w.RegisterWorkflow(PeriodicCleanupWorkflow)
	w.RegisterActivityWithOptions(periodicCleanupActivity(svc.PeriodicCleanupStep), activity.RegisterOptions{Name: "periodic-cleanup"})
}

func PeriodicCleanupWorkflow(ctx workflow.Context, cursor string) error {
	status := periodicCleanupStatus{}
	if err := workflow.SetQueryHandler(ctx, "status", func() (periodicCleanupStatus, error) { return status, nil }); err != nil {
		return err
	}
	bounded := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 90 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: 5 * time.Second}})
	for batch := 0; batch < 20; batch++ {
		var next periodicCleanupStatus
		if err := workflow.ExecuteActivity(bounded, "periodic-cleanup", cursor).Get(ctx, &next); err != nil {
			status.Reason = "MAINTENANCE_UNAVAILABLE"
			if err := workflow.Sleep(ctx, time.Hour); err != nil {
				return err
			}
			return workflow.NewContinueAsNewError(ctx, PeriodicCleanupWorkflow, cursor)
		}
		status.Last = next.Last
		if next.Reason != "" {
			status.Reason = next.Reason
			status.Failures++
		}
		if status.Last.Done {
			if err := workflow.Sleep(ctx, time.Hour); err != nil {
				return err
			}
			return workflow.NewContinueAsNewError(ctx, PeriodicCleanupWorkflow, "")
		}
		if status.Last.Cursor == "" || status.Last.Cursor == cursor {
			return temporal.NewNonRetryableApplicationError("周期清理游标需要检查", "cleanup-cursor-invalid", nil)
		}
		cursor = status.Last.Cursor
	}
	return workflow.NewContinueAsNewError(ctx, PeriodicCleanupWorkflow, cursor)
}
