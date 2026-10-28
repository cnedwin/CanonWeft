package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func assertNovelBookReview(t *testing.T, b *browser, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard) {
	t.Helper()
	ctx := context.Background()
	chapterSvc := &service.NovelChapters{IP: gen.IP}
	catalog, e := (&service.NovelBatch{Writer: &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}}).Chapters(ctx, actor.ID, book.ID)
	if e != nil || len(catalog) != 10 {
		t.Fatal("review catalog", len(catalog), e)
	}
	ids := []string{}
	resources := []string{}
	for i, c := range catalog {
		r, e := chapterSvc.Open(ctx, actor.ID, book.ID, c.ID)
		if e != nil {
			t.Fatal(e)
		}
		d, e := chapterSvc.Read(ctx, actor.ID, book.ID, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		d.Document.Text = fmt.Sprintf("第%d段调查，林舟走入旧城。", i+1)
		if i == 0 {
			d.Document.Text += "林舟把铜钥匙交给阿岚。"
		}
		if i == 8 {
			d.Document.Text += "林舟从口袋取出了铜钥匙。"
		}
		if _, e = chapterSvc.Save(ctx, actor.ID, book.ID, r.ID, d); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, c.ID)
		resources = append(resources, r.ID)
	}
	svc := &service.NovelBookReviews{Generations: gen, Knowledge: knowledge}
	in := service.NovelBookReviewInput{Source: "draft", Notes: "重点检查铜钥匙的持有人和跨章因果", PriceID: price.ID, Currency: "USD", BudgetLimitMicros: 1000000, MaxOutputTokens: 4096, OperationID: "book-review-test"}
	p, e := svc.Preview(ctx, actor.ID, book.ID, in)
	if e != nil || p.Included != 10 || p.Passes != 2 {
		t.Fatal("review preview", p, e)
	}
	in.PreviewHash = p.Hash
	run, e := svc.Prepare(ctx, actor.ID, book.ID, in)
	if e != nil || len(run.Steps) != 2 {
		t.Fatal("prepare review", e)
	}
	replay, e := svc.Prepare(ctx, actor.ID, book.ID, in)
	if e != nil || replay.ID != run.ID {
		t.Fatal("review receipt", e)
	}
	recipes := &service.RecipeExecutions{Generations: gen}
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	budget, e := budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = true
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
	defer func() {
		current, e := budgets.Read(ctx, actor.ID)
		if e != nil {
			t.Fatal(e)
		}
		current.Enabled = false
		if _, e := budgets.Configure(ctx, actor.ID, current); e != nil {
			t.Fatal(e)
		}
	}()
	run, e = recipes.Start(ctx, actor, book.ProjectID, run.ID, run.Revision)
	if e != nil {
		t.Fatal("start review", e)
	}
	transport := &generationTransport{}
	original := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = original }()
	read := func() service.RecipeExecution {
		r, e := recipes.Read(ctx, actor, book.ProjectID, run.ID)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	execute := func(ordinal int, forged bool) service.RecipeStepRun {
		if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "RUNNING" {
			current := read()
			for _, s := range current.Steps {
				if s.Active && s.Ordinal == ordinal {
					_, contextErr := knowledge.ReadContext(ctx, actor, book.ProjectID, book.BranchID, s.Frozen.Input.ContextID)
					manifest, manifestErr := (&service.Capabilities{IP: gen.IP}).Manifest(ctx, actor, book.ProjectID, s.Frozen.Agent.ID)
					t.Log("review diagnostic", ordinal, s.State, s.GenerationID != "", contextErr, manifestErr, manifest.Findings)
				}
			}
			t.Fatal("advance review", ordinal, state, e, current.Reason)
		}
		run = read()
		var step service.RecipeStepRun
		for _, s := range run.Steps {
			if s.Active && s.Ordinal == ordinal {
				step = s
			}
		}
		var input struct {
			ChapterIDs []string `json:"chapter_ids"`
		}
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil || step.GenerationID == "" {
			t.Fatal("missing stage")
		}
		result := novel.BookReviewResult{Coverage: input.ChapterIDs, Checks: map[string]string{}, Findings: []novel.BookReviewFinding{}, Carry: []novel.BookReviewCarry{{Key: "copper-key", Category: "causality", Summary: "铜钥匙已交给阿岚，后续需要核对归还过程。", Status: "open", Evidence: []novel.BookReviewEvidence{{ChapterID: ids[0], Quote: "林舟把铜钥匙交给阿岚。"}}}}, Limits: []string{"仅依据本次正文，人物动机判断仍需作者复核。"}}
		for _, category := range novel.BookReviewCategories {
			result.Checks[category] = "no_issue_found"
		}
		if ordinal == 1 {
			result.Checks["causality"] = "issues_found"
			quote := "林舟从口袋取出了铜钥匙。"
			if forged {
				quote = "从未存在的伪造引文"
			}
			result.Findings = []novel.BookReviewFinding{{ID: "key-return", Category: "causality", Severity: "major", Title: "铜钥匙缺少归还过程", Explanation: "前文交出，后文直接取出，期间未交代归还。", Suggestion: "核对后续安排，在适当位置补足归还过程。", Evidence: []novel.BookReviewEvidence{{ChapterID: ids[0], Quote: "林舟把铜钥匙交给阿岚。"}, {ChapterID: ids[8], Quote: quote}}}}
		}
		raw, _ := json.Marshal(result)
		response, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("review-local-%d-%t", ordinal, forged), "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(raw)}}}, "usage": map[string]int{"prompt_tokens": 300, "completion_tokens": 200}})
		transport.response = string(response)
		if state, e := gen.Execute(ctx, step.GenerationID); e != nil || state != "SUCCEEDED" {
			t.Fatal("execute review", state, e)
		}
		if ordinal == 1 && !strings.Contains(transport.payload, "铜钥匙已交给阿岚") {
			t.Fatal("previous carry missing from actual prompt")
		}
		if _, e = recipes.Advance(ctx, run.ID); e != nil {
			t.Fatal("finish review stage", e)
		}
		return step
	}
	execute(0, false)
	run = read()
	run, e = recipes.Control(ctx, actor, book.ProjectID, run.ID, "pause", run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "PAUSED" {
		t.Fatal(state, e)
	}
	run, e = recipes.Control(ctx, actor, book.ProjectID, run.ID, "resume", run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	invalid := execute(1, true)
	run = read()
	if run.State != "FAILED" {
		t.Fatal("forged quotation accepted", run.State)
	}
	run, e = recipes.Retry(ctx, actor, book.ProjectID, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "review-retry", Step: invalid.Frozen.Input})
	if e != nil {
		t.Fatal("retry invalid review", e)
	}
	execute(1, false)
	if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "COMPLETED" {
		t.Fatal("review completion", state, e, read().Reason)
	}
	report, e := svc.Report(ctx, actor.ID, book.ID, run.ID)
	if e != nil || report.Stale || len(report.Passes) != 2 || report.Covered != 10 || len(report.Passes[1].Result.Findings) != 1 {
		t.Fatal("complete report", report, e)
	}
	b.request("GET", "/api/novels/"+book.ID+"/book-reviews/"+run.ID, nil, 200)
	other := b.request("POST", "/api/members", map[string]any{"username": "review-other", "password": "review-other-password", "role": "editor"}, 201)
	otherID := other["id"].(string)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+otherID, map[string]any{"role": "editor"}, 200)
	if _, e = svc.Report(ctx, otherID, book.ID, run.ID); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("private report leaked", e)
	}
	// A prepared task must also stop if any later chapter changes before start.
	definition := run.Steps[0].Frozen.Agent.Definition
	definition.Instructions += "\n作者自定义：优先解释关键物品的归属变化。"
	custom, e := (&service.Capabilities{IP: gen.IP}).Save(ctx, actor, book.ProjectID, service.CapabilitySave{OperationID: "review-custom-agent", ExpectedHead: run.Steps[0].Frozen.Agent.ID, Definition: definition})
	if e != nil {
		t.Fatal("edit critic agent", e)
	}
	in.AgentVersionID = custom.ID
	in.OperationID = "review-stale-start"
	pending, e := svc.Prepare(ctx, actor.ID, book.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if pending.Steps[0].Frozen.Agent.ID != custom.ID {
		t.Fatal("custom review agent not pinned")
	}
	d, e := chapterSvc.Read(ctx, actor.ID, book.ID, resources[9])
	if e != nil {
		t.Fatal(e)
	}
	d.Document.Text += "阿岚随后归还了铜钥匙。"
	if _, e = chapterSvc.Save(ctx, actor.ID, book.ID, resources[9], d); e != nil {
		t.Fatal(e)
	}
	if _, e = recipes.Start(ctx, actor, book.ProjectID, pending.ID, pending.Revision); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed chapter started", e)
	}
	report, e = svc.Report(ctx, actor.ID, book.ID, run.ID)
	if e != nil || !report.Stale || len(report.Passes) != 2 {
		t.Fatal("historical report lost after edit", e)
	}
	if transport.calls.Load() != 3 {
		t.Fatal("unexpected model calls", transport.calls.Load())
	}
	assertBookReviewBrowser(t, gen.IP, book.Setup.Title, run.ID)
}
