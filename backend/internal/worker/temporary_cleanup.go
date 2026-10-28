package worker

import (
	"context"
	"errors"
	"os"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/executiontemp"
	"novelstudio/internal/isolation"
	"novelstudio/internal/retrieval"
)

type temporaryCleanupReport struct {
	Documents   executiontemp.Report `json:"documents"`
	Tools       executiontemp.Report `json:"tools"`
	Unavailable int                  `json:"unavailable"`
}

func registerTemporaryCleanup(w temporalworker.Worker) {
	w.RegisterWorkflow(TemporaryCleanupWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context) (temporaryCleanupReport, error) {
		var report temporaryCleanupReport
		var err error
		report.Documents, err = retrieval.CleanupTemporary(ctx)
		if err != nil {
			report.Unavailable++
		}
		report.Tools, err = isolation.CleanupTemporary(ctx, os.Getenv("STUDIO_ISOLATION_ROOT"))
		if err != nil {
			report.Unavailable++
		}
		// Only aggregate counts enter history, including retained blocked items.
		// A missing optional runtime does not interrupt unrelated task delivery.
		return report, ctx.Err()
	}, activity.RegisterOptions{Name: "temporary-cleanup"})
}

func TemporaryCleanupWorkflow(ctx workflow.Context) error {
	bounded := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: 5 * time.Second},
	})
	for batch := 0; batch < 20; batch++ {
		var report temporaryCleanupReport
		if err := workflow.ExecuteActivity(bounded, "temporary-cleanup").Get(ctx, &report); err != nil {
			break
		}
		if !report.Documents.More && !report.Tools.More {
			break
		}
	}
	if err := workflow.Sleep(ctx, 10*time.Minute); err != nil {
		return err
	}
	return workflow.NewContinueAsNewError(ctx, TemporaryCleanupWorkflow)
}

func (r *Runtime) deliverTemporaryCleanup(ctx context.Context) {
	_, err := r.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "execution-temporary-maintenance", TaskQueue: ReviewQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, TemporaryCleanupWorkflow)
	var running *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &running) {
		return
	}
}
