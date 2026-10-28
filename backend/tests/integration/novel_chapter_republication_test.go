package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

func assertChapterRepublication(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapter, downstream, oldMemory string) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	next, err := chapters.Read(ctx, actor.ID, book.ID, downstream)
	if err != nil {
		t.Fatal(err)
	}
	request := service.NovelChapterWritingInput{Revision: next.Revision, Hash: next.Hash, Notes: "继续追查纸条", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "before-upstream-republication"}
	pending, err := writer.Prepare(ctx, actor.ID, book.ID, downstream, request)
	if err != nil {
		t.Fatal("prepare downstream before revision", err)
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	previousRelease := draft.BaseHead
	draft.Document.Text += "药铺的灯熄灭了。"
	draft, err = chapters.Save(ctx, actor.ID, book.ID, chapter, draft)
	if err != nil {
		t.Fatal("revise published chapter", err)
	}
	output := novel.LedgerOutput{Summary: "药铺熄灯", Entries: []novel.LedgerEntry{{Kind: "event", Context: "reality", SubjectName: "药铺", Predicate: "熄灯", Statement: "药铺的灯熄灭了", StoryTime: draft.Document.StoryTime, KnownFrom: draft.Document.StoryTime, Visibility: "public", KnownBy: []string{}, Quotes: []string{"药铺的灯熄灭了。"}}}, Notes: []string{}}
	content, _ := json.Marshal(output)
	response, _ := json.Marshal(map[string]any{"id": "revised-ledger-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	protocol := &generationTransport{response: string(response)}
	original := gen.Adapter
	gen.Adapter = models.New(protocol)
	defer func() { gen.Adapter = original }()
	agent := chapterEditableAgent(t, gen.IP, actor, book, chapter, "bookkeeper")
	task, err := writer.PrepareBookkeeper(ctx, actor.ID, book.ID, chapter, service.NovelChapterWritingInput{AgentVersionID: agent, Revision: draft.Revision, Hash: draft.Hash, Notes: "根据改稿重新提取实际事件", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "revised-chapter-ledger"})
	if err != nil {
		t.Fatal("revised bookkeeper prepare", err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("revised bookkeeper execute", state, e)
	}
	ledger, err := writer.LedgerSuggestion(ctx, actor.ID, book.ID, chapter, task.ID)
	if err != nil {
		t.Fatal("revised ledger candidate", err)
	}
	ledger, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, ledger)
	if err != nil {
		t.Fatal("revised ledger adoption", err)
	}
	release, err := chapters.PrepareRelease(ctx, actor.ID, book.ID, chapter, service.NovelChapterReleaseInput{Revision: draft.Revision, Hash: draft.Hash, LedgerRevision: ledger.Revision, OperationID: "chapter-republication"})
	if err != nil || release.ID == previousRelease {
		t.Fatal("revised release", err)
	}
	if _, err = chapters.ConfirmRelease(ctx, actor.ID, book.ID, chapter, release.ID, release.Hash, true); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("republication bypassed independent review", err)
	}
	bindings, err := (&service.Qualifications{IP: gen.IP, Generations: gen}).Bindings(ctx, actor, book.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	binding := ""
	for _, candidate := range bindings {
		if candidate.TaskKind == "shared.semantic-review" && candidate.Valid && candidate.Current {
			binding = candidate.ID
		}
	}
	if binding == "" {
		t.Fatal("existing critic qualification missing")
	}
	critic := &chapterReviewTransport{}
	gen.Adapter = models.New(critic)
	reviews := &service.SemanticReviews{Generations: gen, Knowledge: knowledge}
	review, err := reviews.Prepare(ctx, actor, book.ProjectID, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: book.BranchID, ReviewerBindingID: binding, PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil || review.State != "PREPARED" {
		t.Fatal("republication review prepare", review.State, review.Reason, err)
	}
	reviewTask, err := gen.Read(ctx, actor, book.ProjectID, review.GenerationID)
	if err != nil || reviewTask.Frozen.ContextID == "" {
		t.Fatal("review skipped current memory", err)
	}
	reviewContext, err := knowledge.ReadContext(ctx, actor, book.ProjectID, book.BranchID, reviewTask.Frozen.ContextID)
	if err != nil || reviewContext.Memory == nil || reviewContext.Input.Purpose != "critic" || reviewContext.Input.StoryTime != draft.Document.StoryTime {
		t.Fatal("review memory scope", err)
	}
	if _, err = reviews.Start(ctx, actor, book.ProjectID, review.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := reviews.Advance(ctx, review.ID); e != nil || state != "RUNNING" {
		t.Fatal(state, e)
	}
	if _, err = gen.Execute(ctx, review.GenerationID); err != nil {
		t.Fatal(err)
	}
	if state, e := reviews.Advance(ctx, review.ID); e != nil || state != "COMPLETED" {
		t.Fatal(state, e)
	}
	if _, err = reviews.Select(ctx, actor, book.ProjectID, review.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.IP.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IndexJobs{Knowledge: knowledge}
	job, err := jobs.Create(ctx, actor, book.ProjectID, book.BranchID, service.IndexJobInput{OperationID: service.NewID(), ReleaseID: release.ID})
	if err != nil {
		t.Fatal(err)
	}
	if state, e := jobs.Execute(ctx, job.ID); e != nil || state != "READY" {
		t.Fatal(state, e)
	}
	published, err := chapters.ConfirmRelease(ctx, actor.ID, book.ID, chapter, release.ID, release.Hash, true)
	if err != nil || published.Status != "PUBLISHED" {
		t.Fatal("republication", err)
	}
	if _, err = chapters.ConfirmRelease(ctx, actor.ID, book.ID, chapter, release.ID, release.Hash, true); err != nil {
		t.Fatal("republication replay", err)
	}
	var count int
	if err = gen.IP.Workspace.DB.QueryRow("SELECT COUNT(*) FROM memory_items WHERE project_id=?", book.ProjectID).Scan(&count); err != nil || count != 3 {
		t.Fatal("historical memory lost or replay duplicated", count, err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, pending.ID); !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("old downstream context was not rejected as stale", err)
	}
	unchanged, err := chapters.Read(ctx, actor.ID, book.ID, downstream)
	if err != nil || unchanged.Hash != next.Hash {
		t.Fatal("upstream revision overwrote downstream prose", err)
	}
	request.OperationID = "after-upstream-republication"
	refreshed, err := writer.Prepare(ctx, actor.ID, book.ID, downstream, request)
	if err != nil {
		t.Fatal("refresh downstream memory", err)
	}
	pack, err := knowledge.ReadContext(ctx, actor, book.ProjectID, book.BranchID, refreshed.Frozen.ContextID)
	if err != nil || pack.Memory == nil || len(pack.Memory.Items) != 1 || pack.Memory.Items[0].Item.ID == oldMemory || !strings.Contains(pack.Memory.Items[0].Item.Text, "药铺的灯熄灭了") {
		t.Fatal("republication memory replacement", err)
	}
	raw, err := gen.IP.Blobs.ReadObject(ctx, book.ProjectID, refreshed.Frozen.InputHash)
	if err != nil || strings.Contains(string(raw), oldMemory) || !strings.Contains(string(raw), pack.Memory.Items[0].Item.ID) {
		t.Fatal("refreshed writer prompt retained obsolete memory", err)
	}
	if protocol.calls.Load() != 1 || critic.calls != 1 || !strings.Contains(critic.lastPrompt, oldMemory) {
		t.Fatal("republication duplicated provider calls")
	}
	t.Log("Published chapter revised, independently re-bookkept/reviewed and republished; historical memory retained, downstream stale request rejected, fresh prompt uses new memory without altering existing prose")
}
