package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"novelstudio/internal/novel"
	"novelstudio/internal/research"
	"novelstudio/internal/service"
)

func TestNovelResearchBackgroundInterruptedLease(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "interrupted-research-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	researcher := &service.NovelResearch{Generations: &service.Generations{IP: ip}}
	note, err := researcher.Create(ctx, actor.ID, book.ID, service.ResearchCreate{OperationID: "interrupted-notebook", Document: novel.ResearchNotebook{Title: "港口制度", Question: "商船怎样进港", Genre: "历史", Era: "宋", Region: "泉州", ChapterNeed: "进港场景", ResearchProposal: novel.ResearchProposal{SearchPlan: []novel.ResearchQuery{{Query: "宋代 市舶司", Purpose: "机构职责", PreferredSource: "一手史料"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &researchTransport{}
	gate := &gatedResearchProvider{inner: research.NewWikipedia(transport), entered: make(chan struct{}), proceed: make(chan struct{})}
	svc := &service.NovelResearchExecutions{Research: researcher, Sources: &service.ResearchSources{IP: ip, Provider: gate}}
	run, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, service.ResearchExecutionCreate{Revision: note.Revision, Hash: note.Hash, Language: "zh", OperationID: "interrupted-search"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.StartBackground(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var event string
	if err = db.QueryRow("SELECT id FROM outbox WHERE kind='research.step' AND resource_id=?", run.ResourceID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, e := svc.AdvanceBackground(requestContext, event); finished <- e }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background request never entered")
	}
	cancel()
	if err = <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("interruption did not leave the checkpoint for retry", err)
	}
	run, err = svc.Read(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID)
	if err != nil || run.Document.Attempt == "" || run.Document.Queries[0].Done {
		t.Fatal("interrupted lease was lost", err)
	}
	before := transport.calls
	if _, err = svc.AdvanceBackground(ctx, event); err == nil || transport.calls != before {
		t.Fatal("active lease replay fetched", err)
	}
	// Use the real production lease clock; do not rewrite private JSON or its
	// event pin to manufacture an expired checkpoint.
	remaining := time.Until(time.Unix(run.Document.LeaseUntil, 0)) + 100*time.Millisecond
	if remaining > time.Minute+time.Second {
		t.Fatal("unexpected research lease duration")
	}
	t.Log("Interrupted request preserved its event pin; waiting for the real one-minute lease to expire.")
	if remaining > 0 {
		time.Sleep(remaining)
	}
	state, err := svc.AdvanceBackground(ctx, event)
	if err != nil || state != "WAITING_SELECTION" {
		t.Fatal("expired checkpoint could not resume", state, err)
	}
	run, err = svc.Read(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID)
	if err != nil || !run.Document.Queries[0].Done || run.Document.Attempt != "" || run.Document.Error != "" {
		t.Fatal("recovered result did not persist", err)
	}
	t.Log("Interrupted background request recovered from its unchanged event ID after the real lease expired; no duplicate in-flight fetch.")
}
