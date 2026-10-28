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

func RegisterQualification(w temporalworker.Worker, svc *service.Qualifications) {
	w.RegisterWorkflow(QualificationWorkflow)
	w.RegisterWorkflow(QualificationScheduleWorkflow)
	w.RegisterWorkflow(QualificationReceiptWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, id string) error {
		return retentionActivityError(svc.RefreshReceipts(ctx, id))
	}, activity.RegisterOptions{Name: "qualification-receipts"})
	w.RegisterActivityWithOptions(svc.Schedule, activity.RegisterOptions{Name: "qualification-schedule"})
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Advance), activity.RegisterOptions{Name: "qualification-advance"})
	w.RegisterActivityWithOptions(retentionStateActivity(svc.Fail), activity.RegisterOptions{Name: "qualification-failed"})
}

func QualificationReceiptWorkflow(ctx workflow.Context, id string) error {
	local := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
	return workflow.ExecuteActivity(local, "qualification-receipts", id).Get(ctx, nil)
}

func QualificationScheduleWorkflow(ctx workflow.Context, id string) error {
	options := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5}})
	wake := workflow.GetSignalChannel(ctx, "wake")
	for count := 0; count < 1000; count++ {
		now := workflow.Now(ctx).UTC()
		next := time.Date(now.Year(), now.Month(), now.Day()+7-int(now.Weekday()+6)%7, 0, 0, 0, 0, time.UTC)
		delay := next.Sub(now)
		if err := workflow.ExecuteActivity(options, "qualification-schedule", id, service.QualificationWeek(now)).Get(ctx, nil); err != nil {
			delay = time.Hour
		}
		timerCtx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, delay)
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(timer, func(workflow.Future) {})
		selector.AddReceive(wake, func(channel workflow.ReceiveChannel, _ bool) {
			var ignored string
			channel.Receive(ctx, &ignored)
			cancel()
		})
		selector.Select(ctx)
		cancel()
	}
	return workflow.NewContinueAsNewError(ctx, QualificationScheduleWorkflow, id)
}

func QualificationWorkflow(ctx workflow.Context, id string) (string, error) {
	local := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second}})
	final := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
	state := "QUEUED"
	if err := workflow.SetQueryHandler(ctx, "state", func() (string, error) { return state, nil }); err != nil {
		return "", err
	}
	wake := workflow.GetSignalChannel(ctx, "wake")
	for step := 0; step < 1000; step++ {
		if err := workflow.ExecuteActivity(local, "qualification-advance", id).Get(ctx, &state); err != nil {
			// A durable finalizer survives database outages. External model work is
			// delegated to idempotent child records and is never retried here.
			err = workflow.ExecuteActivity(final, "qualification-failed", id).Get(ctx, &state)
			return state, err
		}
		if state != "RUNNING" && state != "QUEUED" && state != "PAUSED" {
			return state, nil
		}
		timerCtx, cancel := workflow.WithCancel(ctx)
		delay := 500 * time.Millisecond
		if state == "PAUSED" {
			delay = 30 * time.Second
		}
		timer := workflow.NewTimer(timerCtx, delay)
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(timer, func(workflow.Future) {})
		selector.AddReceive(wake, func(ch workflow.ReceiveChannel, _ bool) { var ignored string; ch.Receive(ctx, &ignored); cancel() })
		selector.Select(ctx)
		cancel()
	}
	return "", workflow.NewContinueAsNewError(ctx, QualificationWorkflow, id)
}

func (r *Runtime) WakeQualification(ctx context.Context, id string) error {
	return r.client.SignalWorkflow(ctx, "qualification-"+id, "", "wake", "")
}
