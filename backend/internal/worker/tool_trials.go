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

func RegisterToolTrials(w temporalworker.Worker, svc *service.ToolTrials) {
	w.RegisterWorkflow(ToolTrialWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		running := *svc
		running.Heartbeat = func(ctx context.Context) { activity.RecordHeartbeat(ctx) }
		state, err := running.Execute(ctx, id)
		if err != nil {
			return state, temporal.NewApplicationError("工具试评暂未完成", "tool-transient")
		}
		return state, nil
	}, activity.RegisterOptions{Name: "tool-trial-execute"})
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Fail), activity.RegisterOptions{Name: "tool-trial-failed"})
}

func ToolTrialWorkflow(ctx workflow.Context, id string) (string, error) {
	execute := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 3 * time.Minute, HeartbeatTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: 2 * time.Second}})
	var state string
	var err error
	for {
		err = workflow.ExecuteActivity(execute, "tool-trial-execute", id).Get(ctx, &state)
		if err != nil || state != "PAUSED" {
			break
		}
		if err = workflow.Sleep(ctx, time.Second); err != nil {
			return state, err
		}
		if workflow.GetInfo(ctx).GetCurrentHistoryLength() > 1000 {
			return "", workflow.NewContinueAsNewError(ctx, ToolTrialWorkflow, id)
		}
	}
	if err != nil {
		// Execution remains bounded. Persisting its terminal state waits durably
		// through an outage so acknowledged outbox entries cannot orphan jobs.
		final := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, MaximumInterval: time.Minute}})
		err = workflow.ExecuteActivity(final, "tool-trial-failed", id).Get(ctx, &state)
	}
	return state, err
}
