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

func assertNovelChapterLedger(t *testing.T, gen *service.Generations, knowledge *service.Knowledge, actor domain.Actor, book service.NovelBook, price service.PriceCard, chapter string, publish bool) service.Generation {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := chapters.OpenLedger(ctx, actor.ID, book.ID, chapter)
	if err != nil {
		t.Fatal(err)
	}
	again, err := chapters.OpenLedger(ctx, actor.ID, book.ID, chapter)
	if err != nil || again.Resource.ID != ledger.Resource.ID {
		t.Fatal("duplicate ledger identity", err)
	}
	output := novel.LedgerOutput{Summary: "林舟在门外停留", Entries: []novel.LedgerEntry{{Kind: "event", Context: "reality", SubjectName: "林舟", Predicate: "停留", Statement: "林舟在门外停了下来", StoryTime: 1, KnownFrom: 1, Visibility: "author", KnownBy: []string{}, Quotes: []string{"林舟在门外停了下来。"}}}, Notes: []string{"人物尚未关联已确认身份，请核对。"}}
	content, _ := json.Marshal(output)
	response, _ := json.Marshal(map[string]any{"id": "ledger-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	original := gen.Adapter
	gen.Adapter = models.New(transport)
	defer func() { gen.Adapter = original }()
	w := &service.NovelChapterWriter{Generations: gen, Knowledge: knowledge}
	in := service.NovelChapterWritingInput{Revision: draft.Revision, Hash: draft.Hash, Notes: "只提取正文实际发生的变化，保留证据", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "chapter-bookkeeper-once"}
	if !publish {
		in.AgentVersionID = chapterEditableAgent(t, gen.IP, actor, book, chapter, "bookkeeper")
	}
	task, err := w.PrepareBookkeeper(ctx, actor.ID, book.ID, chapter, in)
	if err != nil || task.Frozen.Role != "bookkeeper" || task.Frozen.NovelChapter == nil || task.Frozen.NovelDetail != nil {
		t.Fatal("bookkeeper preparation", err)
	}
	replay, err := w.PrepareBookkeeper(ctx, actor.ID, book.ID, chapter, in)
	if err != nil || replay.ID != task.ID {
		t.Fatal("bookkeeper replay", err)
	}
	requestRaw, err := gen.IP.Blobs.ReadObject(ctx, book.ProjectID, task.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var request models.Request
	if json.Unmarshal(requestRaw, &request) != nil {
		t.Fatal("invalid request")
	}
	var source domain.NovelChapterRequest
	prompt := request.Prompt
	if !publish {
		if task.Frozen.Capability == nil || task.Frozen.Capability.AgentVersionID != in.AgentVersionID || task.Frozen.Capability.Mode != "TRIAL" {
			t.Fatal("bookkeeper lost selected method")
		}
		open, close, _ := models.CapabilityMethodFrame(true)
		_, rest, ok := strings.Cut(prompt, open)
		if !ok {
			t.Fatal("bookkeeper method missing")
		}
		payload, _, ok := strings.Cut(rest, close)
		var frame struct {
			Input json.RawMessage `json:"input"`
		}
		if !ok || json.Unmarshal([]byte(payload), &frame) != nil {
			t.Fatal("bookkeeper method input missing")
		}
		prompt = string(frame.Input)
	}
	if json.NewDecoder(strings.NewReader(prompt)).Decode(&source) != nil || len(source.Plans) != 0 {
		t.Fatal("bookkeeper inherited future planning")
	}
	if strings.Contains(string(source.Draft), `"writing_card"`) || strings.Contains(request.System, "writing_card") {
		t.Fatal("bookkeeper inherited the writer-only derived planning card")
	}
	if !strings.Contains(string(source.Draft), draft.Document.Text) || !strings.Contains(request.System+request.Prompt, "独立记账员") {
		t.Fatal("actual chapter/preset missing")
	}
	if transport.calls.Load() != 0 {
		t.Fatal("prepare dispatched model")
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("bookkeeper execution", state, e)
	}
	if !publish && !strings.Contains(transport.payload, "作者定制") {
		t.Fatal("custom bookkeeper method not executed")
	}
	candidate, err := w.LedgerSuggestion(ctx, actor.ID, book.ID, chapter, task.ID)
	if err != nil || len(candidate.Document.Entries) != 1 || candidate.Document.Entries[0].Evidence[0].Start != 0 {
		t.Fatal("bookkeeper candidate", err)
	}
	unchanged, err := chapters.ReadLedger(ctx, actor.ID, book.ID, chapter)
	if err != nil || unchanged.Revision != 0 {
		t.Fatal("preview saved ledger", err)
	}
	forged := candidate
	forged.Document.Entries = append([]novel.LedgerEntry{}, candidate.Document.Entries...)
	forged.Document.Entries[0].SubjectID = "invented-person"
	if _, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, forged); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("fabricated entity accepted", err)
	}
	forged.Document.Entries[0].SubjectID = ""
	forged.Document.Entries[0].Quotes = []string{"正文没有此句。"}
	if _, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, forged); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("fabricated quote accepted", err)
	}
	var saved service.NovelLedgerDraft
	if node := os.Getenv("STUDIO_LEDGER_BROWSER_NODE"); node != "" {
		web, e := filepath.Abs("../../../web")
		if e != nil {
			t.Fatal(e)
		}
		server := httptest.NewUnstartedServer(nil)
		origin := "http://" + server.Listener.Addr().String()
		app := httpapi.New(gen.IP.Workspace.DB, origin)
		app.Static("/", filepath.Join(web, "dist"))
		server.Config.Handler = app
		server.Start()
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, e := exec.CommandContext(browserCtx, node, filepath.Join(web, "tests", "novel-ledger-browser.cjs"), origin, task.ID, book.Setup.Title).CombinedOutput()
		if e != nil {
			t.Fatalf("ledger browser: %v\n%s", e, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		saved, err = chapters.ReadLedger(ctx, actor.ID, book.ID, chapter)
	} else {
		saved, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, candidate)
	}
	if err != nil || !saved.Ready || len(saved.Document.Entries) != 1 || saved.Document.GenerationID != task.ID {
		t.Fatal("explicit ledger save", err)
	}
	if _, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, candidate); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale ledger editor overwrote changes", err)
	}
	_, err = gen.IP.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: draft.Document.IPHead, OperationID: "ledger-publication-bypass", Updates: []domain.ResourceUpdate{{ResourceID: saved.Resource.ID, Text: "unreviewed ledger", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}}})
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("ledger bypassed joint publication", err)
	}
	publicationInput := service.NovelChapterReleaseInput{Revision: draft.Revision, Hash: draft.Hash, LedgerRevision: saved.Revision, OperationID: "chapter-joint-review"}
	publication, err := chapters.PrepareRelease(ctx, actor.ID, book.ID, chapter, publicationInput)
	if err != nil || len(publication.Manifest.Artifacts) != 3 {
		t.Fatal("chapter joint review candidate", err)
	}
	publicationAgain, err := chapters.PrepareRelease(ctx, actor.ID, book.ID, chapter, publicationInput)
	if err != nil || publicationAgain.ID != publication.ID {
		t.Fatal("chapter release replay", err)
	}
	readRelease, err := gen.IP.ReadRelease(ctx, actor, book.ProjectID, publication.ID)
	if err != nil || !readRelease.RequiresSemanticReview {
		t.Fatal("chapter must require independent review", err)
	}
	var writerSource, keeperSource bool
	for _, artifact := range publication.Manifest.Artifacts {
		if artifact.Origin == nil || artifact.Origin.Generation == nil {
			continue
		}
		writerSource = writerSource || artifact.Origin.Generation.GenerationID == draft.Document.WriterGenerationID
		keeperSource = keeperSource || artifact.Origin.Generation.GenerationID == task.ID
	}
	if !writerSource || !keeperSource {
		t.Fatal("joint review lost writer/bookkeeper ancestry")
	}
	reviews := &service.SemanticReviews{Generations: gen}
	review, err := reviews.Prepare(ctx, actor, book.ProjectID, service.SemanticReviewInput{OperationID: "chapter-independent-review", TargetKind: "artifact_release", TargetID: publication.ID, TargetHash: publication.Hash, BranchID: book.BranchID, MaxOutputTokens: 2048})
	if err != nil || review.State != "BLOCKED" {
		t.Fatal("missing reviewer must retain blocked review without calling model", err)
	}
	reviewRaw, err := gen.IP.Blobs.ReadObject(ctx, book.ProjectID, review.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var reviewDoc domain.SemanticReviewDocument
	if json.Unmarshal(reviewRaw, &reviewDoc) != nil {
		t.Fatal("bad review document")
	}
	var planAnchors int
	for _, anchor := range reviewDoc.Anchors {
		if strings.HasPrefix(anchor.Key, "chapter-plan:") {
			planAnchors++
		}
	}
	if planAnchors != 4 || len(review.Frozen.SourceGenerationIDs) != 2 {
		t.Fatal("independent reviewer missing actual world/book/volume/detail or both source identities", planAnchors, review.Frozen.SourceGenerationIDs)
	}
	if len(draft.Document.References) > 0 {
		found := false
		for _, anchor := range reviewDoc.Anchors {
			if strings.HasPrefix(anchor.Key, "chapter-research:") && strings.Contains(anchor.Text, "湿纸不能用力揭开") && strings.Contains(anchor.Text, "需核对故事年代的纸张材质") {
				for _, key := range reviewDoc.RequiredCoverage {
					found = found || key == anchor.Key
				}
			}
		}
		if !found {
			t.Fatal("independent review omitted required reference coverage")
		}
		for _, artifact := range publication.Manifest.Artifacts {
			if artifact.Dependencies == nil || len(artifact.Dependencies.Inputs) != 2 || artifact.Dependencies.Inputs[1].Source != draft.Document.References[0].Source {
				t.Fatal("chapter publication lost reference propagation", artifact.ResourceID)
			}
		}
	}
	if _, err = gen.IP.AdoptRelease(ctx, actor, book.ProjectID, publication.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("unreviewed chapter adopted", err)
	}
	// Saving even a small ledger edit invalidates the exact candidate being reviewed.
	saved.Document.Summary += "，等待作者核对"
	saved, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.IP.AdoptRelease(ctx, actor, book.ProjectID, publication.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("review candidate survived ledger edit", err)
	}
	if _, err = chapters.PrepareRelease(ctx, actor.ID, book.ID, chapter, service.NovelChapterReleaseInput{Revision: draft.Revision, Hash: draft.Hash, LedgerRevision: saved.Revision - 1, OperationID: "chapter-old-ledger"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale ledger selection accepted", err)
	}
	if publish {
		assertChapterPublication(t, gen, knowledge, actor, book, price, chapter)
		draft, err = chapters.Read(ctx, actor.ID, book.ID, chapter)
		if err != nil {
			t.Fatal(err)
		}
	}
	draft.Document.Text += "窗外的雨忽然停了。"
	if _, err = chapters.Save(ctx, actor.ID, book.ID, chapter, draft); err != nil {
		t.Fatal(err)
	}
	stale, err := chapters.ReadLedger(ctx, actor.ID, book.ID, chapter)
	expectedEntries := 1
	if publish {
		expectedEntries = 2
	}
	if err != nil || stale.Ready || len(stale.Document.Entries) != expectedEntries {
		t.Fatal("stale ledger erased or marked current", err)
	}
	if _, err = w.LedgerSuggestion(ctx, actor.ID, book.ID, chapter, task.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old bookkeeper reused for new prose", err)
	}
	if _, err = chapters.SaveLedger(ctx, actor.ID, book.ID, chapter, saved); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old ledger applied to new prose", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected bookkeeper calls")
	}
	return task
}
