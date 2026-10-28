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

func RegisterGeneration(w temporalworker.Worker, svc *service.Generations) {
	w.RegisterWorkflow(GenerationWorkflow)
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Inspect), activity.RegisterOptions{Name: "generation-inspect"})
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		state, err := svc.Execute(ctx, id)
		if err != nil {
			return state, temporal.NewNonRetryableApplicationError("生成调用未完成；保留本地执行记录", "generation-failed", nil)
		}
		return state, nil
	}, activity.RegisterOptions{Name: "generation-call-once"})
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Recover), activity.RegisterOptions{Name: "generation-recover"})
}
func GenerationWorkflow(ctx workflow.Context, id string) (string, error) {
	local := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	// Once dispatch may have happened, bookkeeping must survive outages without
	// re-entering the one-shot provider activity or leaving RUNNING orphaned.
	recovery := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
	external := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 4 * time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	wake := workflow.GetSignalChannel(ctx, "wake")
	state := "QUEUED"
	if err := workflow.SetQueryHandler(ctx, "state", func() (string, error) { return state, nil }); err != nil {
		return "", err
	}
	for step := 0; step < 64; step++ {
		if err := workflow.ExecuteActivity(local, "generation-inspect", id).Get(ctx, &state); err != nil {
			return "", err
		}
		if state == "QUEUED" {
			err := workflow.ExecuteActivity(external, "generation-call-once", id).Get(ctx, &state)
			if err != nil || state == "UNKNOWN" || state == "BLOCKED" {
				err = workflow.ExecuteActivity(recovery, "generation-recover", id).Get(ctx, &state)
			}
			if err == nil && state == "PAUSED" {
				continue
			}
			return state, err
		}
		if state != "PAUSED" && state != "PREPARED" {
			if state == "RUNNING" {
				err := workflow.ExecuteActivity(recovery, "generation-recover", id).Get(ctx, &state)
				return state, err
			}
			return state, nil
		}
		timerCtx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, 30*time.Second)
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(timer, func(workflow.Future) {})
		selector.AddReceive(wake, func(ch workflow.ReceiveChannel, _ bool) { var ignored string; ch.Receive(ctx, &ignored); cancel() })
		selector.Select(ctx)
		cancel()
	}
	return "", workflow.NewContinueAsNewError(ctx, GenerationWorkflow, id)
}
