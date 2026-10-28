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

func RegisterReview(w temporalworker.Worker, tasks *service.Tasks) {
	w.RegisterWorkflow(ReviewWorkflow)
	w.RegisterWorkflow(ReviewStep)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) { return tasks.Inspect(ctx, id) }, activity.RegisterOptions{Name: "inspect-candidate"})
}
func ReviewStep(ctx workflow.Context, id string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3}})
	var state string
	err := workflow.ExecuteActivity(ctx, "inspect-candidate", id).Get(ctx, &state)
	return state, err
}

// This business workflow carries immutable references only. No prompt or secret
// enters history. Approval and publication are checked against current SQL authority.
func ReviewWorkflow(ctx workflow.Context, id string) (string, error) {
	state := "QUEUED"
	if err := workflow.SetQueryHandler(ctx, "state", func() (string, error) { return state, nil }); err != nil {
		return "", err
	}
	wake := workflow.GetSignalChannel(ctx, "wake")
	for step := 0; step < 64; step++ {
		child := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowExecutionTimeout: time.Minute})
		if err := workflow.ExecuteChildWorkflow(child, ReviewStep, id).Get(ctx, &state); err != nil {
			return "", err
		}
		if state == "COMPLETED" || state == "CANCELLED" || state == "STALE" {
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
	return "", workflow.NewContinueAsNewError(ctx, ReviewWorkflow, id)
}
