package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func assertNovelPolish(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapter string) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	r, err := chapters.Open(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := chapters.InstallPolish(ctx, actor.ID, book.ID, r.ID)
	if err != nil || len(installed.Versions) != 2 {
		t.Fatal("polish preset", err)
	}
	replayed, err := chapters.InstallPolish(ctx, actor.ID, book.ID, r.ID)
	if err != nil || replayed.RootID != installed.RootID {
		t.Fatal("polish installation replay", err)
	}
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	in := service.NovelChapterWritingInput{Method: "fanqie-polish", Revision: draft.Revision, Hash: draft.Hash, Notes: "仅修复空泛表达，保留线索和人物知情范围", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "fanqie-polish-proof"}
	if _, err = writer.PrepareBookkeeper(ctx, actor.ID, book.ID, r.ID, in); err == nil {
		t.Fatal("polish entered bookkeeper")
	}
	task, err := writer.Prepare(ctx, actor.ID, book.ID, r.ID, in)
	if err != nil {
		t.Fatal("prepare polish", err)
	}
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = true
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	defer func() {
		b, e := budgets.Read(ctx, actor.ID)
		if e == nil {
			b.Enabled = false
			_, e = budgets.Configure(ctx, actor.ID, b)
		}
		if e != nil {
			t.Error(e)
		}
	}()
	output := map[string]any{"title": draft.Document.Title, "text": draft.Document.Text + "雨水顺着信纸的折痕滴落。", "notes": []string{"表达｜信纸｜补充与既有场景一致的可感动作｜人物身份与线索保留；新增描写待作者采用"}}
	raw, _ := json.Marshal(output)
	response, _ := json.Marshal(map[string]any{"id": "polish-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(raw)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	prior := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = prior }()
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal("start polish", err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute polish", state, e)
	}
	if !strings.Contains(transport.payload, "番茄投稿前润稿") || !strings.Contains(transport.payload, "每个问题必须对应原稿") {
		t.Fatal("specialized skill absent from actual request")
	}
	candidate, err := writer.Suggestion(ctx, actor.ID, book.ID, r.ID, task.ID)
	if err != nil || len(candidate.Notes) != 1 {
		t.Fatal("polish evidence", err)
	}
	current, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || current.Hash != draft.Hash {
		t.Fatal("polish auto-overwrote source", err)
	}
	t.Log("Dedicated Fanqie polish Skill installed idempotently and executed with original source pins; candidate and evidence returned without adoption; local transport only")
}
