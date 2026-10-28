package integration

import (
	"context"
	"errors"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryTemporalPauseRestartAndDailyReceipt(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "持久发现", "discovery-temporal")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Remote: &discoveryFixture{commit: strings.Repeat("d", 40)}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Control(ctx, actor, project, run.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	c, err := client.Dial(client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: "studio-discovery-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: "studio-discovery-integration", WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatal(err)
	}
	start := func() temporalworker.Worker {
		w := temporalworker.New(c, "discovery-test", temporalworker.Options{})
		platformworker.RegisterDiscovery(w, svc)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := start()
	execution, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "discovery-" + run.ID, TaskQueue: "discovery-test"}, platformworker.DiscoveryWorkflow, run.ID)
	if err != nil {
		w.Stop()
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), execution.GetID(), "", "test cleanup")
	time.Sleep(500 * time.Millisecond)
	w.Stop()
	if _, err = svc.Control(ctx, actor, project, run.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	replacement := start()
	defer replacement.Stop()
	var state string
	if err = execution.Get(ctx, &state); err != nil || state != "COMPLETED" {
		t.Fatal("restart failed", state, err)
	}
	schedule, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "discovery-schedule-" + source.ID, TaskQueue: "discovery-test"}, platformworker.DiscoveryScheduleWorkflow, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), schedule.GetID(), "", "test cleanup")
	for attempt := 0; attempt < 60; attempt++ {
		overview, e := svc.Overview(ctx, actor, project)
		if e == nil && len(overview.Runs) == 2 {
			break
		}
		if attempt == 59 {
			t.Fatal("daily workflow did not queue persisted run", e)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = svc.Schedule(ctx, source.ID, service.DiscoveryDay(time.Now())); err != nil {
		t.Fatal(err)
	}
	overview, err := svc.Overview(ctx, actor, project)
	if err != nil || len(overview.Runs) != 2 {
		t.Fatal("daily replay duplicated", overview, err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=? AND kind='discovery.start'", project).Scan(&count); err != nil || count != 2 {
		t.Fatal("missing durable outbox", count, err)
	}
}
