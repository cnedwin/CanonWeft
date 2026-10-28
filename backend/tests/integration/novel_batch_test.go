package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func assertNovelBatch(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard) {
	t.Helper()
	ctx := context.Background()
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	budget, e := budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal("read test budget", e)
	}
	budget.Enabled = true
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal("enable isolated budget", e)
	}
	defer func() {
		b, e := budgets.Read(ctx, actor.ID)
		if e == nil {
			b.Enabled = false
			_, e = budgets.Configure(ctx, actor.ID, b)
		}
		if e != nil {
			t.Fatal(e)
		}
	}()
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	batch := &service.NovelBatch{Writer: writer}
	chapters, err := batch.Chapters(ctx, actor.ID, book.ID)
	if err != nil || len(chapters) != 10 {
		t.Fatal("batch chapters", len(chapters), err)
	}
	in := service.NovelBatchInput{Platform: "fanqie", Audience: "female", Focus: "悬疑成长，保留主角主动选择", Notes: "生成连贯正文", PriceID: price.ID, Currency: "USD", BudgetLimitMicros: 1000000, MaxOutputTokens: 2048, OperationID: "ten-chapter-batch"}
	for _, ch := range chapters {
		in.ChapterIDs = append(in.ChapterIDs, ch.ID)
	}
	run, err := batch.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil || len(run.Steps) != 10 {
		t.Fatal("prepare ten chapters", err)
	}
	replay, err := batch.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil || replay.ID != run.ID {
		t.Fatal("batch replay", err)
	}
	recipes := &service.RecipeExecutions{Generations: gen}
	run, err = recipes.Start(ctx, actor, book.ProjectID, run.ID, run.Revision)
	if err != nil {
		t.Fatal("start batch", err)
	}
	transport := &generationTransport{}
	original := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = original }()
	read := func() service.RecipeExecution {
		t.Helper()
		r, e := recipes.Read(ctx, actor, book.ProjectID, run.ID)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	for i := 0; i < 10; i++ {
		if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "RUNNING" {
			t.Fatalf("prepare chapter %d: %s %v %+v", i+1, state, e, read().Reason)
		}
		run = read()
		step := run.Steps[i]
		if step.GenerationID == "" {
			t.Fatal("missing child", i)
		}
		if i == 3 {
			run, err = recipes.Control(ctx, actor, book.ProjectID, run.ID, "pause", run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "PAUSED" {
				t.Fatal("pause", state, e)
			}
			run, err = recipes.Control(ctx, actor, book.ProjectID, run.ID, "resume", run.Revision)
			if err != nil {
				t.Fatal(err)
			}
		}
		output := fmt.Sprintf(`{"title":"连写第%d章","text":"第%d章，林舟循着药铺线索向前调查。","notes":["候选累计状态：第1章留下的纸条仍由林舟保管，旁人尚不知情。"]}`, i+1, i+1)
		response, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("batch-local-%d", i), "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": output}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
		transport.response = string(response)
		if state, e := gen.Execute(ctx, step.GenerationID); e != nil || state != "SUCCEEDED" {
			t.Fatal("execute chapter", i+1, state, e)
		}
		if i > 0 && (!strings.Contains(transport.payload, fmt.Sprintf("第%d章，林舟", i)) || !strings.Contains(transport.payload, "纸条仍由林舟保管")) {
			t.Fatal("prior candidate state missing", i+1)
		}
		if _, e := recipes.Advance(ctx, run.ID); e != nil {
			t.Fatal("complete stage", i+1, e)
		}
	}
	if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "COMPLETED" {
		t.Fatal("complete batch", state, e)
	}
	run = read()
	if transport.calls.Load() != 10 {
		t.Fatal("duplicated calls", transport.calls.Load())
	}
	chapterSvc := &service.NovelChapters{IP: gen.IP}
	for i, step := range run.Steps {
		ref := step.Frozen.Input.NovelChapter
		before, e := chapterSvc.Read(ctx, actor.ID, book.ID, ref.ResourceID)
		if e != nil || before.Hash != ref.Hash {
			t.Fatal("batch overwrote prose", i, e)
		}
		candidate, e := writer.Suggestion(ctx, actor.ID, book.ID, ref.ResourceID, step.GenerationID)
		if e != nil {
			t.Fatal("batch candidate", i, e)
		}
		if _, e = chapterSvc.Save(ctx, actor.ID, book.ID, ref.ResourceID, candidate); e != nil {
			t.Fatal("adopt candidate", i, e)
		}
		if i == 0 {
			first, e := chapterSvc.Read(ctx, actor.ID, book.ID, ref.ResourceID)
			if e != nil {
				t.Fatal(e)
			}
			originalText := first.Document.Text
			first.Document.Text += "作者改变了关键线索。"
			first, e = chapterSvc.Save(ctx, actor.ID, book.ID, ref.ResourceID, first)
			if e != nil {
				t.Fatal(e)
			}
			next := run.Steps[1]
			if _, e = writer.Suggestion(ctx, actor.ID, book.ID, next.Frozen.Input.NovelChapter.ResourceID, next.GenerationID); e == nil {
				t.Fatal("changed predecessor did not invalidate downstream candidate")
			}
			first.Document.Text = originalText
			if _, e = chapterSvc.Save(ctx, actor.ID, book.ID, ref.ResourceID, first); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Editing an adopted predecessor must invalidate downstream unexamined output.
	firstRef := run.Steps[0].Frozen.Input.NovelChapter
	first, e := chapterSvc.Read(ctx, actor.ID, book.ID, firstRef.ResourceID)
	if e != nil {
		t.Fatal(e)
	}
	first.Document.Text += "作者改变了关键线索。"
	if _, e = chapterSvc.Save(ctx, actor.ID, book.ID, firstRef.ResourceID, first); e != nil {
		t.Fatal(e)
	}
	t.Log("Ten sequential chapter requests, candidate-state handoff, pause/resume, idempotent preparation and explicit adoption passed; no real provider calls")
}
