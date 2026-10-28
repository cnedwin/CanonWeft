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
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

func attachChapterResearchFixture(t *testing.T, ip *service.IP, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, draft service.NovelChapterDraft) service.NovelChapterDraft {
	t.Helper()
	ctx := context.Background()
	source, err := ip.CreateResource(ctx, actor, book.ProjectID, service.ResourceInput{BranchID: book.BranchID, Name: "纸张修复专业参考", Kind: "note", OperationID: "chapter-reference-resource"})
	if err != nil {
		t.Fatal(err)
	}
	policy := &domain.KnowledgePolicy{Space: "working", Visibility: "author"}
	release, err := ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: draft.Document.IPHead, OperationID: "chapter-research-source", Updates: []domain.ResourceUpdate{{ResourceID: source.ID, Text: "纸张修复参考：湿纸不能用力揭开，应先核对纸张状态与保存条件。仅用于虚构场景构思，不替代专业修复操作。", Policy: policy}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelChapterResearch{Writer: &service.NovelChapterWriter{Generations: &service.Generations{IP: ip}, Knowledge: knowledge}}
	result, err := svc.Search(ctx, actor.ID, book.ID, draft.Resource.ID, service.ChapterResearchSearchInput{Query: "湿纸 修复", Revision: draft.Revision, Hash: draft.Hash})
	if err != nil || len(result.Items) == 0 {
		t.Fatal("chapter research retrieval", err)
	}
	var pin domain.DependencyRef
	for _, item := range result.Items {
		if item.Source.ID == source.ID && item.CanUse {
			pin = item.Source
		}
	}
	if pin.ID == "" {
		t.Fatal("adopted working reference unavailable")
	}
	chapterResearchBrowser(t, ip, book, "select")
	if os.Getenv("STUDIO_CHAPTER_RESEARCH_BROWSER_NODE") != "" {
		draft, err = (&service.NovelChapters{IP: ip}).Read(ctx, actor.ID, book.ID, draft.Resource.ID)
		if err != nil || !draft.Ready || len(draft.Document.References) != 1 {
			t.Fatal("browser reference not saved", err, draft.Reason)
		}
	}
	draft.Document.References = []novel.ChapterReference{{Source: pin, Quote: "湿纸不能用力揭开", Use: "林舟发现湿信后谨慎查看", Era: "需核对故事年代的纸张材质", Region: "旧城", Limitations: "仅支持避免粗暴揭纸，不确定具体修复工艺"}}
	chapters := &service.NovelChapters{IP: ip}
	draft, err = chapters.Save(ctx, actor.ID, book.ID, draft.Resource.ID, draft)
	if err != nil || !draft.Ready {
		t.Fatal("save chapter reference", err, draft.Reason)
	}
	bad := draft
	bad.Document.References = append([]novel.ChapterReference(nil), draft.Document.References...)
	bad.Document.References[0].Quote = "可直接用火烘干"
	bad, err = chapters.Save(ctx, actor.ID, book.ID, draft.Resource.ID, bad)
	if err != nil || bad.Ready {
		t.Fatal("fabricated quote became usable or draft was lost", err)
	}
	bad.Document.References = draft.Document.References
	draft, err = chapters.Save(ctx, actor.ID, book.ID, draft.Resource.ID, bad)
	if err != nil || !draft.Ready {
		t.Fatal("repair citation", err, draft.Reason)
	}
	return draft
}

func chapterResearchBrowser(t *testing.T, ip *service.IP, book service.NovelBook, phase string) {
	t.Helper()
	node := os.Getenv("STUDIO_CHAPTER_RESEARCH_BROWSER_NODE")
	if node == "" {
		return
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	app := httpapi.New(ip.Workspace.DB, origin)
	app.Static("/", filepath.Join(web, "dist"))
	server.Config.Handler = app
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-chapter-research-browser.cjs"), origin, book.Setup.Title, phase).CombinedOutput()
	if err != nil {
		t.Fatalf("chapter references browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertChapterResearchChange(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapterID string) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	r, err := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || !d.Ready || len(d.Document.References) != 1 {
		t.Fatal("missing reference before change", err, d.Reason)
	}
	writer := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	input := service.NovelChapterWritingInput{Revision: d.Revision, Hash: d.Hash, Notes: "核对本章专业细节", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "chapter-before-reference-change"}
	g, err := writer.Prepare(ctx, actor.ID, book.ID, r.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	originalBudget, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	enabled := originalBudget
	enabled.Enabled = true
	if _, err = budgets.Configure(ctx, actor.ID, enabled); err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, err := budgets.Read(ctx, actor.ID)
		if err != nil {
			t.Error(err)
			return
		}
		current.Enabled = originalBudget.Enabled
		if _, err := budgets.Configure(ctx, actor.ID, current); err != nil {
			t.Error(err)
		}
	}()
	batch := &service.NovelBatch{Writer: writer}
	run, err := batch.Prepare(ctx, actor.ID, book.ID, service.NovelBatchInput{ChapterIDs: []string{chapterID}, Platform: "fanqie", Audience: "female", Notes: "参考资料用于细节", Currency: "USD", PriceID: price.ID, BudgetLimitMicros: 1000000, MaxOutputTokens: 2048, OperationID: "reference-batch"})
	if err != nil {
		t.Fatal("reference batch prepare", err)
	}
	recipes := &service.RecipeExecutions{Generations: gen}
	if _, err = recipes.Start(ctx, actor, book.ProjectID, run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal("reference batch advance", err)
	}
	run, err = recipes.Read(ctx, actor, book.ProjectID, run.ID)
	if err != nil || len(run.Steps) != 1 || run.Steps[0].GenerationID == "" {
		t.Fatal("reference batch child missing", err)
	}
	content := `{"title":"雨夜来信","text":"林舟没有用力揭开湿纸。","notes":["修复细节仍需专业核对。"]}`
	response, _ := json.Marshal(map[string]any{"id": "reference-batch-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	originalAdapter := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = originalAdapter }()
	if state, err := gen.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" {
		t.Fatal("reference batch execution", state, err)
	}
	if !strings.Contains(transport.payload, "reference_materials") || !strings.Contains(transport.payload, "湿纸不能用力揭开") {
		t.Fatal("batch omitted actual references")
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "COMPLETED" {
		t.Fatal("reference batch completion", state, err)
	}
	source, err := gen.IP.ReadResource(ctx, actor, book.ProjectID, d.Document.References[0].Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := gen.IP.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: d.Document.IPHead, OperationID: "correct-chapter-reference", Updates: []domain.ResourceUpdate{{ResourceID: source.ID, ExpectedHead: source.Head, Text: "修订的纸张修复参考：湿纸不能用力揭开，应先核对材质与破损程度，具体措施必须由专业人员判断。", Policy: source.Policy}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.IP.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.IP.PublishRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := gen.IP.Workspace.DB.QueryRowContext(ctx, "SELECT id FROM outbox WHERE project_id=? AND resource_id=? AND kind='release.published'", book.ProjectID, release.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	propagated, err := gen.IP.PropagatePublication(ctx, eventID)
	if err != nil || propagated.State != "COMPLETED" {
		t.Fatal("reference change propagation", err)
	}
	plan, err := gen.IP.ReadChangePlan(ctx, actor, book.ProjectID, propagated.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	controlFound, bodyFound := false, false
	for _, item := range plan.Items {
		controlFound = controlFound || item.Node.ID == r.ID
		bodyFound = bodyFound || item.Node.ID == d.Document.BodyID
	}
	if !controlFound || !bodyFound {
		t.Fatal("changed source did not reach chapter control and prose", plan.Items)
	}
	stale, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || stale.Ready || stale.Document.Text != d.Document.Text || !strings.Contains(stale.Reason, "引用资料已换版") {
		t.Fatal("changed source did not invalidate reference", err, stale.Reason)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, g.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed source dispatched stale model", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("source change caused extra call")
	}
	stale.Document.Text += "作者继续保留并修改原稿。"
	stale, err = chapters.Save(ctx, actor.ID, book.ID, r.ID, stale)
	if err != nil || stale.Ready || !strings.HasSuffix(stale.Document.Text, "作者继续保留并修改原稿。") {
		t.Fatal("stale reference blocked manual text preservation", err)
	}
	chapterResearchBrowser(t, gen.IP, book, "refresh")
}
