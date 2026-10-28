package novel

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
)

// A card is a derived author aid. It is never a canonical chapter, a memory
// assertion, or a replacement for the complete required planning inputs.
type WritingCard struct {
	Title         string                 `json:"title"`
	Goal          string                 `json:"goal"`
	Mainline      string                 `json:"mainline"`
	Theme         string                 `json:"theme"`
	ReaderPromise string                 `json:"reader_promise"`
	Perspective   string                 `json:"perspective"`
	People        string                 `json:"people"`
	SceneGoals    []string               `json:"scene_goals"`
	Guardrails    []string               `json:"guardrails"`
	RecentPlan    []WritingPlanCue       `json:"recent_plan"`
	Sources       []domain.DependencyRef `json:"sources"`
	Notes         []string               `json:"notes"`
}

type WritingPlanCue struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	Change     string `json:"change"`
	Transition string `json:"transition"`
}

type WritingCardPreview struct {
	Revision int64       `json:"revision"`
	Hash     string      `json:"hash"`
	Card     WritingCard `json:"card"`
}

func writingCue(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		return string(runes[:limit]) + "…（摘要，完整内容见规划）"
	}
	return string(runes)
}

func BuildWritingCard(setup Setup, draft ChapterDocument, inputs []domain.RequiredInput) (WritingCard, error) {
	card := WritingCard{Title: draft.Title, Theme: setup.Theme, ReaderPromise: writingCue(setup.Include, 800), Perspective: draft.Perspective, SceneGoals: []string{}, Guardrails: []string{}, RecentPlan: []WritingPlanCue{}, Sources: []domain.DependencyRef{}, Notes: []string{"近期安排来自已确认规划，不证明剧情已经发生；实际承接以已采用记忆和账本为准。", "本卡仅作摘要，完整必要规划继续随写作请求提供，人物知情范围仍由资料权限约束。"}}
	if card.Perspective == "" {
		card.Perspective = setup.Perspective
	}
	if setup.Avoid != "" {
		card.Guardrails = append(card.Guardrails, writingCue(setup.Avoid, 800))
	}
	if preset, ok := SetupPresetFor(setup); ok {
		if card.ReaderPromise == "" {
			card.ReaderPromise = preset.ReaderPromise
		}
		card.Guardrails = append(card.Guardrails, preset.Guidance...)
	}
	detailFound := false
	for _, input := range inputs {
		var plan PlanningDocument
		if json.Unmarshal([]byte(input.Text), &plan) != nil {
			return card, domain.ErrValidation
		}
		card.Sources = append(card.Sources, input.Dependency.Source)
		switch plan.Kind {
		case "world":
			card.Mainline = writingCue(plan.Mainline, 800)
			for _, text := range []string{plan.Boundaries, plan.Costs} {
				if text != "" {
					card.Guardrails = append(card.Guardrails, writingCue(text, 800))
				}
			}
		case "book":
			if plan.Mainline != "" {
				card.Mainline = writingCue(plan.Mainline, 800)
			}
			if plan.Theme != "" {
				card.Theme = writingCue(plan.Theme, 800)
			}
		case "volume":
			for i, chapter := range plan.Chapters {
				if chapter.ID != draft.ChapterID {
					continue
				}
				for _, previous := range plan.Chapters[max(0, i-3):i] {
					card.RecentPlan = append(card.RecentPlan, WritingPlanCue{ID: previous.ID, Title: previous.Title, Goal: writingCue(previous.Goal, 240), Change: writingCue(previous.Change, 240), Transition: writingCue(previous.Transition, 240)})
				}
			}
		case "detail":
			if plan.Scope != draft.ChapterID || detailFound {
				return card, domain.ErrValidation
			}
			detailFound = true
			card.Goal, card.People = writingCue(plan.Summary, 800), writingCue(plan.Characters, 800)
			for i, scene := range plan.Scenes {
				if i >= 12 {
					card.Notes = append(card.Notes, "更多场景请查看完整细纲。")
					break
				}
				card.SceneGoals = append(card.SceneGoals, writingCue(strings.Join([]string{scene.Goal, scene.Conflict, scene.Outcome, "知情边界：" + scene.Knowledge}, "；"), 500))
			}
		}
	}
	if !detailFound {
		return card, domain.ErrNotReady
	}
	return card, nil
}

func WritingCardQuery(card WritingCard, notes string) string {
	// This is only a retrieval query. Required text is never shortened here.
	parts := []string{card.Title, card.Goal, card.People}
	parts = append(parts, card.SceneGoals...)
	parts = append(parts, notes)
	runes := []rune(strings.Join(parts, "\n"))
	if len(runes) > 1000 {
		runes = runes[:1000]
	}
	return string(runes)
}
