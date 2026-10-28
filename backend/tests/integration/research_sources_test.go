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

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/research"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type researchTransport struct{ calls int }

func (s *researchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls++
	body := `{"parse":{"pageid":42,"revid":71,"title":"市舶司","text":"<p>市舶司办理商舶事务。</p><p>核对年代与地域后才能用于历史小说。</p>"}}`
	if r.URL.Query().Get("action") == "query" {
		body = `{"query":{"search":[{"pageid":42,"title":"市舶司","snippet":"港口制度"}]}}`
	}
	if r.URL.Query().Get("pageid") == "43" {
		raw, _ := json.Marshal(map[string]any{"parse": map[string]any{"pageid": 43, "revid": 72, "title": "长条目", "text": "<p>" + strings.Repeat("年代核对🌊", 30000) + "</p>"}})
		body = string(raw)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
}

func TestResearchPartialCollectionNeedsAcknowledgement(t *testing.T) {
	b, db, _, actor := novelEntryFixture(t)
	project := createProject(b, "长资料采集", "long-reference")
	branchID := branch(b, project, "资料分支")
	ip := propagationService(t, db)
	svc := &service.ResearchSources{IP: ip, Provider: research.NewWikipedia(&researchTransport{})}
	ctx := context.Background()
	source, err := svc.Collect(ctx, actor, project, branchID, service.ResearchCollectInput{Language: "zh", PageID: 43, Query: "年代", OperationID: "long-page"})
	if err != nil || source.Report.Complete || source.Report.Pending != 1 || len(source.Report.Text) > 256*1024 {
		t.Fatal("partial collection", err)
	}
	draft, err := ip.ReadImportDraft(ctx, actor, project, branchID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := service.ImportCandidateInput{Text: draft.Content.Text, ExpectedIPHead: draft.Content.BaseIPHead, OperationID: "partial-unconfirmed", Policy: draft.Content.Policy}
	if _, err = ip.ImportCandidate(ctx, actor, project, branchID, source.ID, in); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("partial page silently accepted", err)
	}
	in.AcceptPartial = true
	result, err := ip.ImportCandidate(ctx, actor, project, branchID, source.ID, in)
	if err != nil || !result.Manifest.Artifacts[0].PartialAcknowledged {
		t.Fatal("explicit partial adoption", err)
	}
}

func TestResearchSourceCollectionLifecycle(t *testing.T) {
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, d.ID, service.NovelConfirmation{Revision: d.Revision, OperationID: "research-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	transport := &researchTransport{}
	svc := &service.ResearchSources{IP: ip, Provider: research.NewWikipedia(transport)}
	hits, err := svc.Search(ctx, actor, book.ProjectID, book.BranchID, "zh", "宋代 市舶司")
	if err != nil || len(hits) != 1 {
		t.Fatal("search", hits, err)
	}
	in := service.ResearchCollectInput{Language: "zh", PageID: 42, Query: "宋代 市舶司", OperationID: "collect-reference"}
	source, err := svc.Collect(ctx, actor, book.ProjectID, book.BranchID, in)
	if err != nil {
		t.Fatal("collect", err)
	}
	again, err := svc.Collect(ctx, actor, book.ProjectID, book.BranchID, in)
	if err != nil || again.ID != source.ID || transport.calls != 2 {
		t.Fatal("retry fetched again", err, transport.calls)
	}
	changed := in
	changed.PageID = 43
	if _, err = svc.Collect(ctx, actor, book.ProjectID, book.BranchID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("operation identity", err)
	}
	if _, err = svc.Collect(ctx, actor, book.ProjectID, "wrong-branch", in); err == nil {
		t.Fatal("cross branch")
	}
	if _, err = svc.Search(ctx, domain.Actor{ID: "unknown", Role: "owner"}, book.ProjectID, book.BranchID, "zh", "市舶司"); err == nil {
		t.Fatal("unknown actor searched")
	}
	if transport.calls != 2 {
		t.Fatal("unauthorized request reached network")
	}
	originalName, original, err := ip.ReadImportFile(ctx, actor, book.ProjectID, book.BranchID, source.ID)
	if err != nil || !strings.HasSuffix(originalName, ".json") || !strings.Contains(string(original), `"revid":71`) {
		t.Fatal("raw API bytes", err)
	}
	draft, err := ip.ReadImportDraft(ctx, actor, book.ProjectID, book.BranchID, source.ID)
	if err != nil || draft.Content.Policy.Space != "research" || !strings.Contains(draft.Content.Text, "oldid=71") || !strings.Contains(draft.Content.Text, "CC BY-SA") {
		t.Fatal("source metadata or default space", err)
	}
	if os.Getenv("STUDIO_RESEARCH_BROWSER_NODE") != "" {
		assertResearchBrowser(t, ip, book.Setup.Title, source.ID, book.ProjectID, book.BranchID)
	}
	root := "/api/projects/" + book.ProjectID + "/branches/" + book.BranchID
	list := b.request("GET", root+"/research-sources", nil, 200)
	if len(list["items"].([]any)) != 1 {
		t.Fatal("persisted collection missing")
	}
	draft, err = ip.ReadImportDraft(ctx, actor, book.ProjectID, book.BranchID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Content.Text = "作者核对笔记：市舶司属于港口资料，需核对具体年代。\n" + source.Report.Text
	draft, err = ip.SaveImportDraft(ctx, actor, book.ProjectID, book.BranchID, source.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	br := b.request("GET", root, nil, 200)
	candidate := b.request("POST", root+"/imports/"+source.ID+"/candidate", map[string]any{"text": draft.Content.Text, "expected_ip_head": br["head"], "operation_id": "research-candidate", "policy": domain.KnowledgePolicy{Space: "research", Visibility: "author"}}, 201)
	ref := candidate["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
	if ref["source_import_id"] != source.ID || ref["original_hash"] != source.Hash {
		t.Fatal("lost source dependency")
	}
	r := b.request("GET", "/api/projects/"+book.ProjectID+"/resources/"+ref["resource_id"].(string), nil, 200)
	if r["head"] != "" {
		t.Fatal("collection auto-published")
	}
	releaseAdopt(b, book.ProjectID, candidate["id"].(string))
	releasePublish(b, book.ProjectID, candidate["id"].(string))
	b.request("POST", root+"/knowledge/index", map[string]any{}, 200)
	pack := b.request("POST", root+"/knowledge/search", map[string]any{"query": "市舶司 港口", "purpose": "research", "limit": 5}, 200)
	if len(pack["research_candidates"].([]any)) == 0 || len(pack["evidence"].([]any)) != 0 {
		t.Fatal("research mixed with facts", pack)
	}
	pack = b.request("POST", root+"/knowledge/search", map[string]any{"query": "市舶司 港口", "purpose": "author", "limit": 5}, 200)
	if pack["research_candidates"] != nil {
		t.Fatal("unreviewed source in ordinary context")
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("source inventory", err)
	}
	archive := filepath.Join(t.TempDir(), "research.studio")
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
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: restoredRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var before, after string
	if err = db.QueryRow("SELECT report FROM source_imports WHERE id=?", source.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT report FROM source_imports WHERE id=?", source.ID).Scan(&after); err != nil || before != after {
		t.Fatal("restored source provenance", err)
	}
	bytes, err := (storage.LocalBlobs{Root: restoredRoot}).ReadObject(ctx, book.ProjectID, source.Hash)
	if err != nil || string(bytes) != string(original) {
		t.Fatal("restored raw API response", err)
	}
	t.Log("Collected exact source bytes, pinned revision and attribution; retries avoided network, unauthorized reads blocked; edit/adopt/research-only RAG and independent restore verified without paid calls.")
}

func assertResearchBrowser(t *testing.T, ip *service.IP, title, source, project, branch string) {
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_RESEARCH_BROWSER_NODE"), filepath.Join(web, "tests", "research-sources-browser.cjs"), origin, title, source, project, branch).CombinedOutput()
	if err != nil {
		t.Fatalf("research browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
