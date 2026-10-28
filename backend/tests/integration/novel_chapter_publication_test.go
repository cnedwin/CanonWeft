package integration

import (
	"context"
	"encoding/json"
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

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

// Local protocol answers only. Real qualification, billing gates, review
// selection and publication are exercised; production never uses this transport.
type chapterReviewTransport struct {
	calls      int
	lastPrompt string
}

func (p *chapterReviewTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls++
	var request struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
		return nil, fmt.Errorf("missing test request")
	}
	prompt := request.Messages[len(request.Messages)-1].Content
	p.lastPrompt = prompt
	var doc domain.SemanticReviewDocument
	method := strings.Index(prompt, "<fixed-method-data>\n")
	if method >= 0 {
		var payload struct {
			Input domain.SemanticReviewDocument `json:"input"`
		}
		if err := json.NewDecoder(strings.NewReader(prompt[method+len("<fixed-method-data>\n"):])).Decode(&payload); err != nil {
			return nil, err
		}
		doc = payload.Input
	} else {
		start := strings.Index(prompt, "Input JSON:\n")
		if start < 0 {
			return nil, fmt.Errorf("unknown test review request")
		}
		if err := json.NewDecoder(strings.NewReader(prompt[start+len("Input JSON:\n"):])).Decode(&doc); err != nil {
			return nil, err
		}
	}
	report := domain.SemanticReviewReport{Schema: 1, TargetHash: doc.Target.Hash, Verdict: "PASS", Coverage: doc.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "本地协议测试，非文学质量证明"}
	if method >= 0 {
		category := map[string]string{"belief": "fact_belief_claim", "temporal": "temporal", "secret": "knowledge_visibility", "world": "world_rule", "ambiguous": "unsupported", "injection": "fact_belief_claim"}[doc.Target.ID]
		if category != "" {
			report.Verdict = "FAIL"
			severity := "blocker"
			if category == "unsupported" {
				report.Verdict, severity = "UNKNOWN", "unknown"
			}
			report.Issues = []domain.SemanticReviewIssue{{Category: category, Severity: severity, TargetKey: "resource:scene", AnchorKeys: []string{"source"}, Explanation: "测试反例不成立或证据不足"}}
		}
	}
	output, _ := json.Marshal(report)
	body, _ := json.Marshal(map[string]any{"id": "chapter-critic-fixture", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(output)}}}, "usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 40}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
}

func assertChapterPublication(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapter string) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := chapters.ReadLedger(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	public := ledger.Document.Entries[0]
	public.ID, public.Visibility = service.NewID(), "public"
	ledger.Document.Entries = append(ledger.Document.Entries, public)
	ledger, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, ledger)
	if err != nil {
		t.Fatal(err)
	}
	release, err := chapters.PrepareRelease(ctx, actor.ID, book.ID, chapter, service.NovelChapterReleaseInput{Revision: draft.Revision, Hash: draft.Hash, LedgerRevision: ledger.Revision, OperationID: "chapter-to-publish"})
	if err != nil {
		t.Fatal(err)
	}
	protocol := &chapterReviewTransport{}
	original := gen.Adapter
	gen.Adapter = models.New(protocol)
	defer func() { gen.Adapter = original }()
	if _, err = (&service.Models{Workspace: gen.IP.Workspace}).Bind(ctx, actor.ID, domain.ModelBinding{Role: "critic", ConnectionID: price.ConnectionID, ConnectionRevision: price.ConnectionRevision}); err != nil {
		t.Fatal(err)
	}
	q := &service.Qualifications{IP: gen.IP, Generations: gen}
	installed, err := q.InstallBaselines(ctx, actor, book.ProjectID, service.QualificationPresetInput{SuiteID: "shared-semantic-review-v1", Kind: "agent", Role: "critic"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := q.Create(ctx, actor, book.ProjectID, service.QualificationInput{OperationID: service.NewID(), VersionID: installed.BaselineID, BaselineID: installed.BaselineID, BranchID: book.BranchID, SuiteID: "shared-semantic-review-v1", Role: "critic", PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		state, err := q.Advance(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, err = q.Read(ctx, actor, book.ProjectID, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state != "QUEUED" && state != "RUNNING" {
			break
		}
		for _, step := range run.Steps {
			if step.State == "RUNNING" && step.ChildID != "" {
				if step.ChildKind != "generation" {
					t.Fatal("unexpected tool execution")
				}
				if _, err = gen.Execute(ctx, step.ChildID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if run.State != "PASSED" {
		t.Fatal("critic qualification", run.State, run.Reason)
	}
	binding, err := q.Activate(ctx, actor, book.ProjectID, service.BindingInput{OperationID: service.NewID(), QualificationID: run.QualificationID, TaskKind: "shared.semantic-review"})
	if err != nil {
		t.Fatal(err)
	}
	reviews := &service.SemanticReviews{Generations: gen}
	review, err := reviews.Prepare(ctx, actor, book.ProjectID, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: book.BranchID, ReviewerBindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil || review.State != "PREPARED" {
		t.Fatal("review prepare", err, review.Reason)
	}
	before := protocol.calls
	if _, err = reviews.Start(ctx, actor, book.ProjectID, review.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := reviews.Advance(ctx, review.ID); err != nil || state != "RUNNING" {
		t.Fatal(state, err)
	}
	if _, err = gen.Execute(ctx, review.GenerationID); err != nil {
		t.Fatal(err)
	}
	if state, err := reviews.Advance(ctx, review.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	if protocol.calls != before+1 {
		t.Fatal("critic duplicate call")
	}
	if _, err = reviews.Select(ctx, actor, book.ProjectID, review.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.IP.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IndexJobs{Knowledge: knowledge}
	job, err := jobs.Create(ctx, actor, book.ProjectID, book.BranchID, service.IndexJobInput{OperationID: service.NewID(), ReleaseID: release.ID})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Execute(ctx, job.ID); err != nil || state != "READY" {
		t.Fatal("publication index", state, err)
	}
	db := gen.IP.Workspace.DB
	if _, err = db.Exec("CREATE TRIGGER reject_chapter_memory BEFORE INSERT ON memory_revisions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test-only chapter memory rollback'"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TRIGGER IF EXISTS reject_chapter_memory")
	if _, err = gen.IP.PublishRelease(ctx, actor, book.ProjectID, release.ID); err == nil || !strings.Contains(err.Error(), "test-only chapter memory rollback") {
		t.Fatal("joint publication did not reach atomic memory write", err)
	}
	var heads, memories int
	if err = db.QueryRow("SELECT COUNT(*) FROM resources WHERE head_revision=?", release.ID).Scan(&heads); err != nil || heads != 0 {
		t.Fatal("partial body publication", heads, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM memory_items WHERE project_id=?", book.ProjectID).Scan(&memories); err != nil || memories != 0 {
		t.Fatal("partial memory publication", memories, err)
	}
	if _, err = db.Exec("DROP TRIGGER reject_chapter_memory"); err != nil {
		t.Fatal(err)
	}
	if node := os.Getenv("STUDIO_PUBLICATION_BROWSER_NODE"); node != "" {
		web, err := filepath.Abs("../../../web")
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(nil)
		origin := "http://" + server.Listener.Addr().String()
		app := httpapi.New(db, origin)
		app.Static("/", filepath.Join(web, "dist"))
		server.Config.Handler = app
		server.Start()
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, err := exec.CommandContext(browserCtx, node, filepath.Join(web, "tests", "novel-publication-browser.cjs"), origin, book.Setup.Title).CombinedOutput()
		if err != nil {
			t.Fatalf("publication browser: %v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	published, err := chapters.ConfirmRelease(ctx, actor.ID, book.ID, chapter, release.ID, release.Hash, true)
	if err != nil || published.Status != "PUBLISHED" {
		t.Fatal("chapter publication", err)
	}
	if _, err = gen.IP.PublishRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal("publication replay", err)
	}
	assertNovelEditionArchive(t, gen.IP, actor, book)
	if err = db.QueryRow("SELECT COUNT(*) FROM memory_items WHERE project_id=?", book.ProjectID).Scan(&memories); err != nil || memories != 2 {
		t.Fatal("missing/duplicate memories", memories, err)
	}
	mem := &service.Memory{IP: gen.IP}
	pack, err := mem.Compile(ctx, actor, book.ProjectID, book.BranchID, service.MemoryCompileInput{Purpose: "writer", StoryTime: 100, NarrativeTime: 100, Timeline: "main", Budget: 8192})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.Visibility != "public" {
		t.Fatal("chapter memory visibility", len(pack.Items), err)
	}
	if _, err = mem.Compile(ctx, actor, book.ProjectID, book.BranchID, service.MemoryCompileInput{Purpose: "writer", Perspective: "missing-character", StoryTime: 100, NarrativeTime: 100, Budget: 8192}); err == nil {
		t.Fatal("invented perspective admitted")
	}
	if _, err = mem.Compile(ctx, actor, book.ProjectID, book.BranchID, service.MemoryCompileInput{Purpose: "npc", StoryTime: 100, NarrativeTime: 100, Budget: 8192, Save: "save"}); err == nil {
		t.Fatal("NPC without identity admitted")
	}
	updated, err := chapters.Read(ctx, actor.ID, book.ID, chapter)
	if err != nil || updated.BaseHead != release.ID || updated.Document.Text != draft.Document.Text {
		t.Fatal("published draft not rebased", err)
	}
	var ipHead string
	if err = db.QueryRow("SELECT head_revision FROM branches WHERE id=?", book.BranchID).Scan(&ipHead); err != nil || ipHead != draft.Document.IPHead {
		t.Fatal("chapter publication rewrote static IP", err)
	}
	// Simulate a superseded source head in this isolated fixture, then restore
	// it before backup. A stale projection must not survive even an existing cache.
	if _, err = db.Exec("UPDATE resources SET head_revision=? WHERE id=?", service.NewID(), draft.Document.BodyID); err != nil {
		t.Fatal(err)
	}
	stalePack, staleErr := mem.Compile(ctx, actor, book.ProjectID, book.BranchID, service.MemoryCompileInput{Purpose: "writer", StoryTime: 100, NarrativeTime: 100, Timeline: "main", Budget: 8192})
	if _, err = db.Exec("UPDATE resources SET head_revision=? WHERE id=?", release.ID, draft.Document.BodyID); err != nil {
		t.Fatal(err)
	}
	if staleErr != nil || len(stalePack.Items) != 0 {
		t.Fatal("stale chapter memory selected", staleErr)
	}
	exports := &service.NovelExport{IP: gen.IP}
	exportInput := service.NovelExportInput{Source: "published", Author: "出版验证作者", Language: "zh-Hans", Format: "txt", AllowIncomplete: true}
	exportPreview, exportErr := exports.Preview(ctx, actor.ID, book.ID, exportInput)
	if exportErr != nil || exportPreview.Included != 1 {
		t.Fatal("published export preview", exportPreview, exportErr)
	}
	exportInput.PreviewHash = exportPreview.Hash
	exported, _, exportErr := exports.Download(ctx, actor.ID, book.ID, exportInput)
	if exportErr != nil || !strings.Contains(string(exported), draft.Document.Text) {
		t.Fatal("published export body mismatch", exportErr)
	}
	t.Logf("Qualified local critic and atomic chapter publication passed; %d local protocol calls, no paid provider", protocol.calls)
}
