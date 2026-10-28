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

func RegisterIngest(w temporalworker.Worker, svc *service.IngestJobs) {
	w.RegisterWorkflow(IngestWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) (string, error) {
		state, err := svc.Advance(ctx, id)
		if err != nil {
			return "", temporal.NewApplicationError("文件处理暂未完成，已保存的页面会保留", "ingest-retry")
		}
		return state, nil
	}, activity.RegisterOptions{Name: "ingest-advance"})
}
func IngestWorkflow(ctx workflow.Context, id string) (string, error) {
	bounded := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 90 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, MaximumInterval: time.Minute}})
	for step := 0; step < 64; step++ {
		var state string
		if err := workflow.ExecuteActivity(bounded, "ingest-advance", id).Get(ctx, &state); err != nil {
			return "", err
		}
		if state != "RUNNING" && state != "QUEUED" && state != "PAUSED" {
			return state, nil
		}
		if state == "PAUSED" {
			if err := workflow.Sleep(ctx, 2*time.Second); err != nil {
				return "", err
			}
		}
	}
	return "", workflow.NewContinueAsNewError(ctx, IngestWorkflow, id)
}
