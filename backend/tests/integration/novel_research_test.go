package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestNovelResearchFactsWorkflow(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "research-facts-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	transport := &generationTransport{}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelResearch{Generations: gen, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	branchRoot := "/api/projects/" + book.ProjectID + "/branches/" + book.BranchID
	head := b.request("GET", branchRoot, nil, 200)["head"].(string)
	source := resource(b, book.ProjectID, book.BranchID, "港口原始资料")
	sourceRelease := prepareRelease(b, book.ProjectID, book.BranchID, head, "", "research-source", []map[string]any{{"resource_id": source, "text": "原始资料：市舶机构办理商舶事务。具体制度随年代和地域不同。", "policy": domain.KnowledgePolicy{Space: "research", Visibility: "author"}}})
	releaseAdopt(b, book.ProjectID, sourceRelease)
	releasePublish(b, book.ProjectID, sourceRelease)
	create := service.ResearchCreate{OperationID: "research-notebook", Document: novel.ResearchNotebook{Title: "泉州商舶考据", Question: "商船靠岸的专业流程有哪些", Genre: "历史", Era: "南宋", Region: "泉州", ChapterNeed: "主角第一次靠岸", SourceIDs: []string{source}}}
	d, err := svc.Create(ctx, actor.ID, book.ID, create)
	if err != nil {
		t.Fatal("create", err)
	}
	again, err := svc.Create(ctx, actor.ID, book.ID, create)
	if err != nil || again.ResourceID != d.ResourceID {
		t.Fatal("create replay", err)
	}
	pin := d.Document.Sources[0]
	proposal := novel.ResearchProposal{SearchPlan: []novel.ResearchQuery{{Query: "宋代 市舶司 流程", Purpose: "核对口岸具体流程", PreferredSource: "博物馆与历史原始记录"}}, Cards: []novel.ResearchFact{{Claim: "存在办理商舶事务的机构", Status: "supported", Sources: []novel.ResearchCitation{{SourceID: source, URL: pin.URL, Locator: pin.Locator, CapturedAt: pin.CapturedAt, Evidence: "市舶机构办理商舶事务"}}, EraScope: "南宋范围仍需核实", RegionScope: "泉州范围仍需核实", Limitations: "仅支持一般职责，不支持具体税额", WritingUse: "安排主角与港口管理人员接触", ReviewAfter: "新增一手史料时复核"}}, Conflicts: []string{}, Gaps: []string{"尚无具体税率的一手证据"}, SceneChecks: []string{"不要把一般职责写成固定税额"}}
	body, _ := json.Marshal(proposal)
	response, _ := json.Marshal(map[string]any{"id": "research-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(body)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport.response = string(response)
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "题材研究隔离测试", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/researcher", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	in := service.NovelPlanningAssistInput{Revision: d.Revision, Hash: d.Hash, Notes: "先核对已有材料，未知细节列缺口", PriceID: price.ID, MaxOutputTokens: 4096, OperationID: "research-call"}
	task, err := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil {
		t.Fatal("prepare", err)
	}
	replay, err := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil || replay.ID != task.ID {
		t.Fatal("request replay", err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err == nil {
		t.Fatal("paid gate bypassed")
	}
	budgets := &service.Budgets{Workspace: ip.Workspace}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = true
	budget.Currency = "USD"
	budget.Limit = 1000000
	budget.PerCall = 100000
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute", state, e)
	}
	for _, text := range []string{"题材专业知识研究", "市舶机构办理商舶事务", "南宋", "泉州", pin.URL} {
		if !strings.Contains(transport.payload, text) {
			t.Fatal("missing real method/context", text)
		}
	}
	result, err := svc.Suggestion(ctx, actor.ID, book.ID, d.ResourceID, task.ID)
	if err != nil {
		t.Fatal("suggestion", err)
	}
	if len(result.Document.Cards) != 1 || result.Document.Cards[0].Approved {
		t.Fatal("automatic approval")
	}
	forged := result
	forged.Document.Cards = append([]novel.ResearchFact(nil), result.Document.Cards...)
	forged.Document.Cards[0].Sources = append([]novel.ResearchCitation(nil), result.Document.Cards[0].Sources...)
	forged.Document.Cards[0].Sources[0].Evidence = "免除所有关税"
	if _, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, forged); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("fabricated evidence saved", err)
	}
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, result)
	if err != nil {
		t.Fatal("save proposal", err)
	}
	if _, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, result); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("lost update", err)
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = false
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STUDIO_RESEARCH_FACTS_BROWSER_NODE") != "" {
		assertResearchFactsBrowser(t, ip, book.Setup.Title)
	}
	d, err = svc.Read(ctx, actor.ID, book.ID, d.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	d.Document.Cards[0].Approved = true
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil {
		t.Fatal(err)
	}
	release, err := svc.PrepareRelease(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash, OperationID: "facts-candidate"})
	if err != nil {
		t.Fatal("prepare facts release", err)
	}
	ref := release.Manifest.Artifacts[0]
	if ref.Policy.Space != "working" || ref.Origin == nil || !ref.Origin.ReviewRequired || len(ref.Dependencies.Inputs) != 1 || ref.Dependencies.Inputs[0].Source.ID != source {
		t.Fatal("review/source provenance missing")
	}
	if _, err = ip.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err == nil {
		t.Fatal("unreviewed research candidate adopted")
	}
	replayed, err := svc.PrepareRelease(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash, OperationID: "facts-candidate"})
	if err != nil || replayed.ID != release.ID {
		t.Fatal("candidate replay", err)
	}
	create.OperationID = "unrelated-research-notebook"
	create.Document.Title = "无关研究稿"
	other, err := svc.Create(ctx, actor.ID, book.ID, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: head, OperationID: "wrong-research-origin", Updates: []domain.ResourceUpdate{{ResourceID: other.ResourceID, Text: "无关研究稿", Origin: &domain.ArtifactOriginInput{GenerationID: task.ID}}}}); err == nil {
		t.Fatal("historical origin accepted for unrelated notebook")
	}
	retained := d.Document.Cards[0]
	retained.ID = "retained-quote-card"
	retained.Claim = "制度随年代和地域不同"
	retained.Sources = append([]novel.ResearchCitation(nil), retained.Sources...)
	retained.Sources[0].Evidence = "具体制度随年代和地域不同"
	d.Document.Cards = append(d.Document.Cards, retained)
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil {
		t.Fatal("save retained quote card", err)
	}
	in.Revision = d.Revision
	in.Hash = d.Hash
	in.OperationID = "research-stale"
	stale, err := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil {
		t.Fatal(err)
	}
	updated := prepareRelease(b, book.ProjectID, book.BranchID, head, "", "research-source-update", []map[string]any{{"resource_id": source, "expected_head": sourceRelease, "text": "修正资料：具体制度随年代和地域不同。职责与年代范围需要重新核对。", "policy": domain.KnowledgePolicy{Space: "research", Visibility: "author"}}})
	releaseAdopt(b, book.ProjectID, updated)
	releasePublish(b, book.ProjectID, updated)
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = true
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, stale.ID); err == nil {
		t.Fatal("source changed but stale call started")
	}
	if _, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale citations saved", err)
	}
	oldDraft := d
	if os.Getenv("STUDIO_RESEARCH_FACTS_BROWSER_NODE") != "" {
		assertResearchFactsBrowser(t, ip, book.Setup.Title, "refresh")
		d, err = svc.Read(ctx, actor.ID, book.ID, d.ResourceID)
	} else {
		d, err = svc.RefreshSources(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash})
	}
	if err != nil {
		t.Fatal("refresh research sources", err)
	}
	card := d.Document.Cards[0]
	if d.ResourceID != oldDraft.ResourceID || d.Document.GenerationID != task.ID || d.Document.Sources[0].Revision != updated || card.Approved || card.Status != "unverified" || len(card.Sources) != 0 || len(card.StaleSources) != 1 || card.StaleSources[0].Evidence != "市舶机构办理商舶事务" {
		t.Fatal("source refresh lost edit/history or kept unsupported approval")
	}
	retained = d.Document.Cards[1]
	if retained.Approved || retained.Status != "unverified" || len(retained.Sources) != 1 || len(retained.StaleSources) != 1 || retained.Sources[0].URL != d.Document.Sources[0].URL || retained.StaleSources[0].URL != pin.URL {
		t.Fatal("surviving quotation lost old provenance or silently stayed approved")
	}
	if _, err = svc.RefreshSources(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: oldDraft.Revision, Hash: oldDraft.Hash}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale refresh overwrote current draft", err)
	}
	unchanged, err := svc.RefreshSources(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash})
	if err != nil || unchanged.Revision != d.Revision {
		t.Fatal("unchanged sources caused a new draft", err)
	}
	d.Document.Cards[0].Approved = true
	if _, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("unresolved obsolete evidence approved", err)
	}
	d.Document.Cards[0].Approved = false
	d = assertResearchWithdrawal(t, svc, actor, book, d, source, head, in)
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected remote calls")
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("inventory", err)
	}
	archive := filepath.Join(t.TempDir(), "research-facts.studio")
	if _, err = backup.Create(ctx, db, ip.Blobs, archive); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, e := db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); e != nil {
			t.Error(e)
		}
	})
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(t.TempDir(), "restored"), Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var before, after string
	if err = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", d.ResourceID, actor.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", d.ResourceID, actor.ID).Scan(&after); err != nil || before != after || !strings.Contains(after, "市舶机构办理商舶事务") {
		t.Fatal("facts/provenance restore", err)
	}
	t.Log("Research Agent/Skill and pinned sources entered local model transport; exact quotations, private edit/concurrency, independent review provenance, changed-source blocking and backup restore verified.")
}

func assertResearchFactsBrowser(t *testing.T, ip *service.IP, title string, phase ...string) {
	t.Helper()
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append([]string{filepath.Join(web, "tests", "novel-research-browser.cjs"), origin, title}, phase...)
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_RESEARCH_FACTS_BROWSER_NODE"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("research facts browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
