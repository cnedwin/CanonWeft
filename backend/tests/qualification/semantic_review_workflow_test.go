package qualification_test

import (
	"context"
	"errors"
	"go.temporal.io/sdk/client"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	platformworker "novelstudio/internal/worker"
	"os"
	"testing"
	"time"
)

func TestSemanticReviewActualWorkerRestartAndOutboxReplayDoNotResend(t *testing.T) {
	if os.Getenv("STUDIO_TEMPORAL") == "" {
		t.Skip("explicit local Temporal required")
	}
	f, source, binding, protocol, price := semanticReviewFixture(t)
	// Qualification setup is driven synchronously by this fixture. Its completed
	// jobs are not part of the restart scenario; acknowledge their setup outbox
	// before starting the isolated worker, without changing production dispatch.
	if _, err := f.db.Exec("UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE processed_at IS NULL"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	release := prepareGenerationRelease(t, f, source, "任务重启后继续当前固定改稿的审查")
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	report, err := reviews.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: f.branch, ReviewerBindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviews.Start(ctx, f.actor, f.project, report.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = reviews.Control(ctx, f.actor, f.project, report.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	namespace := "semantic-review-test-" + service.NewID()
	start := func() *platformworker.Runtime {
		t.Helper()
		// Fresh service composition consumes only durable state on each worker start.
		gen := &service.Generations{IP: f.svc.IP, Adapter: f.svc.Generations.Adapter}
		runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: f.svc.IP}, os.Getenv("STUDIO_TEMPORAL"), namespace, platformworker.RuntimeServices{Generations: gen})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	runtime := start()
	closed := false
	defer func() {
		if !closed {
			runtime.Close()
		}
	}()
	temporal, err := client.DialContext(ctx, client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer temporal.Close()
	workflowID := "semantic-review-" + report.ID
	defer temporal.TerminateWorkflow(context.Background(), workflowID, "", "test cleanup")
	before := protocol.calls.Load()
	paused := false
	for i := 0; i < 80; i++ {
		value, e := temporal.QueryWorkflow(ctx, workflowID, "", "state")
		var state string
		if e == nil && value.Get(&state) == nil && state == "PAUSED" {
			paused = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !paused || protocol.calls.Load() != before {
		t.Fatal("pause did not survive durable delivery")
	}
	runtime.Close()
	closed = true
	if _, err = reviews.Control(ctx, f.actor, f.project, report.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	// Replay the durable outbox entry as if acknowledgement was lost at shutdown.
	if _, err = f.db.Exec("UPDATE outbox SET processed_at=NULL WHERE kind='semantic.review.start' AND resource_id=?", report.ID); err != nil {
		t.Fatal(err)
	}
	replacement := start()
	defer replacement.Close()
	if err = replacement.WakeSemanticReview(ctx, report.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = temporal.GetWorkflow(ctx, workflowID, "").Get(ctx, &state); err != nil || state != "COMPLETED" {
		t.Fatal("review did not resume", state, err)
	}
	completed, err := reviews.Read(ctx, f.actor, f.project, report.ID)
	if err != nil || completed.Verdict != "PASS" || protocol.calls.Load() != before+1 {
		t.Fatal("restarted review duplicated dispatch or lost report", completed.State, protocol.calls.Load()-before, err)
	}
	var children int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM generation_tasks WHERE JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.semantic_review_id'))=?", report.ID).Scan(&children); err != nil || children != 1 {
		t.Fatal("outbox replay duplicated child", children, err)
	}
	// A genuinely completed FAILED workflow cannot be revived by reusing its
	// rejected workflow identity. The user must prepare a fresh fixed request.
	protocol.transform = func(r *domain.SemanticReviewReport) { r.Coverage = []string{} }
	failed, err := reviews.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: f.branch, ReviewerBindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviews.Start(ctx, f.actor, f.project, failed.ID); err != nil {
		t.Fatal(err)
	}
	failedWorkflow := "semantic-review-" + failed.ID
	defer temporal.TerminateWorkflow(context.Background(), failedWorkflow, "", "test cleanup")
	// Outbox delivery is asynchronous; wait until the workflow exists.
	for i := 0; i < 100; i++ {
		if _, e := temporal.DescribeWorkflowExecution(ctx, failedWorkflow, ""); e == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = temporal.GetWorkflow(ctx, failedWorkflow, "").Get(ctx, &state); err != nil || state != "FAILED" {
		t.Fatal("expected terminal failed workflow", state, err)
	}
	protocol.transform = nil
	if _, err = reviews.Start(ctx, f.actor, f.project, failed.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("completed failed workflow restarted", err)
	}
	if read, e := reviews.Read(ctx, f.actor, f.project, failed.ID); e != nil || read.State != "FAILED" {
		t.Fatal("failed workflow left terminal state", read.State, e)
	}
}
