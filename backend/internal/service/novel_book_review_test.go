package service

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"strings"
	"testing"
)

func TestBookReviewWindowsAndCoverage(t *testing.T) {
	p := NovelExportPreview{}
	m := novel.ExportBook{}
	for i := 0; i < 10; i++ {
		p.Chapters = append(p.Chapters, NovelExportChapter{ID: string(rune('a' + i)), VolumeID: "volume", Included: true})
		m.Chapters = append(m.Chapters, novel.ExportChapter{Text: "正文"})
	}
	w, err := reviewWindows(p, m)
	if err != nil || len(w) != 2 || len(w[0]) != 8 || len(w[1]) != 2 {
		t.Fatal(w, err)
	}
	m.Chapters[0].Text = strings.Repeat("a", novelReviewWindowBytes+1)
	if _, err = reviewWindows(p, m); err == nil {
		t.Fatal("silently truncated chapter")
	}
	root := novelReviewRoot{Kind: novelReviewKind, BookID: "book", Preview: p}
	r := RecipeExecution{}
	for i, window := range w {
		ids := []string{}
		for _, ch := range window {
			ids = append(ids, ch.ID)
		}
		raw, _ := json.Marshal(novelReviewInput{BookID: "book", ChapterIDs: ids, Previous: json.RawMessage(`{}`)})
		step := RecipeStepRun{Key: []string{"review-1", "review-2"}[i], Ordinal: i, Active: true, Frozen: RecipeStepFrozen{Input: RecipeStepInput{ContextID: "context", Input: raw}, Agent: domain.CapabilityVersion{Definition: domain.CapabilityDefinition{Role: "critic"}}}}
		if i > 0 {
			step.Frozen.Input.Bindings = []domain.RecipeValueBinding{{TargetPointer: "/previous", SourceStep: "review-1"}}
		}
		r.Steps = append(r.Steps, step)
	}
	if err = validateNovelReviewSteps(r, root); err != nil {
		t.Fatal(err)
	}
	r.Steps[1].Frozen.Input.Bindings[0].TargetPointer = "/chapter_ids"
	if err = validateNovelReviewSteps(r, root); err == nil {
		t.Fatal("overwritten chapter identity")
	}
	r.Steps = r.Steps[:1]
	if err = validateNovelReviewSteps(r, root); err == nil {
		t.Fatal("missing chapters accepted")
	}
}
