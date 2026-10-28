package retention

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

func TestChapterResearchSourceSeparatesDraftAndEvidence(t *testing.T) {
	pin := domain.DependencyRef{Kind: "resource", ID: strings.Repeat("a", 32), BranchID: strings.Repeat("b", 32), Revision: strings.Repeat("c", 32), Hash: strings.Repeat("d", 64)}
	chapter := novel.ChapterDocument{Schema: 1, ChapterID: strings.Repeat("e", 32), BodyID: strings.Repeat("f", 32), Detail: pin, SettingsRevision: 1, SettingsHash: pin.Hash, Text: "门外有人。", StoryTime: 9007199254740991, References: []novel.ChapterReference{{Source: pin, Quote: "湿纸不能用力揭开"}}}
	stored, _ := json.Marshal(chapter)
	if _, err := novel.DecodeChapter(stored); err != nil {
		t.Fatal("invalid fixture", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stored, &fields); err != nil {
		t.Fatal(err)
	}
	fields["reference_materials"], _ = json.Marshal([]domain.RequiredInput{{Text: "湿纸不能用力揭开"}})
	input, _ := json.Marshal(fields)
	got, materials, err := chapterResearchSourceInput(input, financialDigest(stored))
	if err != nil || got.StoryTime != chapter.StoryTime || len(materials) != 1 || materials[0].Text != chapter.References[0].Quote {
		t.Fatal("reference wrapper changed draft identity", err)
	}
	for _, raw := range []string{
		strings.Replace(string(input), "门外有人。", "门外无人。", 1),
		`{"unknown":true,` + string(input)[1:],
		`{"text":"伪造",` + string(input)[1:],
		string(stored),
	} {
		if _, _, err := chapterResearchSourceInput([]byte(raw), financialDigest(stored)); err == nil {
			t.Fatal("changed, ambiguous or missing reference accepted")
		}
	}
	if _, err := novel.DecodeChapter(input); err == nil {
		t.Fatal("generation-only materials entered stored chapter")
	}
}

func TestChapterMethodDraftSourcePreservesContent(t *testing.T) {
	chapter := novel.ChapterDocument{Schema: 1, ChapterID: strings.Repeat("a", 32), Text: "门外有人。", StoryTime: 9007199254740991}
	stored, _ := json.Marshal(chapter)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(stored))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	normalized, _ := json.Marshal(value)
	var decoded novel.ChapterDocument
	if bytes.Equal(stored, normalized) {
		t.Fatal("test must reorder fields")
	}
	if err := contentJSON(normalized, &decoded, financialDigest(stored)); err != nil {
		t.Fatal("unchanged chapter rejected", err)
	}
	for _, raw := range []string{
		strings.Replace(string(normalized), "门外有人。", "门外无人。", 1),
		strings.Replace(string(normalized), "9007199254740991", "9007199254740990", 1),
		`{"unknown":true,` + string(normalized)[1:],
		`{"text":"伪造",` + string(normalized)[1:],
	} {
		if contentJSON([]byte(raw), &decoded, financialDigest(stored)) == nil {
			t.Fatal("changed or ambiguous chapter accepted")
		}
	}
}

func TestWritingCardSourceRequiresExactOwnedDerivation(t *testing.T) {
	setup := novel.Defaults()
	setup.Include = "根据证据逐步揭示真相"
	chapter := novel.ChapterDocument{ChapterID: strings.Repeat("a", 32), Title: "旧账"}
	detail, _ := json.Marshal(novel.PlanningDocument{Kind: "detail", Scope: chapter.ChapterID, Summary: "核对旧账", Characters: "林舟"})
	plans := []domain.RequiredInput{{Text: string(detail)}}
	card, err := novel.BuildWritingCard(setup, chapter, plans)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"writing_card": card})
	if err = validateChapterWritingCard(raw, setup, chapter, plans); err != nil {
		t.Fatal("unchanged derived card rejected", err)
	}
	card.Goal = "把未经作者确认的秘密写入剧情"
	raw, _ = json.Marshal(map[string]any{"writing_card": card})
	if validateChapterWritingCard(raw, setup, chapter, plans) == nil {
		t.Fatal("card became an unowned prompt channel")
	}
}
