package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

// Reuses the ordered planning/chapter fixture, and exits before backup/restore.
func TestNovelChapterWritingCardHTTP(t *testing.T) { runNovelPlanning(t, false) }

func assertNovelWritingCardHTTP(t *testing.T, b *browser, gen *service.Generations, actor domain.Actor, book service.NovelBook, chapterID, nextChapterID string, plans []service.NovelPlanDraft) {
	t.Helper()
	ctx := context.Background()
	chapters := &service.NovelChapters{IP: gen.IP}
	r, err := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	db := gen.IP.Workspace.DB
	var tasksBefore, callsBefore, tasksAfter, callsAfter int
	if err = db.QueryRow("SELECT (SELECT COUNT(*) FROM generation_tasks),(SELECT COUNT(*) FROM model_calls)").Scan(&tasksBefore, &callsBefore); err != nil {
		t.Fatal(err)
	}
	budgets := &service.Budgets{Workspace: gen.IP.Workspace}
	budgetBefore, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/novels/" + book.ID + "/chapters/" + r.ID + "/writing-card"
	decode := func(value map[string]any) service.NovelWritingCardPreview {
		t.Helper()
		raw, e := json.Marshal(value)
		var card service.NovelWritingCardPreview
		if e != nil || json.Unmarshal(raw, &card) != nil {
			t.Fatal("invalid writing card response", e)
		}
		return card
	}
	preview := decode(b.request("GET", path, nil, 200))
	again := decode(b.request("GET", path, nil, 200))
	if !reflect.DeepEqual(preview, again) || preview.Revision != draft.Revision || preview.Hash != draft.Hash || preview.Card.Title != draft.Document.Title || preview.Card.Goal != "找到师父留下的纸条" || len(preview.Card.SceneGoals) != 1 || !strings.Contains(preview.Card.SceneGoals[0], "只知道师父失踪") {
		t.Fatal("card lost the selected chapter or private version", preview)
	}
	if len(preview.Card.Sources) != 4 || len(preview.Card.RecentPlan) != 0 || strings.Contains(preview.Card.Goal, "蓝匣谜底") {
		t.Fatal("current chapter inherited future detail", preview.Card)
	}
	want := map[string]domain.DependencyRef{}
	for _, plan := range plans {
		ref := dependencyFromCatalog(t, b, book.ProjectID, "resource", plan.Resource.ID)
		raw, _ := json.Marshal(ref)
		var source domain.DependencyRef
		if json.Unmarshal(raw, &source) != nil || source.Revision != plan.Resource.Head {
			t.Fatal("catalog no longer pins confirmed planning")
		}
		want[source.ID] = source
	}
	for _, source := range preview.Card.Sources {
		if source != want[source.ID] || source.BranchID != book.BranchID {
			t.Fatal("card source version/hash drift", source, want[source.ID])
		}
		delete(want, source.ID)
	}
	if len(want) != 0 {
		t.Fatal("card omitted a required planning source")
	}
	retained, err := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || !reflect.DeepEqual(retained, draft) {
		t.Fatal("card GET altered author's draft", err)
	}
	budgetAfter, err := budgets.Read(ctx, actor.ID)
	if err != nil || budgetAfter != budgetBefore {
		t.Fatal("card GET changed model budget", err)
	}
	if err = db.QueryRow("SELECT (SELECT COUNT(*) FROM generation_tasks),(SELECT COUNT(*) FROM model_calls)").Scan(&tasksAfter, &callsAfter); err != nil || tasksAfter != tasksBefore || callsAfter != callsBefore {
		t.Fatal("card GET created a generation/model call", err)
	}

	// The next chapter has a different detail and can see a prior *plan* cue.
	next, err := chapters.Open(ctx, actor.ID, book.ID, nextChapterID)
	if err != nil {
		t.Fatal(err)
	}
	nextCard := decode(b.request("GET", "/api/novels/"+book.ID+"/chapters/"+next.ID+"/writing-card", nil, 200))
	if nextCard.Card.Goal != "仅下一章可用的蓝匣谜底" || len(nextCard.Card.RecentPlan) != 1 || nextCard.Card.RecentPlan[0].ID != chapterID || !strings.Contains(strings.Join(nextCard.Card.Notes, " "), "不证明剧情已经发生") {
		t.Fatal("next chapter card mixed current/future details or promoted planned events to facts", nextCard.Card)
	}

	member := b.request("POST", "/api/members", map[string]any{"username": "writing-card-reader", "password": "writing-card-test-password", "role": "reader"}, 201)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "writing-card-reader", "password": "writing-card-test-password"}, 200)
	reader.request("GET", path, nil, 403)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader.request("GET", "/api/novels/"+book.ID, nil, 200)
	reader.request("GET", path, nil, 403)

	entry := &service.NovelEntry{Workspace: gen.IP.Workspace}
	otherSetup := book.Setup
	otherSetup.Title = "另一本书的隔离检查"
	otherDraft, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: otherSetup})
	if err != nil {
		t.Fatal(err)
	}
	otherBook, err := entry.Confirm(ctx, actor.ID, otherDraft.ID, service.NovelConfirmation{Revision: otherDraft.Revision, OperationID: "writing-card-other-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/novels/"+otherBook.ID+"/chapters/"+r.ID+"/writing-card", nil, 403)
}

func assertCapturedChapterWritingCard(t *testing.T, gen *service.Generations, actor domain.Actor, book service.NovelBook, resource string, task service.Generation) {
	t.Helper()
	ctx := context.Background()
	raw, err := gen.IP.Blobs.ReadObject(ctx, book.ProjectID, task.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var request models.Request
	if json.Unmarshal(raw, &request) != nil {
		t.Fatal("invalid frozen writer request")
	}
	prompt := request.Prompt
	if task.Frozen.Capability != nil && task.Frozen.Capability.Mode == "TRIAL" {
		open, close, _ := models.CapabilityMethodFrame(true)
		_, rest, ok := strings.Cut(prompt, open)
		if !ok {
			t.Fatal("selected writer method frame missing")
		}
		payload, _, ok := strings.Cut(rest, close)
		var frame struct {
			Input json.RawMessage `json:"input"`
		}
		if !ok || json.Unmarshal([]byte(payload), &frame) != nil {
			t.Fatal("selected writer input missing")
		}
		prompt = string(frame.Input)
	}
	var input domain.NovelChapterRequest
	if json.NewDecoder(strings.NewReader(prompt)).Decode(&input) != nil || len(input.Plans) != 4 {
		t.Fatal("writer omitted complete four planning inputs")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input.Draft, &fields) != nil {
		t.Fatal("writer draft is not JSON")
	}
	var chapter novel.ChapterDocument
	if json.Unmarshal(input.Draft, &chapter) != nil {
		t.Fatal("writer lost chapter identity")
	}
	var card service.NovelWritingCard
	if json.Unmarshal(fields["writing_card"], &card) != nil {
		t.Fatal("writer omitted derived card")
	}
	preview, err := (&service.NovelChapters{IP: gen.IP}).WritingCard(ctx, actor.ID, book.ID, resource)
	if err != nil || !reflect.DeepEqual(card, preview.Card) {
		t.Fatal("writer card differs from author's current card", err)
	}
	seen := map[string]bool{}
	for _, plan := range input.Plans {
		var document novel.PlanningDocument
		if json.Unmarshal([]byte(plan.Text), &document) != nil || document.Kind == "" {
			t.Fatal("required planning was replaced by summary")
		}
		if document.Kind == "detail" && document.Scope != chapter.ChapterID {
			t.Fatal("writer inherited another chapter detail")
		}
		seen[document.Kind] = true
	}
	for _, kind := range []string{"world", "book", "volume", "detail"} {
		if !seen[kind] {
			t.Fatal("writer missing full plan", kind)
		}
	}
	if !strings.Contains(request.System, "recent_plan是近期规划，不代表事件已发生") {
		t.Fatal("writer treats planned events as past facts")
	}
	delete(fields, "writing_card")
	delete(fields, "reference_materials")
	plain, _ := json.Marshal(fields)
	document, err := novel.DecodeChapter(plain)
	if err != nil {
		t.Fatal("derived writer envelope cannot recover strict canonical chapter", err)
	}
	canonical, _ := json.Marshal(document)
	hash := sha256.Sum256(canonical)
	if hex.EncodeToString(hash[:]) != task.Frozen.NovelChapter.Hash {
		t.Fatal("adding the card changed canonical draft hash")
	}
}
