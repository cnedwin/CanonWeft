package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

// Follow the published chapter with a distinct confirmed chapter. This checks
// the actual model request, not only a direct call to the memory compiler.
func assertNextChapterMemory(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, previous, next string) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	first, err := chapters.Read(ctx, actor.ID, book.ID, previous)
	if err != nil || first.BaseHead == "" {
		t.Fatal("previous chapter unpublished", err)
	}
	resource, err := chapters.Open(ctx, actor.ID, book.ID, next)
	if err != nil || resource.ID == previous {
		t.Fatal("next chapter identity", err)
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, resource.ID)
	if err != nil || !draft.Ready || draft.Document.StoryTime <= first.Document.StoryTime {
		t.Fatal("next chapter position", err)
	}
	draft.Document.Text = "第二天，林舟来到旧城。"
	draft, err = chapters.Save(ctx, actor.ID, book.ID, resource.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	agent := chapterEditableAgent(t, gen.IP, actor, book, resource.ID, "writer")
	task, err := writer.Prepare(ctx, actor.ID, book.ID, resource.ID, service.NovelChapterWritingInput{AgentVersionID: agent, Revision: draft.Revision, Hash: draft.Hash, Notes: "根据上一章停在门外的经历，继续追查纸条", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "next-chapter-memory"})
	if err != nil {
		t.Fatal("next chapter request", err)
	}
	pack, err := knowledge.ReadContext(ctx, actor, book.ProjectID, book.BranchID, task.Frozen.ContextID)
	if err != nil || pack.Memory == nil || len(pack.Memory.Items) != 1 {
		t.Fatal("next chapter lost published memory", err)
	}
	memory := pack.Memory.Items[0].Item
	if memory.Visibility != "public" || len(memory.Sources) != 2 {
		t.Fatal("next chapter memory provenance")
	}
	if !strings.Contains(memory.Text, "林舟在门外停了下来") {
		t.Fatal("wrong previous chapter memory")
	}
	var hidden string
	if err = gen.IP.Workspace.DB.QueryRow("SELECT id FROM memory_items WHERE project_id=? AND JSON_UNQUOTE(JSON_EXTRACT(document,'$.visibility'))='author'", book.ProjectID).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	output := `{"title":"旧城","text":"第二天，林舟来到旧城。他记得昨夜在门外停步时看见的信纸。","notes":[]}`
	response, _ := json.Marshal(map[string]any{"id": "next-chapter-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": output}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	original := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = original }()
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal("start next chapter", err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute next chapter", state, e)
	}
	if transport.calls.Load() != 1 || !strings.Contains(transport.payload, memory.ID) || strings.Contains(transport.payload, hidden) {
		t.Fatal("actual writer request lost public memory or leaked hidden memory")
	}
	candidate, err := writer.Suggestion(ctx, actor.ID, book.ID, resource.ID, task.ID)
	if err != nil || !strings.Contains(candidate.Document.Text, "昨夜") {
		t.Fatal("next chapter candidate", err)
	}
	unchanged, err := chapters.Read(ctx, actor.ID, book.ID, resource.ID)
	if err != nil || unchanged.Hash != draft.Hash {
		t.Fatal("next chapter auto-adopted", err)
	}
	candidate.Revision, candidate.BaseHead = draft.Revision, draft.BaseHead
	if _, err = chapters.Save(ctx, actor.ID, book.ID, resource.ID, candidate); err != nil {
		t.Fatal("adopt next chapter private prose", err)
	}
	t.Log("Second confirmed chapter used first published chapter memory in actual custom writer request; author memory excluded, explicit private adoption, local transport only")
	assertChapterRepublication(t, gen, knowledge, actor, book, price, previous, resource.ID, memory.ID)
}
