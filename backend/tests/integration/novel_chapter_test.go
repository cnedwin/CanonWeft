package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

// Reuse the real ordered world/book/volume/detail fixture; this is not a second
// set of hand-seeded planning records which could bypass the planning gates.
func assertNovelChapterWriting(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapterID, nextChapterID string, publish bool) (service.Generation, service.Generation) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	r, err := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil {
		t.Fatal("open chapter", err)
	}
	same, err := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil || same.ID != r.ID {
		t.Fatal("duplicate chapter identity", err)
	}
	if _, err = chapters.Open(ctx, actor.ID, book.ID, service.NewID()); err == nil {
		t.Fatal("opened chapter without confirmed detail")
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || !draft.Ready {
		t.Fatal("chapter not ready", draft.Reason, err)
	}
	draft.Document.Text = "林舟在门外停了下来。"
	draft, err = chapters.Save(ctx, actor.ID, book.ID, r.ID, draft)
	if err != nil || !draft.Ready || draft.Revision != 1 {
		t.Fatal("manual chapter draft", err)
	}
	if t.Name() == "TestNovelChapterResearchReferences" {
		draft = attachChapterResearchFixture(t, gen.IP, knowledge, actor, book, draft)
	}
	content := `{"title":"雨夜来信","text":"林舟在门外停了下来。门缝里露出一角湿透的信纸。","notes":["只完成开场，余下场景仍需续写。"]}`
	response, _ := json.Marshal(map[string]any{"id": "chapter-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	original := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = original }()
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	input := service.NovelChapterWritingInput{Revision: draft.Revision, Hash: draft.Hash, Notes: "保留原稿，续写发现纸条的场景", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "chapter-writer-once"}
	if !publish {
		input.AgentVersionID = chapterEditableAgent(t, gen.IP, actor, book, r.ID, "writer")
	}
	task, err := writer.Prepare(ctx, actor.ID, book.ID, r.ID, input)
	if err != nil || task.Frozen.NovelChapter == nil {
		t.Fatal("prepare chapter writer", err)
	}
	if t.Name() == "TestNovelChapterWritingCardHTTP" {
		assertCapturedChapterWritingCard(t, gen, actor, book, r.ID, task)
	}
	sameTask, err := writer.Prepare(ctx, actor.ID, book.ID, r.ID, input)
	if err != nil || sameTask.ID != task.ID {
		t.Fatal("writer retry changed task", err)
	}
	changed := input
	changed.Notes = "不同要求"
	if _, err = writer.Prepare(ctx, actor.ID, book.ID, r.ID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("idempotency accepted changed chapter request", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("prepare called model")
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err == nil {
		t.Fatal("disabled budget allowed execution")
	}
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	if _, err = budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		budget, e := budgets.Read(ctx, actor.ID)
		if e != nil {
			t.Error(e)
			return
		}
		budget.Enabled = false
		if _, e := budgets.Configure(ctx, actor.ID, budget); e != nil {
			t.Error(e)
		}
	}()
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal("start chapter", err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute chapter", state, e)
	}
	if transport.calls.Load() != 1 || !strings.Contains(transport.payload, "林舟在门外停了下来") || !strings.Contains(transport.payload, "本章已确认的作者规划") {
		t.Fatal("writer lost actual draft or confirmed planning")
	}
	if !publish && (task.Frozen.Capability == nil || task.Frozen.Capability.AgentVersionID != input.AgentVersionID || task.Frozen.Capability.Mode != "TRIAL" || !strings.Contains(transport.payload, "作者定制")) {
		t.Fatal("custom writer method was not executed")
	}
	if len(draft.Document.References) > 0 && (!strings.Contains(transport.payload, "reference_materials") || !strings.Contains(transport.payload, "湿纸不能用力揭开") || !strings.Contains(transport.payload, "需核对故事年代的纸张材质")) {
		t.Fatal("writer lost selected reference text or scope")
	}
	candidate, err := writer.Suggestion(ctx, actor.ID, book.ID, r.ID, task.ID)
	if err != nil || len(candidate.Notes) != 1 || candidate.Document.WriterGenerationID != task.ID {
		t.Fatal("chapter proposal", err)
	}
	unchanged, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || unchanged.Document.Text != draft.Document.Text || unchanged.Revision != draft.Revision {
		t.Fatal("preview changed private draft", err)
	}
	var adopted service.NovelChapterDraft
	if node := os.Getenv("STUDIO_CHAPTER_BROWSER_NODE"); node != "" {
		web, e := filepath.Abs("../../../web")
		if e != nil {
			t.Fatal(e)
		}
		server := httptest.NewUnstartedServer(nil)
		origin := "http://" + server.Listener.Addr().String()
		app := httpapi.New(gen.IP.Workspace.DB, origin)
		app.Static("/", filepath.Join(web, "dist"))
		server.Config.Handler = app
		server.Start()
		defer server.Close()
		browserContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, e := exec.CommandContext(browserContext, node, filepath.Join(web, "tests", "novel-chapter-browser.cjs"), origin, task.ID, book.Setup.Title).CombinedOutput()
		if e != nil {
			t.Fatalf("chapter browser: %v\n%s", e, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		adopted, err = chapters.Read(ctx, actor.ID, book.ID, r.ID)
	} else {
		adopted, err = chapters.Save(ctx, actor.ID, book.ID, r.ID, candidate)
	}
	if err != nil || !adopted.Ready || adopted.Document.Text == draft.Document.Text {
		t.Fatal("explicit chapter adoption", err)
	}
	if _, err = chapters.Save(ctx, actor.ID, book.ID, r.ID, draft); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old editor overwrote new prose", err)
	}
	if _, err = writer.Suggestion(ctx, actor.ID, book.ID, r.ID, task.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale model output remained current", err)
	}
	input.Revision, input.Hash, input.OperationID = adopted.Revision, adopted.Hash, "chapter-before-manual-edit"
	beforeEdit, err := writer.Prepare(ctx, actor.ID, book.ID, r.ID, input)
	if err != nil {
		t.Fatal("prepare before manual edit", err)
	}
	adopted.Document.Text += "他没有立刻伸手。"
	adopted, err = chapters.Save(ctx, actor.ID, book.ID, r.ID, adopted)
	if err != nil || adopted.Document.WriterGenerationID != task.ID {
		t.Fatal("manual revision lost origin", err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, beforeEdit.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale private prose dispatched model call", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("stale draft dispatched an extra call")
	}
	basis, err := chapters.Basis(ctx, actor.ID, book.ID, r.ID, adopted.Revision)
	if err != nil || basis.Document.Text != adopted.Document.Text || basis.Revision != adopted.Revision {
		t.Fatal("basis refresh altered text or version", err)
	}
	unchanged, err = chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || unchanged.Hash != adopted.Hash {
		t.Fatal("basis preview wrote the draft", err)
	}
	for _, id := range []string{r.ID, adopted.Document.BodyID} {
		_, err = gen.IP.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: adopted.Document.IPHead, OperationID: "chapter-publish-bypass-" + id, Updates: []domain.ResourceUpdate{{ResourceID: id, Text: adopted.Document.Text, Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}}})
		if !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("generic publication did not enforce chapter review gate", err)
		}
	}
	bookkeeper := assertNovelChapterLedger(t, gen, knowledge, actor, book, price, r.ID, publish)
	if publish {
		assertNextChapterMemory(t, gen, knowledge, actor, book, price, r.ID, nextChapterID)
	}
	return task, bookkeeper
}
