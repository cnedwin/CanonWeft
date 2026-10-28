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

func RegisterRecipe(w temporalworker.Worker, svc *service.RecipeExecutions) {
	w.RegisterWorkflow(RecipeWorkflow)
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Advance), activity.RegisterOptions{Name: "recipe-advance"})
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Fail), activity.RegisterOptions{Name: "recipe-failed"})
}

// Temporal history contains an opaque execution ID and states only. Stage
// content, credentials, confirmations and costs remain in their own stores.
func RecipeWorkflow(ctx workflow.Context, id string) (string, error) {
	local := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second}})
	durable := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
	state := "RUNNING"
	if err := workflow.SetQueryHandler(ctx, "state", func() (string, error) { return state, nil }); err != nil {
		return "", err
	}
	wake := workflow.GetSignalChannel(ctx, "wake")
	for step := 0; step < 1000; step++ {
		if err := workflow.ExecuteActivity(local, "recipe-advance", id).Get(ctx, &state); err != nil {
			if err = workflow.ExecuteActivity(durable, "recipe-failed", id).Get(ctx, &state); err != nil {
				return "", err
			}
		}
		if state == "COMPLETED" || state == "FAILED" || state == "CANCELLED" {
			return state, nil
		}
		delay := time.Second
		if state != "RUNNING" {
			delay = 30 * time.Second
		}
		timerCtx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, delay)
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(timer, func(workflow.Future) {})
		selector.AddReceive(wake, func(ch workflow.ReceiveChannel, _ bool) { var ignored string; ch.Receive(ctx, &ignored); cancel() })
		selector.Select(ctx)
		cancel()
	}
	return "", workflow.NewContinueAsNewError(ctx, RecipeWorkflow, id)
}

func (r *Runtime) WakeRecipe(ctx context.Context, id string) error {
	return r.client.SignalWorkflow(ctx, "recipe-"+id, "", "wake", "")
}
