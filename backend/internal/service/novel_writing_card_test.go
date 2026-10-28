package service

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

func TestNovelWritingCardUsesCurrentPlansWithoutFutureLeak(t *testing.T) {
	chapter := strings.Repeat("a", 32)
	setup := novel.Defaults()
	setup.Theme, setup.Include, setup.Avoid = "选择的代价", "用调查和行动推进真相", "不要让角色凭空知道答案"
	draft := novel.ChapterDocument{ChapterID: chapter, Title: "查验旧账", Perspective: "hero"}
	volume := novel.PlanningDocument{Kind: "volume", Chapters: []novel.PlannedChapter{
		{ID: strings.Repeat("b", 32), Title: "旧人来信", Goal: "找到线索", Change: "获得账本"},
		{ID: chapter, Title: draft.Title},
		{ID: strings.Repeat("c", 32), Title: "future-secret", Goal: "future-secret"},
	}}
	detail := novel.PlanningDocument{Kind: "detail", Scope: chapter, Summary: "在档案馆核对旧账", Characters: "林舟与档案员", Scenes: []novel.PlannedScene{{Goal: "核对印章", Knowledge: "只知道账本有缺页", Conflict: "记录与口供不同", Outcome: "发现日期矛盾"}}}
	plans := []domain.RequiredInput{}
	for _, doc := range []novel.PlanningDocument{{Kind: "world", Mainline: "追查失踪案", Boundaries: "不能将传言当事实"}, {Kind: "book", Theme: setup.Theme}, volume, detail} {
		raw, _ := json.Marshal(doc)
		plans = append(plans, domain.RequiredInput{Text: string(raw)})
	}
	card, err := buildNovelWritingCard(setup, draft, plans)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(card)
	if card.Goal != detail.Summary || card.People != detail.Characters || card.ReaderPromise != setup.Include || len(card.RecentPlan) != 1 || strings.Contains(string(raw), "future-secret") || len(card.SceneGoals) != 1 {
		t.Fatalf("wrong chapter material: %s", raw)
	}
	query := novelWritingCardQuery(card, "不要解释太多"+strings.Repeat("字", 2000))
	if !strings.Contains(query, "档案馆") || !strings.Contains(query, "林舟") || utf8.RuneCountInString(query) > 1000 {
		t.Fatal("chapter retrieval query lost its task or exceeded budget")
	}
	detail.Scope = strings.Repeat("d", 32)
	raw, _ = json.Marshal(detail)
	plans[len(plans)-1].Text = string(raw)
	if _, err = buildNovelWritingCard(setup, draft, plans); err == nil {
		t.Fatal("another chapter's detail became the writing card")
	}
}

func TestNovelWritingCardEnvelopeKeepsCanonicalChapter(t *testing.T) {
	doc := novel.ChapterDocument{Schema: 1, ChapterID: strings.Repeat("a", 32), BodyID: strings.Repeat("b", 32), Detail: domain.DependencyRef{Kind: "resource", BranchID: strings.Repeat("c", 32), ID: strings.Repeat("d", 32), Revision: strings.Repeat("e", 32), Hash: strings.Repeat("f", 64)}, SettingsRevision: 1, SettingsHash: strings.Repeat("a", 64), Title: "旧账", Text: "他打开账本。", StoryTime: 2}
	raw, _ := json.Marshal(doc)
	wrapped, err := attachNovelWritingCard(raw, NovelWritingCard{Title: doc.Title, Goal: "核对账本"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, plain, _, err := decodeChapterResearchDraft(wrapped)
	if err != nil || decoded.Text != doc.Text || digest(string(plain)) != digest(string(raw)) {
		t.Fatal("derived writing material changed the author's chapter", err)
	}
	if _, err = novel.DecodeChapter(wrapped); err == nil {
		t.Fatal("writing-card envelope was accepted as persisted chapter data")
	}
}
