package worker

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/service"
)

func RegisterChangePropagation(w temporalworker.Worker, svc *service.IP) {
	w.RegisterWorkflow(ChangePropagationWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, eventID string) (service.PropagationResult, error) {
		result, err := svc.PropagatePublication(ctx, eventID)
		if err != nil {
			// Keep SQL diagnostics/private content out of durable workflow history.
			return result, temporal.NewApplicationError("发布后的影响登记尚未完成", "publication-propagation-pending")
		}
		return result, nil
	}, activity.RegisterOptions{Name: "publication-propagate"})
}

func ChangePropagationWorkflow(ctx workflow.Context, eventID string) (service.PropagationResult, error) {
	// Once the transactional event is accepted, a database outage must not lose
	// it. Service-level receipts make activity retry safe after an ambiguous commit.
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute},
	})
	var result service.PropagationResult
	err := workflow.ExecuteActivity(ctx, "publication-propagate", eventID).Get(ctx, &result)
	return result, err
}
