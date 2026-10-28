package integration

import (
	"context"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	platformworker "novelstudio/internal/worker"
	"os"
	"testing"
	"time"
)

// Run by verify-runtime.ps1 in two processes separated by a real service restart.
func TestTemporalServerRestart(t *testing.T) {
	phase := os.Getenv("STUDIO_PROBE_PHASE")
	if phase == "" {
		t.Skip("two-process restart test: run scripts/verify-runtime.ps1")
	}
	id := os.Getenv("STUDIO_PROBE_ID")
	if id == "" {
		t.Fatal("restart probe identity required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := client.Dial(client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: "studio-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := temporalworker.New(c, "foundation-restart", temporalworker.Options{})
	w.RegisterWorkflow(platformworker.PersistenceProbe)
	if err = w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if phase == "start" {
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: "foundation-restart"}, platformworker.PersistenceProbe, "immutable-reference-before-restart")
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 80; attempt++ {
			value, err := c.QueryWorkflow(ctx, id, run.GetRunID(), "state")
			if err == nil {
				var state string
				if err = value.Get(&state); err == nil && state == "WAITING" {
					return
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatal("workflow did not persist WAITING before shutdown")
	}
	if phase != "finish" {
		t.Fatal("unknown restart phase")
	}
	// Query replays the persisted history; no workflow is started in this phase.
	var state string
	for attempt := 0; attempt < 100; attempt++ {
		value, queryErr := c.QueryWorkflow(ctx, id, "", "state")
		if queryErr == nil {
			err = value.Get(&state)
			if err == nil {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if state != "WAITING" {
		t.Fatalf("lost persisted wait after restart: %q", state)
	}
	if err = c.SignalWorkflow(ctx, id, "", "continue", "immutable-reference-before-restart"); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = c.GetWorkflow(ctx, id, "").Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if result != "immutable-reference-before-restart" {
		t.Fatal("reference changed after restart")
	}
	history := c.GetWorkflowHistory(ctx, id, "", false, 0)
	events := 0
	for history.HasNext() {
		if _, err = history.Next(); err != nil {
			t.Fatal(err)
		}
		events++
	}
	if events < 5 {
		t.Fatal("execution history was not retained")
	}
}
