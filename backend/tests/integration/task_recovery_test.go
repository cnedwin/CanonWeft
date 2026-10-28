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
	"testing"
	"time"
)

func TestApprovalTaskWaitSurvivesWorkerRestart(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "持久任务", "task-project")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "task-candidate", originalSnapshot())
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Tasks{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	task, err := svc.StartReview(ctx, actor, project, branchID, candidate, "review-task")
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Dial(client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: "studio-task-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: "studio-task-integration", WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		t.Fatal(err)
	}
	startWorker := func() temporalworker.Worker {
		w := temporalworker.New(c, "review-test", temporalworker.Options{})
		platformworker.RegisterReview(w, svc)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := startWorker()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "review-" + task.ID, TaskQueue: "review-test"}, platformworker.ReviewWorkflow, task.ID)
	if err != nil {
		w.Stop()
		t.Fatal(err)
	}
	defer c.TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "test cleanup")
	wait := func(want string) {
		t.Helper()
		for i := 0; i < 80; i++ {
			current, err := svc.Read(ctx, actor, project, task.ID)
			if err == nil && current.State == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("task did not reach %s", want)
	}
	wait("WAITING_ADOPTION")
	// A signal is merely a wake-up, never evidence of approval.
	if err = c.SignalWorkflow(ctx, run.GetID(), "", "wake", "approve"); err != nil {
		w.Stop()
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	current, _ := svc.Read(ctx, actor, project, task.ID)
	if current.State != "WAITING_ADOPTION" {
		w.Stop()
		t.Fatal("forged signal approved candidate")
	}
	w.Stop()
	path := "/api/projects/" + project + "/branches/" + branchID + "/candidates/" + candidate
	b.request("POST", path+"/adopt", map[string]any{}, 200)
	replacement := startWorker()
	defer replacement.Stop()
	if err = c.SignalWorkflow(ctx, run.GetID(), "", "wake", ""); err != nil {
		t.Fatal(err)
	}
	wait("WAITING_PUBLICATION")
	b.request("POST", path+"/publish", map[string]any{}, 200)
	if err = c.SignalWorkflow(ctx, run.GetID(), "", "wake", ""); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = run.Get(ctx, &result); err != nil || result != "COMPLETED" {
		t.Fatal(result, err)
	}
	wait("COMPLETED")
}
