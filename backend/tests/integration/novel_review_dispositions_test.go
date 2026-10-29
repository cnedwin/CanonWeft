package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type dispositionFixture struct {
	chapterBasisFixture
	svc    *service.NovelBookReviews
	report service.NovelBookReviewReport
	second service.NovelChapterDraft
}

func newDispositionFixture(t *testing.T, findings int) dispositionFixture {
	t.Helper()
	// Snapshot clocks use UTC. Scope the connection session to UTC rather than
	// changing the reused server's global settings or any working studio.
	if admin := os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"); admin != "" {
		cfg, e := mysql.ParseDSN(admin)
		if e != nil {
			t.Fatal("invalid isolated administrator DSN")
		}
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params["time_zone"] = "'+00:00'"
		t.Setenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN", cfg.FormatDSN())
	}
	ctx := context.Background()
	f := newChapterBasisFixture(t, 1)
	f.draft.Document.Text = "原文甲。🙂重复句。重复句。"
	var err error
	f.draft, err = f.chapters.Save(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID, f.draft)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := (&service.NovelBatch{Writer: &service.NovelChapterWriter{Generations: &service.Generations{IP: f.ip}}}).Chapters(ctx, f.actor.ID, f.book.ID)
	if err != nil || len(catalog) != 2 {
		t.Fatal(catalog, err)
	}
	resource, err := f.chapters.Open(ctx, f.actor.ID, f.book.ID, catalog[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.chapters.Read(ctx, f.actor.ID, f.book.ID, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	second.Document.Text = "原文乙。"
	second, err = f.chapters.Save(ctx, f.actor.ID, f.book.ID, resource.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	connection := f.b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "隔离审稿替身", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	f.b.request("PUT", "/api/model-bindings/critic", map[string]any{"connection_id": connection, "connection_revision": 1, "revision": 0}, 200)
	gen := &service.Generations{IP: f.ip}
	price, err := gen.SavePrice(ctx, f.actor.ID, service.PriceCard{ConnectionID: connection, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&service.Budgets{Workspace: f.ip.Workspace}).Configure(ctx, f.actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelBookReviews{Generations: gen, Knowledge: &service.Knowledge{IP: f.ip, Embedder: embedder}}
	in := service.NovelBookReviewInput{Source: "draft", PriceID: price.ID, Currency: "USD", BudgetLimitMicros: 100000, MaxOutputTokens: 32768, OperationID: "disposition-review"}
	preview, err := svc.Preview(ctx, f.actor.ID, f.book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.PreviewHash = preview.Hash
	run, err := svc.Prepare(ctx, f.actor.ID, f.book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	result := novel.BookReviewResult{Coverage: []string{f.draft.Document.ChapterID, second.Document.ChapterID}, Checks: map[string]string{}, Findings: []novel.BookReviewFinding{}, Carry: []novel.BookReviewCarry{}, Limits: []string{}}
	for _, category := range novel.BookReviewCategories {
		result.Checks[category] = "no_issue_found"
	}
	result.Checks["causality"] = "issues_found"
	for i := 0; i < findings; i++ {
		result.Findings = append(result.Findings, novel.BookReviewFinding{ID: fmt.Sprintf("finding-%d", i), Category: "causality", Severity: "major", Title: "两章因果待核对", Explanation: "两章的叙述需要作者核对", Suggestion: "核对人物行动前后是否一致", Evidence: []novel.BookReviewEvidence{{ChapterID: f.draft.Document.ChapterID, Quote: "重复句。"}, {ChapterID: second.Document.ChapterID, Quote: "原文乙。"}}})
	}
	raw, _ := json.Marshal(result)
	response, _ := json.Marshal(map[string]any{"id": "disposition-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(raw)}}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
	gen.Adapter = models.New(&generationTransport{response: string(response)})
	recipes := &service.RecipeExecutions{Generations: gen}
	run, err = recipes.Start(ctx, f.actor, f.book.ProjectID, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, f.actor, f.book.ProjectID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Execute(ctx, run.Steps[0].GenerationID); err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Report(ctx, f.actor.ID, f.book.ID, run.ID)
	if err != nil || len(report.Passes) != 1 || len(report.Passes[0].Result.Findings) != findings || report.Passes[0].StepID == "" || report.Passes[0].OutputHash == "" {
		t.Fatal("isolated report fixture", err, report.Run.State)
	}
	return dispositionFixture{f, svc, report, second}
}

func readDispositions(t *testing.T, b *browser, path string) service.NovelReviewDispositions {
	t.Helper()
	raw, _ := json.Marshal(b.request("GET", path, nil, 200))
	var value service.NovelReviewDispositions
	if json.Unmarshal(raw, &value) != nil {
		t.Fatal("invalid dispositions")
	}
	return value
}
func dispositionInput(f dispositionFixture, view service.NovelReviewDispositions, index int) service.NovelReviewDispositionInput {
	return service.NovelReviewDispositionInput{Revision: view.Revision, Hash: view.Hash, SourceStepID: f.report.Passes[0].StepID, SourceOutputHash: f.report.Passes[0].OutputHash, FindingID: fmt.Sprintf("finding-%d", index), Status: "revised", Note: "作者已修改，仍需核对；不代表模型审查通过", ExpectedChapters: view.CurrentChapters, OperationID: "request-v2:" + service.NewID()}
}

func TestNovelReviewDispositionsVersionedPrivateAndReadOnly(t *testing.T) {
	f := newDispositionFixture(t, 2)
	ctx := context.Background()
	path := "/api/novels/" + f.book.ID + "/book-reviews/" + f.report.Run.ID + "/dispositions"
	counts := func() []int {
		values := make([]int, 5)
		for i, table := range []string{"resources", "resource_drafts", "audit_events", "generation_tasks", "model_calls"} {
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&values[i]); err != nil {
				t.Fatal(err)
			}
		}
		return values
	}
	before := counts()
	embeds := f.embeddingCalls.Load()
	view := readDispositions(t, f.b, path)
	if view.Revision != 0 || view.ResourceID != "" || view.Hash != "" || len(view.Entries) != 0 || len(view.CurrentChapters) != 2 || !reflect.DeepEqual(before, counts()) || embeds != f.embeddingCalls.Load() {
		t.Fatal("GET created state or lost current author chapter pins", view)
	}
	in := dispositionInput(f, view, 0)
	f.b.request("PUT", path, in, 200)
	stored := readDispositions(t, f.b, path)
	if stored.Revision != 1 || len(stored.Entries) != 1 || stored.Entries[0].Stale || len(stored.Entries[0].Chapters) != 2 || stored.Entries[0].OperationID != in.OperationID {
		t.Fatal("disposition lost its source/pins/operation", stored)
	}
	f.b.request("PUT", path, in, 409)
	bad := dispositionInput(f, stored, 1)
	bad.ExpectedChapters = bad.ExpectedChapters[:1]
	f.b.request("PUT", path, bad, 409)
	f.second.Document.Text += "作者新增衔接。"
	var err error
	f.second, err = f.chapters.Save(ctx, f.actor.ID, f.book.ID, f.second.Resource.ID, f.second)
	if err != nil {
		t.Fatal(err)
	}
	changed := readDispositions(t, f.b, path)
	if !changed.Entries[0].Stale || changed.Entries[0].Status != "revised" {
		t.Fatal("editing the other evidence chapter did not expire the old human disposition", changed)
	}
	bad = dispositionInput(f, stored, 1)
	f.b.request("PUT", path, bad, 409)
	if _, err = f.ip.SaveResourceDraft(ctx, f.actor, f.book.ProjectID, f.second.Resource.ID, service.ResourceDraft{Revision: f.second.Revision, BaseHead: f.second.BaseHead, Text: "isolated fixture: current chapter unavailable"}); err != nil {
		t.Fatal(err)
	}
	missing := readDispositions(t, f.b, path)
	deferred := dispositionInput(f, missing, 1)
	deferred.Status = "deferred"
	f.b.request("PUT", path, deferred, 200)
	missing = readDispositions(t, f.b, path)
	if len(missing.Entries) != 2 || !missing.Entries[1].Stale {
		t.Fatal("missing evidence note was treated as a current confirmation", missing)
	}
	confirm := dispositionInput(f, missing, 1)
	confirm.Status = "confirmed"
	f.b.request("PUT", path, confirm, 409)
	member := f.b.request("POST", "/api/members", map[string]any{"username": "disposition-other", "password": "disposition-other-password", "role": "editor"}, 201)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: f.b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "disposition-other", "password": "disposition-other-password"}, 200)
	other.request("GET", path, nil, 403)
	other.request("PUT", path, dispositionInput(f, changed, 1), 403)
	wrong := strings.Replace(path, f.report.Run.ID, service.NewID(), 1)
	f.b.request("GET", wrong, nil, 403)
	var tasks, calls int
	if err = f.db.QueryRow("SELECT (SELECT COUNT(*) FROM generation_tasks),(SELECT COUNT(*) FROM model_calls)").Scan(&tasks, &calls); err != nil || tasks != before[3] || calls != before[4] {
		t.Fatal("human dispositions created another model review", err)
	}
}

func TestNovelReviewDispositionsAttemptIsolationAndNoPublication(t *testing.T) {
	f := newDispositionFixture(t, 1)
	ctx := context.Background()
	path := "/api/novels/" + f.book.ID + "/book-reviews/" + f.report.Run.ID + "/dispositions"
	view := readDispositions(t, f.b, path)
	in := dispositionInput(f, view, 0)
	in.Status = "confirmed"
	f.b.request("PUT", path, in, 200)
	view = readDispositions(t, f.b, path)
	var entryID string
	if err := f.db.QueryRow("SELECT id FROM resources WHERE kind='novel_review_dispositions' AND name LIKE ?", "审稿整改 · "+f.report.Run.ID+" · %").Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	knowledge := f.svc.Knowledge
	choices, err := knowledge.WorkingChoices(ctx, f.actor, f.book.ProjectID, f.book.BranchID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range choices.Items {
		if c.Ref.ID == entryID || c.Ref.ID == view.ResourceID {
			t.Fatal("disposition offered as a writing input")
		}
	}
	_, err = knowledge.Search(ctx, f.actor, f.book.ProjectID, f.book.BranchID, service.SearchInput{Purpose: "critic", Query: "核对", IncludeWorking: true, Limit: 8, TokenBudget: 65536, WorkingRefs: []domain.WorkingRef{{Kind: "resource_draft", ID: entryID, Version: "1"}}})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("explicit disposition was accepted as model context", err)
	}
	_, err = f.ip.PrepareRelease(ctx, f.actor, f.book.ProjectID, domain.ReleaseInput{BranchID: f.book.BranchID, OperationID: "publish-disposition-must-fail", Updates: []domain.ResourceUpdate{{ResourceID: entryID, Text: "不得发布的作者备注"}}})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatal("generic release allowed a disposition note", err)
	}
	step := f.report.Run.Steps[0]
	if _, err = (&service.RecipeExecutions{Generations: f.svc.Generations}).Retry(ctx, f.actor, f.book.ProjectID, f.report.Run.ID, service.RecipeRetryInput{ExpectedRevision: f.report.Run.Revision, OperationID: "disposition-new-review-attempt", Step: step.Frozen.Input}); err != nil {
		t.Fatal("replace report attempt through its existing retry contract", err)
	}
	view = readDispositions(t, f.b, path)
	if !view.Entries[0].Stale {
		t.Fatal("new report attempt inherited old manual confirmation", view)
	}
	in = dispositionInput(f, view, 0)
	f.b.request("PUT", path, in, 409)
}

func TestNovelReviewDispositionsShardedRetentionAndSnapshot(t *testing.T) {
	f := newDispositionFixture(t, 64)
	ctx := context.Background()
	path := "/api/novels/" + f.book.ID + "/book-reviews/" + f.report.Run.ID + "/dispositions"
	view := readDispositions(t, f.b, path)
	for i := 0; i < 64; i++ {
		in := dispositionInput(f, view, i)
		in.Note = strings.Repeat("核", 2000)
		raw, _ := json.Marshal(f.b.request("PUT", path, in, 200))
		if json.Unmarshal(raw, &view) != nil {
			t.Fatal("invalid save response")
		}
	}
	var total, maxBody, count int
	if err := f.db.QueryRow("SELECT SUM(OCTET_LENGTH(d.body)),MAX(OCTET_LENGTH(d.body)),COUNT(*) FROM resource_drafts d JOIN resources r ON r.id=d.resource_id WHERE r.project_id=? AND r.kind='novel_review_dispositions'", f.book.ProjectID).Scan(&total, &maxBody, &count); err != nil || total <= 256*1024 || maxBody >= 256*1024 || count != 65 || len(view.Entries) != 64 {
		t.Fatal("run-wide capacity still depends on one 256KiB body", total, maxBody, count, err)
	}
	scanner := &service.RetentionScanner{Workspace: f.ip.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if _, err := scanner.Scan(ctx, f.actor.ID, f.book.ProjectID); err != nil {
		t.Fatal("disposition owner/retention registration", err)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "review-dispositions.studio")
	if _, err := backup.Create(ctx, f.db, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}, archive); err != nil {
		t.Fatal("snapshot author notes", err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := file.Stat()
	_, err = backup.ValidateArchive(file, stat.Size())
	file.Close()
	if err != nil {
		t.Fatal("snapshot validation/owner graph", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := f.db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	_, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(root, "restored"), Authority: backup.ProtectedAuthority{DB: f.db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}})
	if err != nil {
		t.Fatal("snapshot restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredCount, restoredBytes int
	if err = restored.QueryRow("SELECT COUNT(*),SUM(OCTET_LENGTH(d.body)) FROM resource_drafts d JOIN resources r ON r.id=d.resource_id WHERE r.project_id=? AND r.kind='novel_review_dispositions'", f.book.ProjectID).Scan(&restoredCount, &restoredBytes); err != nil || restoredCount != count || restoredBytes != total {
		t.Fatal("author disposition bodies did not round-trip", err, restoredCount, restoredBytes)
	}
}
