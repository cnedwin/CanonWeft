package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNovelCharacterSources(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, e := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if e != nil {
		t.Fatal(e)
	}
	book, e := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "source-book", AcceptSettings: true})
	if e != nil {
		t.Fatal(e)
	}
	ip := propagationService(t, db)
	transport := &generationTransport{}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, e := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if e != nil {
		t.Fatal(e)
	}
	knowledge := &service.Knowledge{IP: ip, Embedder: embedder}
	svc := &service.NovelCharacters{Generations: gen, Knowledge: knowledge}
	publishSource := func(id, head, text, op string) string {
		t.Helper()
		branch := b.request("GET", "/api/projects/"+book.ProjectID+"/branches/"+book.BranchID, nil, 200)
		release := prepareRelease(b, book.ProjectID, book.BranchID, branch["head"].(string), "", op, []map[string]any{{"resource_id": id, "expected_head": head, "text": text, "policy": map[string]any{"space": "working", "visibility": "author"}}})
		releaseAdopt(b, book.ProjectID, release)
		releasePublish(b, book.ProjectID, release)
		return release
	}
	source := resource(b, book.ProjectID, book.BranchID, "角色专业资料")
	sourceText := "完整资料开头：铜钥匙不能打开银锁。" + strings.Repeat("专业背景必须核对。", 80) + "全文结尾标记：人物右手拇指有旧伤。"
	sourceHead := publishSource(source, "", sourceText, "source-first")
	d, e := svc.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "source-characters", Notes: "根据附件塑造两名人物", Count: 2, SourceIDs: []string{source}})
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("STUDIO_CHARACTER_SOURCES_BROWSER_NODE") != "" {
		startNovelBrowserIngest(t, db)
		assertCharacterSourcesBrowser(t, ip, book.Setup.Title, d.ResourceID)
		d, e = svc.Read(ctx, actor.ID, book.ID, d.ResourceID)
		if e != nil || len(d.Document.SourceIDs) != 2 {
			t.Fatal("browser source selection", e, d.Document.SourceIDs)
		}
	}
	// A source belonging to another project must not be selected even if its ID is known.
	other := createProject(b, "无关资料项目", "foreign-source-project")
	otherBranch := branch(b, other, "正史")
	foreign := resource(b, other, otherBranch, "外项目资料")
	bad := d
	bad.Document.SourceIDs = []string{foreign}
	if _, e = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, bad); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("foreign source accepted", e)
	}
	bad.Document.SourceIDs = []string{source, source}
	if _, e = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, bad); !errors.Is(e, domain.ErrValidation) {
		t.Fatal("duplicate source accepted", e)
	}
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "角色资料隔离测试", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/character_designer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, e := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if e != nil {
		t.Fatal(e)
	}
	input := service.NovelPlanningAssistInput{Revision: d.Revision, Hash: d.Hash, Notes: d.Document.Notes, PriceID: price.ID, MaxOutputTokens: 4096, OperationID: "source-single"}
	single, e := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, input)
	if e != nil {
		t.Fatal("single prepare", e)
	}
	recipes := &service.RecipeExecutions{Generations: gen}
	batch, e := svc.PrepareBatch(ctx, actor.ID, book.ID, d.ResourceID, service.CharacterBatchInput{Revision: d.Revision, Hash: d.Hash, Notes: d.Document.Notes, Size: 1, PriceID: price.ID, Currency: "USD", BudgetLimitMicros: 100000, MaxOutputTokens: 4096, OperationID: "source-batch"})
	if e != nil {
		t.Fatal("batch prepare", e)
	}
	checkContext := func(id string) {
		t.Helper()
		var raw []byte
		if e := db.QueryRow("SELECT payload FROM context_packages WHERE id=?", id).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var pack domain.ContextPackage
		if json.Unmarshal(raw, &pack) != nil {
			t.Fatal("context decode")
		}
		for _, id := range d.Document.SourceIDs {
			found := false
			for _, item := range pack.Required {
				if item.Dependency.Source.ID == id {
					found = true
					if id == source && item.Text != sourceText {
						t.Fatal("required text truncated")
					}
				}
			}
			if !found {
				t.Fatal("selected source omitted", id)
			}
		}
	}
	checkContext(single.Frozen.ContextID)
	checkContext(batch.Steps[0].Frozen.Input.ContextID)
	budgets := &service.Budgets{Workspace: entry.Workspace}
	if _, e = budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	p := map[string]any{"entities": d.Document.Candidate.Entities, "assertions": d.Document.Candidate.Assertions, "explanation": "依据全文提出人物候选", "warnings": []string{}}
	content, _ := json.Marshal(p)
	response, _ := json.Marshal(map[string]any{"id": "source-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 300, "completion_tokens": 100}})
	transport.response = string(response)
	if _, e = gen.Start(ctx, actor, book.ProjectID, single.ID); e != nil {
		t.Fatal(e)
	}
	if state, e := gen.Execute(ctx, single.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	if !strings.Contains(transport.payload, "完整资料开头") || !strings.Contains(transport.payload, "全文结尾标记") {
		t.Fatal("full source absent from actual model request")
	}
	if os.Getenv("STUDIO_CHARACTER_SOURCES_BROWSER_NODE") != "" && (!strings.Contains(transport.payload, "码头学徒怕水") || !strings.Contains(transport.payload, "作者补充：只作为创作参考")) {
		t.Fatal("uploaded and edited material absent from model request")
	}
	input.OperationID = "source-pending"
	pending, e := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, input)
	if e != nil {
		t.Fatal(e)
	}
	publishSource(source, sourceHead, sourceText+"作者已修订这份资料。", "source-update")
	if _, e = gen.Start(ctx, actor, book.ProjectID, pending.ID); e == nil {
		t.Fatal("single stale source accepted")
	}
	if _, e = recipes.Start(ctx, actor, book.ProjectID, batch.ID, batch.Revision); e == nil {
		t.Fatal("batch stale source accepted")
	}
	if _, e = svc.Suggestion(ctx, actor.ID, book.ID, d.ResourceID, single.ID); e == nil {
		t.Fatal("stale result adoptable")
	}
	large := resource(b, book.ProjectID, book.BranchID, "超长角色资料")
	publishSource(large, "", strings.Repeat("x", 70000), "source-large")
	d.Document.SourceIDs = []string{large}
	d, e = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if e != nil {
		t.Fatal(e)
	}
	input.Revision = d.Revision
	input.Hash = d.Hash
	input.OperationID = "source-overflow"
	if _, e = svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, input); !errors.Is(e, domain.ErrContextBudget) {
		t.Fatal("oversized source silently trimmed", e)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected provider calls", transport.calls.Load())
	}
	budget, e := budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = false
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
}
