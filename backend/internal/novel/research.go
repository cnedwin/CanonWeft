package novel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

type ResearchSource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Revision   string `json:"revision"`
	Hash       string `json:"hash"`
	URL        string `json:"url"`
	Locator    string `json:"locator"`
	CapturedAt string `json:"captured_at"`
}
type ResearchCitation struct {
	SourceID   string `json:"source_id"`
	URL        string `json:"url"`
	Locator    string `json:"locator"`
	Evidence   string `json:"evidence"`
	CapturedAt string `json:"captured_at"`
}
type WithdrawnResearchSource struct {
	Source ResearchSource `json:"source"`
	Reason string         `json:"reason"`
}
type ResearchFact struct {
	ID           string             `json:"id,omitempty"`
	Approved     bool               `json:"approved,omitempty"`
	Claim        string             `json:"claim"`
	Status       string             `json:"status"`
	Sources      []ResearchCitation `json:"sources"`
	StaleSources []ResearchCitation `json:"stale_sources,omitempty"`
	EraScope     string             `json:"era_scope"`
	RegionScope  string             `json:"region_scope"`
	Limitations  string             `json:"limitations"`
	WritingUse   string             `json:"writing_use"`
	ReviewAfter  string             `json:"review_after"`
}
type ResearchQuery struct {
	Query           string `json:"query"`
	Purpose         string `json:"purpose"`
	PreferredSource string `json:"preferred_source"`
}
type ResearchProposal struct {
	SearchPlan  []ResearchQuery `json:"search_plan"`
	Cards       []ResearchFact  `json:"fact_cards"`
	Conflicts   []string        `json:"conflicts"`
	Gaps        []string        `json:"gaps"`
	SceneChecks []string        `json:"scene_checks"`
}
type ResearchNotebook struct {
	WithdrawnSources []WithdrawnResearchSource `json:"withdrawn_sources,omitempty"`
	Title            string                    `json:"title"`
	Question         string                    `json:"question"`
	Genre            string                    `json:"genre"`
	Era              string                    `json:"era"`
	Region           string                    `json:"region"`
	ChapterNeed      string                    `json:"chapter_need"`
	SourceIDs        []string                  `json:"source_ids"`
	Sources          []ResearchSource          `json:"sources"`
	GenerationID     string                    `json:"generation_id,omitempty"`
	ResearchProposal
}

func ValidateResearch(d ResearchNotebook, texts map[string]string) error {
	valid := func(s string, max int) bool {
		return strings.TrimSpace(s) != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
	}
	if !valid(d.Title, 160) || !valid(d.Question, 4000) || !valid(d.Genre, 120) || !valid(d.Era, 4000) || !valid(d.Region, 4000) || !valid(d.ChapterNeed, 4000) || len(d.SourceIDs) > 12 || len(d.Cards) > 100 || len(d.SearchPlan) > 30 {
		return domain.ErrValidation
	}
	for _, values := range [][]string{d.Conflicts, d.Gaps, d.SceneChecks} {
		if len(values) > 100 {
			return domain.ErrValidation
		}
		for _, v := range values {
			if !valid(v, 4000) {
				return domain.ErrValidation
			}
		}
	}
	for _, q := range d.SearchPlan {
		if !valid(q.Query, 4000) || !valid(q.Purpose, 4000) || !valid(q.PreferredSource, 4000) {
			return domain.ErrValidation
		}
	}
	sources := map[string]ResearchSource{}
	for _, s := range d.Sources {
		if _, ok := sources[s.ID]; ok {
			return domain.ErrValidation
		}
		sources[s.ID] = s
	}
	seen := map[string]bool{}
	if len(d.WithdrawnSources) > 100 {
		return domain.ErrValidation
	}
	for _, retired := range d.WithdrawnSources {
		if !valid(retired.Reason, 2000) || !planID.MatchString(retired.Source.ID) || !planID.MatchString(retired.Source.Revision) || !planHash.MatchString(retired.Source.Hash) || !valid(retired.Source.Name, 160) || !valid(retired.Source.URL, 2000) || !valid(retired.Source.Locator, 2000) || !valid(retired.Source.CapturedAt, 120) {
			return domain.ErrValidation
		}
	}
	for _, f := range d.Cards {
		if !valid(f.ID, 120) || seen[f.ID] || !valid(f.Claim, 4000) || !slices.Contains([]string{"supported", "contested", "inference", "unverified"}, f.Status) || len(f.Sources) > 20 {
			return domain.ErrValidation
		}
		seen[f.ID] = true
		if len(f.StaleSources) > 100 || (f.Approved && len(f.StaleSources) > 0) {
			return fmt.Errorf("%w：请先处理旧版引证，再选择事实卡作为写作参考", domain.ErrValidation)
		}
		for _, cite := range f.StaleSources {
			if !valid(cite.SourceID, 120) || !valid(cite.URL, 2000) || !valid(cite.Locator, 2000) || !valid(cite.CapturedAt, 120) || !valid(cite.Evidence, 2000) {
				return domain.ErrValidation
			}
		}
		for _, value := range []string{f.EraScope, f.RegionScope, f.Limitations, f.WritingUse, f.ReviewAfter} {
			if !valid(value, 4000) {
				return domain.ErrValidation
			}
		}
		if (f.Status == "supported" || f.Status == "contested") && len(f.Sources) == 0 {
			return fmt.Errorf("%w：有支持或有争议的事实卡必须引用实际资料", domain.ErrValidation)
		}
		for _, cite := range f.Sources {
			s, ok := sources[cite.SourceID]
			if !ok || cite.URL != s.URL || cite.Locator != s.Locator || cite.CapturedAt != s.CapturedAt || !valid(cite.Evidence, 2000) || !strings.Contains(texts[s.ID], cite.Evidence) {
				return fmt.Errorf("%w：事实卡引用不在所选资料原文中，或来源版本不符", domain.ErrValidation)
			}
		}
	}
	raw, _ := json.Marshal(d)
	if len(raw) > 220*1024 {
		return fmt.Errorf("%w：研究稿过大，请拆成不同研究问题", domain.ErrValidation)
	}
	return nil
}

func ResearchText(d ResearchNotebook) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n研究问题：%s\n题材：%s\n年代：%s\n地域：%s\n情节需要：%s\n\n作者已选择以下参考卡。证据存在性检查不等于事实已独立核实；外部参考不改变故事正史。\n", d.Title, d.Question, d.Genre, d.Era, d.Region, d.ChapterNeed)
	for _, f := range d.Cards {
		if !f.Approved {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n状态：%s\n年代范围：%s\n地域范围：%s\n限制：%s\n写作用法：%s\n复核条件：%s\n", f.Claim, f.Status, f.EraScope, f.RegionScope, f.Limitations, f.WritingUse, f.ReviewAfter)
		for _, s := range f.Sources {
			fmt.Fprintf(&b, "\n来源：%s\n定位：%s\n资料快照时间：%s\n原文引证：%s\n", s.URL, s.Locator, s.CapturedAt, s.Evidence)
		}
	}
	for _, group := range []struct {
		title string
		items []string
	}{{"冲突", d.Conflicts}, {"资料缺口", d.Gaps}, {"场景核对", d.SceneChecks}} {
		if len(group.items) > 0 {
			fmt.Fprintf(&b, "\n## %s\n", group.title)
			for _, s := range group.items {
				fmt.Fprintf(&b, "- %s\n", s)
			}
		}
	}
	return b.String()
}
