package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"novelstudio/internal/novel"
	"novelstudio/internal/research"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type catalogResearchTransport struct{ calls int }

func (p *catalogResearchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls++
	var body string
	if r.URL.Host == "www.ebi.ac.uk" {
		if strings.HasSuffix(r.URL.Path, "/search") {
			body = `{"resultList":{"result":[{"pmcid":"PMC42","title":"Wound care","authorString":"Ada Example","firstPublicationDate":"2024-01-01","isOpenAccess":"Y","abstractText":"<p>Clinical research.</p>"}]}}`
		} else {
			license := "by"
			if strings.Contains(r.URL.Path, "PMC43/") {
				license = "by-nc"
			}
			id := "42"
			if strings.Contains(r.URL.Path, "PMC43/") {
				id = "43"
			}
			body = fmt.Sprintf(`<article><front><article-meta><article-id pub-id-type="pmc">PMC%s</article-id><article-id pub-id-type="doi">10.1234/example</article-id><title-group><article-title>Wound care</article-title></title-group><contrib-group><contrib><name><given-names>Ada</given-names><surname>Example</surname></name></contrib></contrib-group><pub-date><year>2024</year></pub-date><permissions><license xlink:href="https://creativecommons.org/licenses/%s/4.0/"/></permissions></article-meta></front><body><p>Wound care professional detail. 年代和适用人群仍须复核。</p></body></article>`, id, license)
		}
	} else if r.URL.Host == "www.wikidata.org" {
		if r.URL.Query().Get("action") == "query" {
			body = `{"query":{"search":[{"title":"Q900000002","snippet":"Liora given name"}]}}`
		} else {
			id := r.URL.Query().Get("ids")
			label := "Elowen"
			if id == "Q900000002" {
				label = "Liora"
			}
			body = fmt.Sprintf(`{"entities":{"%s":{"id":"%s","lastrevid":71,"labels":{"en":{"language":"en","value":"%s"}},"descriptions":{"en":{"value":"given name; provenance must be checked"}},"claims":{"P31":[{"rank":"normal","mainsnak":{"snaktype":"value","datavalue":{"value":{"id":"Q202444"}}}}]}}}}`, id, id, label)
		}
	} else {
		return (&researchTransport{}).RoundTrip(r)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestResearchSourceCatalogAndNameIntake(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "catalog-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	transport := &catalogResearchTransport{}
	provider := research.NewCatalog(transport)
	sources := &service.ResearchSources{IP: ip, Provider: provider}
	hits, err := sources.SearchIn(ctx, actor, book.ProjectID, book.BranchID, research.EuropePMCSource, "en", "wound care")
	if err != nil || len(hits) != 1 {
		t.Fatal("primary research search", err)
	}
	in := service.ResearchCollectInput{Provider: research.EuropePMCSource, Language: "en", PageID: 42, Query: "wound care", OperationID: "collect-open-paper"}
	paper, err := sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in)
	if err != nil || paper.Report.Research == nil || paper.Report.Research.Version == "" || !strings.Contains(paper.Report.Text, "10.1234/example") {
		t.Fatal("licensed paper collection", err)
	}
	count := transport.calls
	again, err := sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in)
	if err != nil || again.ID != paper.ID || transport.calls != count {
		t.Fatal("same original fetched again", err)
	}
	in.Provider = research.NamesSource
	if _, err = sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in); !errors.Is(err, domain.ErrConflict) || transport.calls != count {
		t.Fatal("cross-source receipt accepted", err)
	}
	in.Provider = research.EuropePMCSource
	in.OperationID = "reject-noncommercial"
	in.PageID = 43
	if _, err = sources.Collect(ctx, actor, book.ProjectID, book.BranchID, in); err == nil {
		t.Fatal("unusable full-text license stored")
	}
	var stored int
	if err = db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE project_id=?", book.ProjectID).Scan(&stored); err != nil || stored != 1 {
		t.Fatal("rejected original was persisted", err)
	}
	name, err := sources.Collect(ctx, actor, book.ProjectID, book.BranchID, service.ResearchCollectInput{Provider: research.NamesSource, Language: "en", PageID: 900000001, Query: "Elowen", OperationID: "collect-name-original"})
	if err != nil {
		t.Fatal(err)
	}
	characters := &service.NovelCharacters{Generations: &service.Generations{IP: ip}}
	library, err := characters.OpenNameCatalog(ctx, actor.ID, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := characters.PreviewNameSource(ctx, actor.ID, book.ID, name.ID)
	if err != nil || len(preview.Document.Records) != len(library.Document.Records)+1 {
		t.Fatal("name source preview", err)
	}
	unchanged, err := characters.NameCatalog(ctx, actor.ID, book.ID)
	if err != nil || unchanged.Hash != library.Hash || unchanged.Revision != library.Revision {
		t.Fatal("preview saved without author action", err)
	}
	index := len(preview.Document.Records) - 1
	if preview.Document.Records[index].SourceImportID != name.ID || preview.Document.Records[index].Culture != "unknown" || preview.Document.Records[index].Kind != "given_name" {
		t.Fatal("name provenance or unsupported inference")
	}
	preview.Document.Records[index].Label = "Elowen Vale"
	library, err = characters.SaveNameCatalog(ctx, actor.ID, book.ID, preview)
	if err != nil {
		t.Fatal("save collected and edited name", err)
	}
	duplicate, err := characters.PreviewNameSource(ctx, actor.ID, book.ID, name.ID)
	if err != nil || len(duplicate.Document.Records) != len(library.Document.Records) || duplicate.Document.Records[index].Label != "Elowen Vale" {
		t.Fatal("source refresh overwrote author edit", err)
	}
	invalid := library
	invalid.Document.Records = append([]service.NameMaterial(nil), library.Document.Records...)
	invalid.Document.Records[index].SourceImportID = paper.ID
	if _, err = characters.SaveNameCatalog(ctx, actor.ID, book.ID, invalid); err == nil {
		t.Fatal("paper receipt used as name provenance")
	}
	if _, err = characters.PreviewNameSource(ctx, "unknown-author", book.ID, name.ID); err == nil {
		t.Fatal("private source exposed")
	}
	researcher := &service.NovelResearch{Generations: characters.Generations}
	note, err := researcher.Create(ctx, actor.ID, book.ID, service.ResearchCreate{OperationID: "paper-research", Document: novel.ResearchNotebook{Title: "开放论文研究", Question: "创伤处理哪些细节需要核对", Genre: "医疗", Era: "现代", Region: "研究适用地区待核对", ChapterNeed: "专业场景", ResearchProposal: novel.ResearchProposal{SearchPlan: []novel.ResearchQuery{{Query: "wound care", Purpose: "专业细节", PreferredSource: "开放原始论文"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	executions := &service.NovelResearchExecutions{Research: researcher, Sources: sources}
	run, err := executions.Create(ctx, actor.ID, book.ID, note.ResourceID, service.ResearchExecutionCreate{Provider: research.EuropePMCSource, Language: "en", AutoCollectLimit: 1, Revision: note.Revision, Hash: note.Hash, OperationID: "paper-plan"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run, err = executions.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
		if err != nil {
			t.Fatal("pinned provider checkpoint", err)
		}
	}
	if run.Document.Stage != "complete" || run.Document.Provider != research.EuropePMCSource {
		t.Fatal("provider was not pinned through collection")
	}
	if os.Getenv("STUDIO_TEMPORAL") != "" {
		assertResearchBackgroundRuntime(t, executions, actor, book, note, research.EuropePMCSource)
	}
	if _, err = characters.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "online-names-browser", Notes: "在线素材入库验收", Genre: "架空", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STUDIO_SOURCE_CATALOG_BROWSER_NODE") != "" {
		assertSourceCatalogBrowser(t, ip, provider, book.Setup.Title)
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("source inventory", err)
	}
	assertSourceCatalogRestore(t, db, ip, book, actor, []service.SourceImport{paper, name}, library.ResourceID)
	t.Log("Licensed paper originals, pinned source checkpoints, cross-source receipt rejection, name preview/edit/save/deduplication, scope and isolated restore verified without paid calls; enabled browser evidence is logged separately.")
}

func assertSourceCatalogBrowser(t *testing.T, ip *service.IP, provider service.ResearchProvider, title string) {
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_SOURCE_CATALOG_BROWSER_NODE"), filepath.Join(web, "tests", "research-source-catalog-browser.cjs"), origin, title).CombinedOutput()
	if err != nil {
		t.Fatalf("source catalog browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertSourceCatalogRestore(t *testing.T, db *sql.DB, ip *service.IP, book service.NovelBook, actor domain.Actor, originals []service.SourceImport, catalogID string) {
	t.Helper()
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "source-catalog.studio")
	if _, err := backup.Create(ctx, db, ip.Blobs, archive); err != nil {
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
	blobRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, source := range originals {
		original, err := ip.Blobs.ReadObject(ctx, book.ProjectID, source.Hash)
		if err != nil {
			t.Fatal(err)
		}
		copy, err := (storage.LocalBlobs{Root: blobRoot}).ReadObject(ctx, book.ProjectID, source.Hash)
		if err != nil || string(original) != string(copy) {
			t.Fatal("source original changed on restore", err)
		}
	}
	var before, after string
	if err = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", catalogID, actor.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", catalogID, actor.ID).Scan(&after); err != nil || before != after {
		t.Fatal("name source/edit restore", err)
	}
}
