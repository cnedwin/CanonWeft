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

func RegisterResearchExecutions(w temporalworker.Worker, svc *service.NovelResearchExecutions) {
	w.RegisterWorkflow(ResearchExecutionWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		state, err := svc.AdvanceBackground(ctx, id)
		if err != nil {
			return "", temporal.NewApplicationError("研究检查点暂未完成，等待恢复", "research-checkpoint-pending")
		}
		return state, nil
	}, activity.RegisterOptions{Name: "research-checkpoint"})
}

// Only an opaque event ID and state enter workflow history; source text, query
// content, permissions and checkpoint authority remain in the application DB.
func ResearchExecutionWorkflow(ctx workflow.Context, eventID string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 45 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	var state string
	err := workflow.ExecuteActivity(ctx, "research-checkpoint", eventID).Get(ctx, &state)
	return state, err
}
