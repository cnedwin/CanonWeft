package integration

import (
	"context"
	"errors"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
	platformworker "novelstudio/internal/worker"
	"os"
	"testing"
	"time"
)

func TestGenerationWorkflowResumesAfterWorkerRestart(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	_, svc, actor, project, task := generationFixture(t, transport)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := svc.Start(ctx, actor, project, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Control(ctx, actor, project, task.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: "studio-generation-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: "studio-generation-integration", WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatal(err)
	}
	startWorker := func() temporalworker.Worker {
		w := temporalworker.New(c, "generation-test", temporalworker.Options{})
		platformworker.RegisterGeneration(w, svc)
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
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "generation-" + task.ID, TaskQueue: "generation-test"}, platformworker.GenerationWorkflow, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "test cleanup")
	paused := false
	for i := 0; i < 50; i++ {
		value, queryErr := c.QueryWorkflow(ctx, run.GetID(), "", "state")
		var state string
		if queryErr == nil && value.Get(&state) == nil && state == "PAUSED" {
			paused = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !paused || transport.calls.Load() != 0 {
		t.Fatal("pause did not persist before dispatch")
	}
	w.Stop()
	stopped = true
	if _, err = svc.Control(ctx, actor, project, task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	replacement := startWorker()
	defer replacement.Stop()
	if err = c.SignalWorkflow(ctx, run.GetID(), "", "wake", ""); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = run.Get(ctx, &result); err != nil || result != "SUCCEEDED" || transport.calls.Load() != 1 {
		t.Fatal("generation recovery", result, transport.calls.Load(), err)
	}
	// Simulate a lost completion marker after the durable usage receipt exists.
	if _, err = svc.IP.Workspace.DB.Exec("UPDATE generation_tasks SET state='RUNNING' WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	if result, err = svc.Recover(ctx, task.ID); err != nil || result != "SUCCEEDED" || transport.calls.Load() != 1 {
		t.Fatal("recovery resent provider request", result, err)
	}
}
