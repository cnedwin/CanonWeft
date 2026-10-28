package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	platformworker "novelstudio/internal/worker"
)

// Exercise persisted event grants against the real application database. Only
// the upstream public-source transport is substituted by the parent fixture.
func assertResearchBackgroundCheckpoints(t *testing.T, db *sql.DB, svc *service.NovelResearchExecutions, actor domain.Actor, book service.NovelBook, note service.ResearchDraft, transport *researchTransport) []string {
	t.Helper()
	ctx := context.Background()
	ids := []string{}
	create := func(operation string) service.ResearchExecution {
		t.Helper()
		run, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, service.ResearchExecutionCreate{Revision: note.Revision, Hash: note.Hash, Language: "zh", OperationID: operation, AutoCollectLimit: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ResourceID)
		return run
	}
	start := func(run service.ResearchExecution) service.ResearchExecution {
		t.Helper()
		out, err := svc.StartBackground(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
		if err != nil || !out.BackgroundQueued {
			t.Fatal("background enqueue", err)
		}
		return out
	}
	read := func(id string) service.ResearchExecution {
		t.Helper()
		out, err := svc.Read(ctx, actor.ID, book.ID, note.ResourceID, id)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	event := func(run service.ResearchExecution) string {
		t.Helper()
		var id string
		if err := db.QueryRow("SELECT id FROM outbox WHERE kind='research.step' AND resource_id=? AND CAST(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.revision')) AS UNSIGNED)=?", run.ResourceID, run.Revision).Scan(&id); err != nil {
			var payloads string
			_ = db.QueryRow("SELECT COALESCE(GROUP_CONCAT(payload),'none') FROM outbox WHERE kind='research.step' AND resource_id=?", run.ResourceID).Scan(&payloads)
			t.Fatalf("event pin revision=%d stage=%s queued=%t payloads=%s: %v", run.Revision, run.Document.Stage, run.BackgroundQueued, payloads, err)
		}
		return id
	}
	advance := func(id, expected string) {
		t.Helper()
		state, err := svc.AdvanceBackground(ctx, id)
		if err != nil || state != expected {
			t.Fatalf("advance: %s, want %s: %v", state, expected, err)
		}
	}
	before := transport.calls
	run := start(create("background-checkpoints"))
	first := event(run)
	if _, err := svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision); !errors.Is(err, domain.ErrConflict) || transport.calls != before {
		t.Fatal("manual endpoint bypassed background grant", err)
	}
	again := start(run)
	if again.Revision != run.Revision || event(again) != first || transport.calls != before {
		t.Fatal("enqueue replay fetched or changed grant")
	}
	advance(first, "QUEUED")
	advance(first, "CANCELLED")
	run = read(run.ResourceID)
	if !run.Document.Queries[0].Done || run.Document.Queries[1].Done || transport.calls != before+1 {
		t.Fatal("checkpoint replay duplicated search")
	}
	second := event(run)
	// An outbox dispatcher may acknowledge after the activity already saved
	// its result. That acknowledgement must not consume the next event.
	if _, err := db.Exec("UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=?", first); err != nil {
		t.Fatal(err)
	}
	var nextPending int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE id=? AND processed_at IS NULL", second).Scan(&nextPending); err != nil || nextPending != 1 {
		t.Fatal("stale acknowledgement consumed next event", err)
	}
	if _, err := svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "pause"); err != nil {
		t.Fatal(err)
	}
	advance(second, "SUPERSEDED")
	if transport.calls != before+1 {
		t.Fatal("paused event fetched")
	}
	run, err := svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "resume")
	if err != nil || !run.BackgroundQueued {
		t.Fatal("resume grant", err)
	}
	advance(event(run), "QUEUED")
	run = read(run.ResourceID)
	advance(event(run), "COMPLETED")
	run = read(run.ResourceID)
	if run.Document.Stage != "complete" || run.BackgroundQueued || transport.calls != before+3 || run.Document.Queries[0].Hits[0].ImportID != run.Document.Queries[1].Hits[0].ImportID {
		t.Fatal("background collection or deduplication failed")
	}

	cancelled := start(create("background-cancel"))
	cancelEvent := event(cancelled)
	if _, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, cancelled.ResourceID, "cancel"); err != nil {
		t.Fatal(err)
	}
	before = transport.calls
	advance(cancelEvent, "SUPERSEDED")
	if transport.calls != before {
		t.Fatal("cancelled event fetched")
	}

	// Public draft editing remains available, but editing the document must not
	// accidentally authorize execution under a grant for its previous revision.
	edited := start(create("background-edited"))
	editedEvent := event(edited)
	draft, err := svc.Sources.IP.ReadResourceDraft(ctx, actor, book.ProjectID, edited.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	edited.Document.Queries[0].Query = "作者修改后的检索词"
	raw, err := json.Marshal(edited.Document)
	if err != nil {
		t.Fatal(err)
	}
	draft.Text = string(raw)
	if _, err = svc.Sources.IP.SaveResourceDraft(ctx, actor, book.ProjectID, edited.ResourceID, draft); err != nil {
		t.Fatal(err)
	}
	advance(editedEvent, "SUPERSEDED")
	if transport.calls != before || read(edited.ResourceID).BackgroundQueued {
		t.Fatal("private edit reused old network grant")
	}
	unchanged := start(create("background-revision-only"))
	unchangedEvent := event(unchanged)
	draft, err = svc.Sources.IP.ReadResourceDraft(ctx, actor, book.ProjectID, unchanged.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Sources.IP.SaveResourceDraft(ctx, actor, book.ProjectID, unchanged.ResourceID, draft); err != nil {
		t.Fatal(err)
	}
	unchanged = read(unchanged.ResourceID)
	if unchanged.BackgroundQueued {
		t.Fatal("same-text new revision reused an old grant")
	}
	advance(unchangedEvent, "SUPERSEDED")
	unchanged = start(unchanged)
	if _, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, unchanged.ResourceID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if transport.calls != before {
		t.Fatal("revision-only change fetched without a fresh grant")
	}
	denied := start(create("background-disabled-author"))
	deniedEvent := event(denied)
	if _, err = db.Exec("UPDATE users SET enabled=0 WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	state, deniedErr := svc.AdvanceBackground(ctx, deniedEvent)
	if _, err = db.Exec("UPDATE users SET enabled=1 WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	if deniedErr != nil || state != "CANCELLED" || transport.calls != before {
		t.Fatal("disabled author reached the source or left an endless retry", state, deniedErr)
	}
	if _, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, denied.ResourceID, "cancel"); err != nil {
		t.Fatal(err)
	}

	originalProvider := svc.Sources.Provider
	svc.Sources.Provider = &retryResearchProvider{inner: originalProvider}
	failed := start(create("background-failure"))
	failureEvent := event(failed)
	advance(failureEvent, "WAITING_RETRY")
	failed = read(failed.ResourceID)
	if failed.BackgroundQueued || failed.Document.Error == "" || failed.Document.Queries[0].Done {
		t.Fatal("failure did not stop automatic continuation")
	}
	advance(failureEvent, "SUPERSEDED")
	failed = start(failed)
	advance(event(failed), "QUEUED")
	svc.Sources.Provider = originalProvider
	if _, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, failed.ResourceID, "cancel"); err != nil {
		t.Fatal(err)
	}
	t.Log("Background grants: enqueue/replay, atomic continuation, pause/resume/cancel, private-edit invalidation, error stop and explicit retry verified.")
	return ids
}

func assertResearchBackgroundRuntime(t *testing.T, svc *service.NovelResearchExecutions, actor domain.Actor, book service.NovelBook, note service.ResearchDraft, sources ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source, language := "", "zh"
	if len(sources) > 0 {
		source = sources[0]
	}
	if source == "europe_pmc" {
		language = "en"
	}
	run, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, service.ResearchExecutionCreate{Provider: source, Revision: note.Revision, Hash: note.Hash, Language: language, OperationID: "real-background-restart"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.StartBackground(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "research-" + service.NewID()
	start := func() *platformworker.Runtime {
		t.Helper()
		runtime, e := platformworker.StartRuntime(ctx, &service.Tasks{IP: svc.Sources.IP}, os.Getenv("STUDIO_TEMPORAL"), namespace, platformworker.RuntimeServices{ResearchProvider: svc.Sources.Provider})
		if e != nil {
			t.Fatal(e)
		}
		return runtime
	}
	wait := func(stage string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			run, err = svc.Read(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Document.Error != "" {
				t.Fatal("background source failure", run.Document.Error)
			}
			if run.Document.Stage == stage {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("background stage=%s, wanted %s", run.Document.Stage, stage)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	runtime := start()
	wait("selection")
	runtime.Close()
	// With no worker running, the explicit selection must persist its event.
	selected := []service.ResearchHitSelection{}
	for qi := range run.Document.Queries {
		if len(run.Document.Queries[qi].Hits) == 0 {
			t.Fatal("background query has no selectable result")
		}
		selected = append(selected, service.ResearchHitSelection{QueryIndex: qi, PageID: run.Document.Queries[qi].Hits[0].PageID})
	}
	run, err = svc.Select(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, service.ResearchExecutionSelection{Revision: run.Revision, Items: selected})
	if err != nil || !run.BackgroundQueued || run.Document.Stage != "collect" {
		t.Fatal("stopped worker lost pending collection", err)
	}
	runtime = start()
	defer runtime.Close()
	wait("complete")
	if run.Document.Queries[0].Hits[0].ImportID == "" || (len(run.Document.Queries) > 1 && run.Document.Queries[0].Hits[0].PageID == run.Document.Queries[1].Hits[0].PageID && run.Document.Queries[0].Hits[0].ImportID != run.Document.Queries[1].Hits[0].ImportID) || run.Document.Provider != source {
		t.Fatal("restarted worker lost collection receipt")
	}
	t.Log("Real Temporal executed search checkpoints and, after worker stop/restart, consumed the persisted selection/collection event without a browser.")
	return run.ResourceID
}
