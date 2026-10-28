package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/novel"
	"novelstudio/internal/research"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

const officialTestURL = "https://www.gov.cn/zhengce/test-article.htm"

type officialBatchTransport struct {
	names         catalogResearchTransport
	officialCalls int
}

func (p *officialBatchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "www.gov.cn" {
		response, err := p.names.RoundTrip(r)
		if err == nil && r.URL.Query().Get("ids") == "Q900000003" {
			raw, e := io.ReadAll(response.Body)
			response.Body.Close()
			if e != nil {
				return nil, e
			}
			response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), "Elowen", "Neris")))
		}
		return response, err
	}
	p.officialCalls++
	text, kind := "User-agent: *\nAllow: /zhengce/\n", "text/plain"
	if r.URL.Path != "/robots.txt" {
		kind = "text/html; charset=utf-8"
		text = `<html><head><meta charset="utf-8"><meta name="PubDate" content="2024-01-01"><title>网站</title></head><body><header>HEADER_CANARY</header><main><h1>制度原文</h1><div id="UCAP-CONTENT"><p>` + strings.Repeat("手续须核对当时制度和适用地区。", 30) + `</p><p>FULLTEXT_CANARY</p></div></main></body></html>`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(strings.NewReader(text)), Request: r}, nil
}

func TestOfficialReferencesAndBatchNameSources(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "official-batch-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	transport := &officialBatchTransport{}
	provider := research.NewCatalog(transport)
	sources := &service.ResearchSources{IP: ip, Provider: provider}
	characters := &service.NovelCharacters{Generations: &service.Generations{IP: ip}}
	originals := []service.SourceImport{}
	ids := []string{}
	for i, page := range []int64{900000001, 900000002, 900000003} {
		source, e := sources.Collect(ctx, actor, book.ProjectID, book.BranchID, service.ResearchCollectInput{Provider: research.NamesSource, Language: "en", PageID: page, Query: "name material", OperationID: "batch-name-" + string(rune('a'+i))})
		if e != nil {
			t.Fatal(e)
		}
		originals = append(originals, source)
		ids = append(ids, source.ID)
	}
	library, err := characters.OpenNameCatalog(ctx, actor.ID, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := characters.PreviewNameSource(ctx, actor.ID, book.ID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	preview.Document.Records[len(preview.Document.Records)-1].Label = "Elowen kept"
	library, err = characters.SaveNameCatalog(ctx, actor.ID, book.ID, preview)
	if err != nil {
		t.Fatal(err)
	}
	preview, err = characters.PreviewNameSources(ctx, actor.ID, book.ID, ids)
	if err != nil || len(preview.Document.Records) != 503 || preview.Document.Records[500].Label != "Elowen kept" {
		t.Fatal("batch merge lost prior edits", err)
	}
	current, err := characters.NameCatalog(ctx, actor.ID, book.ID)
	if err != nil || current.Hash != library.Hash || current.Revision != library.Revision {
		t.Fatal("batch preview wrote the catalog", err)
	}
	if _, err = characters.PreviewNameSources(ctx, actor.ID, book.ID, []string{ids[0], ids[0]}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("duplicate receipt IDs accepted", err)
	}
	assertNameSourcePagination(t, db, characters, actor, book, ids[0])
	hits, err := sources.SearchIn(ctx, actor, book.ProjectID, book.BranchID, research.OfficialSource, "zh", officialTestURL)
	if err != nil || len(hits) != 1 || transport.officialCalls != 0 {
		t.Fatal("official link discovery", err)
	}
	in := service.ResearchCollectInput{Provider: research.OfficialSource, ReferenceURL: officialTestURL, Language: "zh", PageID: hits[0].PageID, Query: officialTestURL, OperationID: "official-receipt"}
	source, err := sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in)
	if err != nil || source.Report.Complete || source.Report.Pending != 1 || source.Report.Research == nil || source.Report.Research.Extent != "metadata_excerpt" {
		t.Fatal("official excerpt boundary", err)
	}
	originals = append(originals, source)
	_, raw, err := ip.ReadImportFile(ctx, actor, book.ProjectID, book.BranchID, source.ID)
	var receipt struct {
		Schema  string `json:"schema"`
		Excerpt string `json:"excerpt"`
	}
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Schema != "official-reference-receipt-v1" || len([]rune(receipt.Excerpt)) > 160 || strings.Contains(string(raw), "FULLTEXT_CANARY") {
		t.Fatal("stored excerpt is not an explicit receipt", err)
	}
	before := transport.officialCalls
	if _, err = sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in); err != nil || transport.officialCalls != before {
		t.Fatal("receipt replay fetched again", err)
	}
	bad := in
	bad.ReferenceURL = "http://127.0.0.1/private"
	if _, err = sources.Collect(ctx, actor, book.ProjectID, book.BranchID, bad); err == nil || transport.officialCalls != before {
		t.Fatal("unsafe URL reached network", err)
	}
	if _, err = characters.PreviewNameSources(ctx, actor.ID, book.ID, []string{ids[1], source.ID}); err == nil {
		t.Fatal("non-name original admitted to batch")
	}
	researcher := &service.NovelResearch{Generations: characters.Generations}
	note, err := researcher.Create(ctx, actor.ID, book.ID, service.ResearchCreate{OperationID: "official-plan", Document: novel.ResearchNotebook{Title: "制度链接研究", Question: "手续如何核对", Genre: "历史", Era: "待核对", Region: "待核对", ChapterNeed: "制度场景", ResearchProposal: novel.ResearchProposal{SearchPlan: []novel.ResearchQuery{{Query: officialTestURL, Purpose: "核对原文", PreferredSource: "官方链接"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	executions := &service.NovelResearchExecutions{Research: researcher, Sources: sources}
	run, err := executions.Create(ctx, actor.ID, book.ID, note.ResourceID, service.ResearchExecutionCreate{Provider: research.OfficialSource, Language: "zh", AutoCollectLimit: 1, Revision: note.Revision, Hash: note.Hash, OperationID: "official-fixed-source"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run, err = executions.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
		if err != nil {
			t.Fatal("official checkpoint", err)
		}
	}
	if run.Document.Stage != "complete" || run.Document.Queries[0].Hits[0].URL != officialTestURL || run.Document.Queries[0].Hits[0].ImportID == "" {
		t.Fatal("official URL did not survive checkpoint")
	}
	if os.Getenv("STUDIO_TEMPORAL") != "" {
		assertResearchBackgroundRuntime(t, executions, actor, book, note, research.OfficialSource)
	}
	if _, err = characters.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "batch-originals-browser", Notes: "批量原件验收", Genre: "架空", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STUDIO_OFFICIAL_BATCH_BROWSER_NODE") != "" {
		assertOfficialBatchBrowser(t, ip, provider, book.Setup.Title)
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("source inventory", err)
	}
	assertSourceCatalogRestore(t, db, ip, book, actor, originals, library.ResourceID)
	t.Log("Batch name preview/deduplication/pagination, official robots-bound excerpt receipt, URL scope, fixed-source checkpoints, original retention and isolated restore verified; browser/runtime evidence is logged only when enabled.")
}

func assertNameSourcePagination(t *testing.T, db *sql.DB, characters *service.NovelCharacters, actor domain.Actor, book service.NovelBook, sourceID string) {
	t.Helper()
	clones := []string{}
	defer func() {
		for _, id := range clones {
			if _, err := db.Exec("DELETE FROM source_imports WHERE id=? AND project_id=?", id, book.ProjectID); err != nil {
				t.Error(err)
			}
		}
	}()
	for i := 0; i < 51; i++ {
		id := service.NewID()
		_, err := db.Exec(`INSERT INTO source_imports(id,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,operation_id) SELECT ?,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,? FROM source_imports WHERE id=? AND project_id=?`, id, "pagination-"+id, sourceID, book.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		clones = append(clones, id)
	}
	ctx := context.Background()
	first, err := characters.NameSources(ctx, actor.ID, book.ID, "")
	if err != nil || len(first.Items) != 50 || first.Next == "" {
		t.Fatal("first name source page", err)
	}
	second, err := characters.NameSources(ctx, actor.ID, book.ID, first.Next)
	if err != nil || len(second.Items) != 4 || second.Next != "" {
		t.Fatal("last name source page", err)
	}
	seen := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatal("duplicate pagination item")
		}
		seen[item.ID] = true
	}
	if _, err = characters.PreviewNameSources(ctx, actor.ID, book.ID, clones); err != nil {
		t.Fatal("valid batch larger than required-input list limit rejected", err)
	}
	if _, err = characters.NameSources(ctx, "unknown-author", book.ID, ""); err == nil {
		t.Fatal("name originals exposed to unavailable author")
	}
}

func assertOfficialBatchBrowser(t *testing.T, ip *service.IP, provider service.ResearchProvider, title string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	app := httpapi.NewWithResearchProvider(ip.Workspace.DB, origin, provider)
	app.Static("/", filepath.Join(web, "dist"))
	server.Config.Handler = app
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_OFFICIAL_BATCH_BROWSER_NODE"), filepath.Join(web, "tests", "official-name-batch-browser.cjs"), origin, title).CombinedOutput()
	if err != nil {
		t.Fatalf("official and name batch browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
