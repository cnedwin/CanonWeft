package worker

import (
	"context"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/service"
	"time"
)

func RegisterIndex(w temporalworker.Worker, svc *service.IndexJobs) {
	w.RegisterWorkflow(IndexWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		running := *svc
		running.Heartbeat = func(ctx context.Context) { activity.RecordHeartbeat(ctx) }
		state, err := running.Execute(ctx, id)
		if err != nil {
			return state, temporal.NewApplicationError("本地索引执行暂未完成", "index-transient")
		}
		return state, nil
	}, activity.RegisterOptions{Name: "index-build"})
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) { return svc.Fail(ctx, id) }, activity.RegisterOptions{Name: "index-failed"})
}
func IndexWorkflow(ctx workflow.Context, id string) (string, error) {
	build := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Minute, HeartbeatTimeout: 5 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second}})
	var state string
	if err := workflow.ExecuteActivity(build, "index-build", id).Get(ctx, &state); err != nil {
		// Preserve the acknowledged job through database outages. Only terminal
		// bookkeeping retries here; the expensive build remains limited to three.
		final := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, MaximumInterval: time.Minute}})
		if recoveryErr := workflow.ExecuteActivity(final, "index-failed", id).Get(ctx, &state); recoveryErr != nil {
			return "FAILED", recoveryErr
		}
	}
	return state, nil
}
