package qualification_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
	"novelstudio/internal/service"
	platformworker "novelstudio/internal/worker"
)

func TestActualTemporalEvaluationRestartAndOutboxChildren(t *testing.T) {
	f, transport, input := modelFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: "studio-qualification-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: "studio-qualification-integration", WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatal(err)
	}
	queue := "qualification-test-" + service.NewID()
	startWorker := func() temporalworker.Worker {
		w := temporalworker.New(c, queue, temporalworker.Options{})
		platformworker.RegisterQualification(w, f.svc)
		platformworker.RegisterGeneration(w, f.svc.Generations)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := startWorker()
	stopped := false
	defer func() {
		if !stopped {
			w.Stop()
		}
	}()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "qualification-" + r.ID, TaskQueue: queue}, platformworker.QualificationWorkflow, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "test cleanup")
	paused := false
	for i := 0; i < 100; i++ {
		value, queryErr := c.QueryWorkflow(ctx, run.GetID(), "", "state")
		var state string
		if queryErr == nil && value.Get(&state) == nil && state == "PAUSED" {
			paused = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !paused || transport.calls.Load() != 0 {
		t.Fatal("pause not durable before dispatch")
	}
	w.Stop()
	stopped = true
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	replacement := startWorker()
	defer replacement.Stop()
	if err = c.SignalWorkflow(ctx, run.GetID(), "", "wake", ""); err != nil {
		t.Fatal(err)
	}
	// This tests-only delivery loop injects the local provider transport into real
	// child workflows. Outbox rows and workflow IDs remain the actual durable data.
	dispatchCtx, stopDispatch := context.WithCancel(ctx)
	defer stopDispatch()
	done := make(chan error, 1)
	go func() {
		for dispatchCtx.Err() == nil {
			rows, readErr := f.db.QueryContext(dispatchCtx, "SELECT id,resource_id FROM outbox WHERE kind='generation.start' AND processed_at IS NULL")
			if readErr != nil {
				if dispatchCtx.Err() != nil {
					done <- nil
					return
				}
				done <- readErr
				return
			}
			type item struct{ id, child string }
			items := []item{}
			for rows.Next() {
				var v item
				if readErr = rows.Scan(&v.id, &v.child); readErr != nil {
					rows.Close()
					done <- readErr
					return
				}
				items = append(items, v)
			}
			rows.Close()
			for _, v := range items {
				_, startErr := c.ExecuteWorkflow(dispatchCtx, client.StartWorkflowOptions{ID: "generation-" + v.child, TaskQueue: queue}, platformworker.GenerationWorkflow, v.child)
				var already *serviceerror.WorkflowExecutionAlreadyStarted
				if startErr != nil && !errors.As(startErr, &already) {
					done <- startErr
					return
				}
				if _, writeErr := f.db.ExecContext(dispatchCtx, "UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=?", v.id); writeErr != nil {
					done <- writeErr
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		done <- nil
	}()
	var state string
	if err = run.Get(ctx, &state); err != nil || state != "PASSED" || transport.calls.Load() != 18 {
		t.Fatal("restart/children failed", state, transport.calls.Load(), err)
	}
	stopDispatch()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || result.QualificationID == "" || len(result.Steps) != 18 {
		t.Fatal("missing recovered qualification evidence", err)
	}
}

func TestActualRuntimeReceiptOutboxSurvivesRestartAndLaterInvoice(t *testing.T) {
	f, transport, input := modelFixture(t)
	transport.lost = true
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	namespace := "studio-qualification-receipt-" + service.NewID()
	start := func() *platformworker.Runtime {
		runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: f.svc.IP}, os.Getenv("STUDIO_TEMPORAL"), namespace, platformworker.RuntimeServices{Generations: f.svc.Generations})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	runtime := start()
	defer func() {
		if runtime != nil {
			runtime.Close()
		}
	}()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(done func(service.QualificationRun) bool) service.QualificationRun {
		t.Helper()
		for i := 0; i < 350; i++ {
			current, e := f.svc.Read(ctx, f.actor, f.project, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			if done(current) {
				return current
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("durable runtime state did not converge")
		return service.QualificationRun{}
	}
	r = wait(func(current service.QualificationRun) bool {
		return current.State == "BLOCKED" && current.Steps[0].State == "UNKNOWN"
	})
	runtime.Close()
	runtime = nil
	var call string
	var revision int64
	if err = f.db.QueryRow("SELECT c.id,a.revision FROM model_calls c JOIN model_call_accounting a ON a.call_id=c.id WHERE c.project_id=?", f.project).Scan(&call, &revision); err != nil {
		t.Fatal(err)
	}
	b := &service.Budgets{Workspace: f.svc.IP.Workspace}
	if _, err = b.Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: 73, BillReference: "restart-invoice", Evidence: "Fixture invoice received while the runtime is stopped", Reason: "collect receipt after restart", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	runtime = start()
	r = wait(func(current service.QualificationRun) bool {
		return current.Steps[0].State == "RECONCILED" && current.Steps[0].Metrics.Cost == 73
	})
	if r.State != "BLOCKED" || r.QualificationID != "" || !r.Steps[0].Metrics.CostKnown || r.Steps[0].Metrics.UsageKnown || transport.calls.Load() != 1 {
		t.Fatal("receipt event reissued or granted qualification", r.State, r.Steps[0], transport.calls.Load())
	}
}
