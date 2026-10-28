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

func TestTemporalDurableWait(t *testing.T) {
	address := os.Getenv("STUDIO_TEMPORAL")
	if address == "" {
		t.Fatal("real STUDIO_TEMPORAL endpoint required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	namespace := "studio-integration"
	c, err := client.Dial(client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: namespace, WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatal(err)
	}
	w := temporalworker.New(c, "foundation-probe", temporalworker.Options{})
	w.RegisterWorkflow(platformworker.PersistenceProbe)
	if err = w.Start(); err != nil {
		t.Fatal(err)
	}
	running := true
	defer func() {
		if running {
			w.Stop()
		}
	}()
	id := "foundation-probe-" + time.Now().UTC().Format("20060102T150405.000000000")
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: "foundation-probe"}, platformworker.PersistenceProbe, "snapshot:fixed")
	if err != nil {
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), id, run.GetRunID(), "test cleanup")
	var state string
	for attempt := 0; attempt < 40; attempt++ {
		query, queryErr := c.QueryWorkflow(ctx, id, run.GetRunID(), "state")
		if queryErr == nil {
			err = query.Get(&state)
			break
		}
		err = queryErr
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if state != "WAITING" {
		t.Fatalf("state=%s want WAITING", state)
	}
	w.Stop()
	running = false
	replacement := temporalworker.New(c, "foundation-probe", temporalworker.Options{})
	replacement.RegisterWorkflow(platformworker.PersistenceProbe)
	if err = replacement.Start(); err != nil {
		t.Fatal(err)
	}
	defer replacement.Stop()
	if err = c.SignalWorkflow(ctx, id, run.GetRunID(), "continue", "snapshot:fixed"); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = run.Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if result != "snapshot:fixed" {
		t.Fatalf("lost frozen reference: %q", result)
	}
}
