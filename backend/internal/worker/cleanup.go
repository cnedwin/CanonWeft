package worker

import (
	"context"
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func RegisterCleanup(w temporalworker.Worker, svc *service.RetentionScanner) {
	registerPeriodicCleanup(w, svc)
	w.RegisterWorkflow(CleanupWorkflow)
	w.RegisterWorkflow(RecoveryCleanupWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context) (bool, error) {
		advanced, err := svc.ReconcileRecovery(ctx)
		if err != nil {
			return false, temporal.NewApplicationError("恢复文件暂未核对完成", "recovery-cleanup-retry")
		}
		return advanced, nil
	}, activity.RegisterOptions{Name: "recovery-cleanup"})
	w.RegisterActivityWithOptions(cleanupActivity(svc.AdvanceJob, svc.HoldUnavailableCleanup), activity.RegisterOptions{Name: "cleanup-advance"})
}

// The existing cleanup runtime owns this maintenance workflow. It performs a
// bounded batch, sleeps durably, then rotates history; no process timer decides
// whether author files are safe to remove.
func RecoveryCleanupWorkflow(ctx workflow.Context) error {
	bounded := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 90 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 5 * time.Second, MaximumInterval: time.Minute}})
	for step := 0; step < 20; step++ {
		var more bool
		if err := workflow.ExecuteActivity(bounded, "recovery-cleanup").Get(ctx, &more); err != nil {
			return err
		}
		if !more {
			break
		}
	}
	if err := workflow.Sleep(ctx, 10*time.Minute); err != nil {
		return err
	}
	return workflow.NewContinueAsNewError(ctx, RecoveryCleanupWorkflow)
}

func cleanupActivity(advance func(context.Context, string) (string, error), hold func(context.Context, string) error) func(context.Context, string) (string, error) {
	return func(ctx context.Context, id string) (string, error) {
		state, err := advance(ctx, id)
		if err == nil {
			return state, nil
		}
		if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrConflict) {
			if hold(ctx, id) == nil {
				return "BLOCKED", nil
			}
		}
		return "", temporal.NewApplicationError("清理暂未完成，已保存进度", "cleanup-retry")
	}
}

// Only the job ID and closed states enter Temporal history. SQL owns every
// checkpoint, claim and permission check, including redelivery after a crash.
func CleanupWorkflow(ctx workflow.Context, id string) (string, error) {
	bounded := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 90 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, MaximumInterval: time.Minute}})
	for step := 0; step < 64; step++ {
		var state string
		if err := workflow.ExecuteActivity(bounded, "cleanup-advance", id).Get(ctx, &state); err != nil {
			return "", err
		}
		switch state {
		case "DETACHING", "DELETING", "VERIFYING", "RETRY_PENDING":
		case "COMPLETE", "BLOCKED", "OVERDUE", "CANCELLED", "PLANNED":
			return state, nil
		default:
			return "", temporal.NewNonRetryableApplicationError("清理状态需要检查", "cleanup-invalid-state", nil)
		}
	}
	return "", workflow.NewContinueAsNewError(ctx, CleanupWorkflow, id)
}

// retention_jobs itself is the transactional dispatch record. No duplicate
// outbox payload, copied plan or separate delivery flag is required. A terminal
// job is never selected; a manual retry reopens its persisted execution state.
func (r *Runtime) deliverCleanupBatch(ctx context.Context) {
	if r.cleanup == nil {
		return
	}
	_, _ = r.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "periodic-material-maintenance", TaskQueue: ReviewQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE}, PeriodicCleanupWorkflow, "")
	_, recoveryErr := r.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "recovery-object-maintenance", TaskQueue: ReviewQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE}, RecoveryCleanupWorkflow)
	var recoveryRunning *serviceerror.WorkflowExecutionAlreadyStarted
	if recoveryErr != nil && !errors.As(recoveryErr, &recoveryRunning) {
		return
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM retention_jobs WHERE restored_disabled=0 AND state IN ('DETACHING','DELETING','VERIFYING','RETRY_PENDING') ORDER BY updated_at,id LIMIT 20")
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, id := range ids {
		options := client.StartWorkflowOptions{ID: "cleanup-" + id, TaskQueue: ReviewQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE}
		_, err := r.client.ExecuteWorkflow(ctx, options, CleanupWorkflow, id)
		var running *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &running) {
			return
		}
	}
}
