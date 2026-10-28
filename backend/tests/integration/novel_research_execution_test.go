package integration

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	platformworker "novelstudio/internal/worker"
)

func TestNovelResearchPlanExecution(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "research-execution-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	researcher := &service.NovelResearch{Generations: &service.Generations{IP: ip}}
	note, err := researcher.Create(ctx, actor.ID, book.ID, service.ResearchCreate{OperationID: "execution-notebook", Document: novel.ResearchNotebook{Title: "港口制度", Question: "商船怎样进港", Genre: "历史", Era: "宋", Region: "泉州", ChapterNeed: "进港场景", ResearchProposal: novel.ResearchProposal{SearchPlan: []novel.ResearchQuery{{Query: "宋代 市舶司", Purpose: "机构职责", PreferredSource: "一手史料"}, {Query: "泉州 港口", Purpose: "地域制度", PreferredSource: "博物馆资料"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &researchTransport{}
	svc := &service.NovelResearchExecutions{Research: researcher, Sources: &service.ResearchSources{IP: ip, Provider: research.NewWikipedia(transport)}}
	in := service.ResearchExecutionCreate{Revision: note.Revision, Hash: note.Hash, Language: "zh", OperationID: "execute-research"}
	run, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, in)
	if err != nil {
		t.Fatal("create", err)
	}
	again, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, in)
	if err != nil || again.ResourceID != run.ResourceID || transport.calls != 0 {
		t.Fatal("create replay/network", err)
	}
	original := run
	run, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
	if err != nil || !run.Document.Queries[0].Done || run.Document.Queries[1].Done {
		t.Fatal("first search", err)
	}
	if _, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, original.Revision); !errors.Is(err, domain.ErrConflict) || transport.calls != 1 {
		t.Fatal("stale step fetched again", err)
	}
	run, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "pause")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision); !errors.Is(err, domain.ErrConflict) || transport.calls != 1 {
		t.Fatal("paused execution performed network work", err)
	}
	svc = &service.NovelResearchExecutions{Research: researcher, Sources: svc.Sources}
	run, err = svc.Read(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID)
	if err != nil || !run.Document.Paused || !run.Document.Queries[0].Done {
		t.Fatal("resume persistence", err)
	}
	run, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "resume")
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
	if err != nil || run.Document.Stage != "selection" || transport.calls != 2 {
		t.Fatal("complete search", err)
	}
	run, err = svc.Select(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, service.ResearchExecutionSelection{Revision: run.Revision, Items: []service.ResearchHitSelection{{QueryIndex: 0, PageID: 42}, {QueryIndex: 1, PageID: 42}}})
	if err != nil {
		t.Fatal("select", err)
	}
	run, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision)
	if err != nil || run.Document.Stage != "complete" || transport.calls != 3 {
		t.Fatal("collect/deduplicate", err, transport.calls)
	}
	first, second := run.Document.Queries[0].Hits[0], run.Document.Queries[1].Hits[0]
	if first.ImportID == "" || first.ImportID != second.ImportID {
		t.Fatal("duplicate material or missing receipt")
	}
	stored, err := ip.ReadImport(ctx, actor, book.ProjectID, book.BranchID, first.ImportID)
	if err != nil || stored.Report.Research == nil || stored.Report.Research.Query != "宋代 市舶司" {
		t.Fatal("original provenance", err)
	}
	if _, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, run.Revision); err != nil || transport.calls != 3 {
		t.Fatal("completed step not idempotent", err)
	}
	if _, err = svc.Select(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, service.ResearchExecutionSelection{Revision: run.Revision, Items: []service.ResearchHitSelection{{QueryIndex: 0, PageID: 99999}}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("unknown hit accepted", err)
	}
	run, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, run.ResourceID, "resume"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("cancelled run restarted", err)
	}
	items, err := svc.List(ctx, actor.ID, book.ID, note.ResourceID)
	if err != nil || len(items) != 1 || !items[0].Document.Cancelled {
		t.Fatal("history", err)
	}
	in.OperationID = "automatic-collection"
	in.AutoCollectLimit = 1
	automatic, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		automatic, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, automatic.ResourceID, automatic.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if automatic.Document.Stage != "collect" || !automatic.Document.Queries[0].Hits[0].Selected {
		t.Fatal("automatic plan stopped before collecting sources")
	}
	automatic, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, automatic.ResourceID, automatic.Revision)
	if err != nil || automatic.Document.Stage != "complete" {
		t.Fatal("automatic collection", err)
	}
	in.AutoCollectLimit = 0
	gate := &gatedResearchProvider{inner: svc.Sources.Provider, entered: make(chan struct{}), proceed: make(chan struct{})}
	svc.Sources.Provider = gate
	in.OperationID = "pause-in-flight"
	inFlight, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, in)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, e := svc.Step(ctx, actor.ID, book.ID, note.ResourceID, inFlight.ResourceID, inFlight.Revision)
		finished <- e
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never started")
	}
	_, err = svc.Control(ctx, actor.ID, book.ID, note.ResourceID, inFlight.ResourceID, "pause")
	close(gate.proceed)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal("paused late completion", err)
	}
	inFlight, err = svc.Read(ctx, actor.ID, book.ID, note.ResourceID, inFlight.ResourceID)
	if err != nil || !inFlight.Document.Paused || inFlight.Document.Queries[0].Done || inFlight.Document.Attempt != "" {
		t.Fatal("late response changed paused result", err)
	}
	failedProvider := &retryResearchProvider{inner: svc.Sources.Provider}
	svc.Sources.Provider = failedProvider
	in.OperationID = "retry-source-failure"
	retryRun, err := svc.Create(ctx, actor.ID, book.ID, note.ResourceID, in)
	if err != nil {
		t.Fatal(err)
	}
	retryRun, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, retryRun.ResourceID, retryRun.Revision)
	if err != nil || retryRun.Document.Error == "" || retryRun.Document.Queries[0].Done || retryRun.Document.Attempt != "" {
		t.Fatal("failed request checkpoint", err)
	}
	retryRun, err = svc.Step(ctx, actor.ID, book.ID, note.ResourceID, retryRun.ResourceID, retryRun.Revision)
	if err != nil || retryRun.Document.Error != "" || !retryRun.Document.Queries[0].Done {
		t.Fatal("failed request resume", err)
	}
	beforeCalls := transport.calls
	if _, err = svc.Step(ctx, "unavailable-actor", book.ID, note.ResourceID, retryRun.ResourceID, retryRun.Revision); err == nil || transport.calls != beforeCalls {
		t.Fatal("unauthorized request reached public source", err)
	}
	backgroundIDs := assertResearchBackgroundCheckpoints(t, db, svc, actor, book, note, transport)
	if os.Getenv("STUDIO_TEMPORAL") != "" {
		backgroundIDs = append(backgroundIDs, assertResearchBackgroundRuntime(t, svc, actor, book, note))
	}
	if os.Getenv("STUDIO_RESEARCH_EXECUTION_BROWSER_NODE") != "" {
		assertResearchExecutionBrowser(t, ip, svc.Sources.Provider, book.Setup.Title)
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("source inventory", err)
	}
	archive := filepath.Join(t.TempDir(), "research-execution.studio")
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
		t.Fatal("restore execution", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredEvents int
	if err = restored.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='research.step' AND processed_at IS NULL").Scan(&restoredEvents); err != nil || restoredEvents != 0 {
		t.Fatal("restored backup retained pending network delivery", err)
	}
	for _, id := range append(backgroundIDs, run.ResourceID, automatic.ResourceID, inFlight.ResourceID, retryRun.ResourceID) {
		var before, after string
		if err = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, actor.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, actor.ID).Scan(&after); err != nil || before != after {
			t.Fatal("restored execution differs", err)
		}
	}
	t.Log("Persisted search plan, pause/resume, stale request protection, selected collection, duplicate reuse and original source provenance verified without model calls.")
}

type retryResearchProvider struct {
	inner  service.ResearchProvider
	failed bool
}

func (p *retryResearchProvider) Search(ctx context.Context, language, query string) ([]research.Hit, error) {
	if !p.failed {
		p.failed = true
		return nil, errors.New("upstream temporarily unavailable")
	}
	return p.inner.Search(ctx, language, query)
}
func (p *retryResearchProvider) Collect(ctx context.Context, language string, page int64) (research.Document, error) {
	return p.inner.Collect(ctx, language, page)
}

type gatedResearchProvider struct {
	inner   service.ResearchProvider
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (p *gatedResearchProvider) Search(ctx context.Context, language, query string) ([]research.Hit, error) {
	p.once.Do(func() {
		close(p.entered)
		select {
		case <-p.proceed:
		case <-ctx.Done():
		}
	})
	return p.inner.Search(ctx, language, query)
}
func (p *gatedResearchProvider) Collect(ctx context.Context, language string, page int64) (research.Document, error) {
	return p.inner.Collect(ctx, language, page)
}

func assertResearchExecutionBrowser(t *testing.T, ip *service.IP, provider service.ResearchProvider, title string) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "research-browser-"+service.NewID(), platformworker.RuntimeServices{ResearchProvider: provider})
	if err != nil {
		t.Fatal("browser background runtime", err)
	}
	defer runtime.Close()
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_RESEARCH_EXECUTION_BROWSER_NODE"), filepath.Join(web, "tests", "novel-research-execution-browser.cjs"), origin, title).CombinedOutput()
	if err != nil {
		t.Fatalf("research execution browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
