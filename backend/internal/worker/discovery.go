package worker

import (
	"context"
	"errors"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/service"
	"time"
)

func RegisterDiscovery(w temporalworker.Worker, svc *service.SkillDiscovery) {
	w.RegisterWorkflow(DiscoveryWorkflow)
	w.RegisterWorkflow(DiscoveryScheduleWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		running := *svc
		running.Heartbeat = func(ctx context.Context) { activity.RecordHeartbeat(ctx) }
		state, err := running.Execute(ctx, id)
		if errors.Is(err, service.ErrDiscoverySuperseded) {
			return state, temporal.NewNonRetryableApplicationError("发现进度已推进，等待后按持久进度继续", "discovery-superseded", nil)
		}
		if err != nil {
			return state, temporal.NewApplicationError("公开技能发现暂未完成", "discovery-transient")
		}
		return state, nil
	}, activity.RegisterOptions{Name: "discovery-execute"})
	w.RegisterActivityWithOptions(svc.Fail, activity.RegisterOptions{Name: "discovery-failed"})
	w.RegisterActivityWithOptions(svc.Schedule, activity.RegisterOptions{Name: "discovery-schedule"})
}
func DiscoveryWorkflow(ctx workflow.Context, id string) (string, error) {
	execute := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 12 * time.Minute, HeartbeatTimeout: 3 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: 2 * time.Second}})
	for {
		var state string
		err := workflow.ExecuteActivity(execute, "discovery-execute", id).Get(ctx, &state)
		if err != nil {
			var application *temporal.ApplicationError
			if errors.As(err, &application) && application.Type() == "discovery-superseded" {
				if waitErr := workflow.Sleep(ctx, time.Second); waitErr != nil {
					return "RUNNING", waitErr
				}
				continue
			}
			// The outbox is already acknowledged. Keep the final database receipt
			// durable through an outage without restarting exhausted public GETs.
			final := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 0, InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute}})
			err = workflow.ExecuteActivity(final, "discovery-failed", id).Get(ctx, &state)
			if err != nil {
				return "FAILED", err
			}
		}
		if state != "PAUSED" {
			return state, nil
		}
		if err = workflow.Sleep(ctx, 5*time.Second); err != nil {
			return state, err
		}
		if workflow.GetInfo(ctx).GetCurrentHistoryLength() > 1000 {
			return "PAUSED", workflow.NewContinueAsNewError(ctx, DiscoveryWorkflow, id)
		}
	}
}
func DiscoveryScheduleWorkflow(ctx workflow.Context, sourceID string) error {
	options := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5}})
	// Schedule once immediately, then at the next UTC day boundary. The operation
	// receipt makes outbox replay and ContinueAsNew safe within the same day.
	now := workflow.Now(ctx).UTC()
	if err := workflow.ExecuteActivity(options, "discovery-schedule", sourceID, service.DiscoveryDay(now)).Get(ctx, nil); err != nil {
		// A temporary database outage must not permanently orphan the daily source.
		if waitErr := workflow.Sleep(ctx, time.Hour); waitErr != nil {
			return waitErr
		}
		return workflow.NewContinueAsNewError(ctx, DiscoveryScheduleWorkflow, sourceID)
	}
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	if err := workflow.Sleep(ctx, next.Sub(now)); err != nil {
		return err
	}
	return workflow.NewContinueAsNewError(ctx, DiscoveryScheduleWorkflow, sourceID)
}
