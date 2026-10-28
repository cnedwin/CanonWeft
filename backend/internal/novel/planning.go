package novel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

type PlannedVolume struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}
type PlannedChapter struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Perspective string `json:"perspective"`
	Goal        string `json:"goal"`
	Events      string `json:"events"`
	Change      string `json:"change"`
	Transition  string `json:"transition"`
}
type PlannedScene struct {
	ID            string `json:"id"`
	Time          string `json:"time"`
	Place         string `json:"place"`
	Characters    string `json:"characters"`
	Knowledge     string `json:"knowledge"`
	Goal          string `json:"goal"`
	Conflict      string `json:"conflict"`
	Action        string `json:"action"`
	Outcome       string `json:"outcome"`
	Emotion       string `json:"emotion"`
	Foreshadow    string `json:"foreshadow"`
	Prerequisites string `json:"prerequisites"`
	Hook          string `json:"hook"`
	Words         int    `json:"words"`
}

// One document per world, book, volume or chapter. Canonical content is stored
// as a normal immutable resource release; author drafts use the existing CAS.
type PlanningDocument struct {
	Schema           int                    `json:"schema"`
	Kind             string                 `json:"kind"`
	Scope            string                 `json:"scope"`
	SettingsRevision int64                  `json:"settings_revision"`
	SettingsHash     string                 `json:"settings_hash"`
	IPHead           string                 `json:"ip_head"`
	Inputs           []domain.DependencyRef `json:"inputs"`
	Title            string                 `json:"title"`
	Summary          string                 `json:"summary"`
	Mainline         string                 `json:"mainline"`
	Theme            string                 `json:"theme"`
	WorldRules       string                 `json:"world_rules"`
	Costs            string                 `json:"costs"`
	Boundaries       string                 `json:"boundaries"`
	Setting          string                 `json:"setting"`
	Factions         string                 `json:"factions"`
	Characters       string                 `json:"characters"`
	Arcs             string                 `json:"arcs"`
	TurningPoints    string                 `json:"turning_points"`
	Ending           string                 `json:"ending"`
	OpenQuestions    string                 `json:"open_questions"`
	Volumes          []PlannedVolume        `json:"volumes"`
	Chapters         []PlannedChapter       `json:"chapters"`
	Scenes           []PlannedScene         `json:"scenes"`
}

var planID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var planHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func PlanIdentity(kind, scope string) bool {
	switch kind {
	case "world", "book":
		return scope == ""
	case "volume", "detail":
		return planID.MatchString(scope)
	}
	return false
}
func PlanKind(kind string) string { return "novel_plan_" + kind }
func PlanKindName(kind string) string {
	switch kind {
	case "world":
		return "世界与故事"
	case "book":
		return "全书大纲"
	case "volume":
		return "分卷章节安排"
	case "detail":
		return "章节细纲"
	}
	return ""
}
func (d PlanningDocument) Validate(complete bool) error {
	invalid := func(field string) error {
		return fmt.Errorf("%w：规划的%s尚未正确填写", domain.ErrValidation, field)
	}
	if d.Schema != 1 || !PlanIdentity(d.Kind, d.Scope) || d.SettingsRevision < 1 || !planHash.MatchString(d.SettingsHash) || (d.IPHead != "" && !planID.MatchString(d.IPHead)) {
		return invalid("版本与身份")
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > 256*1024 {
		return invalid("内容大小（每份最多256KiB，可按卷或章节拆分）")
	}
	texts := []string{d.Title, d.Summary, d.Mainline, d.Theme, d.WorldRules, d.Costs, d.Boundaries, d.Setting, d.Factions, d.Characters, d.Arcs, d.TurningPoints, d.Ending, d.OpenQuestions}
	for _, v := range texts {
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > 20000 {
			return invalid("文字长度")
		}
	}
	if len(d.Inputs) > 250 || len(d.Volumes) > 200 || len(d.Chapters) > 1000 || len(d.Scenes) > 128 {
		return invalid("条目数量")
	}
	if (d.Kind != "book" && len(d.Volumes) > 0) || (d.Kind != "volume" && len(d.Chapters) > 0) || (d.Kind != "detail" && len(d.Scenes) > 0) {
		return invalid("文档类型与条目")
	}
	seen := map[string]bool{}
	required := func(values ...string) bool {
		for _, v := range values {
			if !utf8.ValidString(v) || utf8.RuneCountInString(v) > 20000 || (complete && strings.TrimSpace(v) == "") {
				return false
			}
		}
		return true
	}
	for _, v := range d.Volumes {
		if !planID.MatchString(v.ID) || seen[v.ID] || !required(v.Title, v.Summary) {
			return invalid("分卷身份、标题或摘要")
		}
		seen[v.ID] = true
	}
	for _, c := range d.Chapters {
		if !planID.MatchString(c.ID) || seen[c.ID] || !required(c.Title, c.Summary, c.Perspective, c.Goal, c.Events, c.Change, c.Transition) {
			return invalid("章节身份、摘要、视角、事件或衔接")
		}
		seen[c.ID] = true
	}
	for _, s := range d.Scenes {
		if !planID.MatchString(s.ID) || seen[s.ID] || s.Words < 0 || s.Words > 50000 || (complete && s.Words == 0) || !required(s.Time, s.Place, s.Characters, s.Knowledge, s.Goal, s.Conflict, s.Action, s.Outcome, s.Emotion, s.Prerequisites, s.Hook) || !utf8.ValidString(s.Foreshadow) || utf8.RuneCountInString(s.Foreshadow) > 20000 {
			return invalid("场景身份、因果、知情范围或字数")
		}
		seen[s.ID] = true
	}
	policy := d.Dependencies()
	if policy.Validate() != nil {
		return invalid("依据版本")
	}
	for _, pin := range d.Inputs {
		if pin.Kind != "resource" || !planID.MatchString(pin.ID) || !planID.MatchString(pin.BranchID) || !planID.MatchString(pin.Revision) || !planHash.MatchString(pin.Hash) {
			return invalid("来源身份")
		}
	}
	if !complete {
		return nil
	}
	if !required(d.Title, d.Summary) {
		return invalid("标题或摘要")
	}
	switch d.Kind {
	case "world":
		if !required(d.Mainline, d.Theme, d.WorldRules, d.Costs, d.Boundaries, d.Setting, d.Factions, d.Characters, d.Arcs, d.TurningPoints, d.Ending) {
			return invalid("世界规则、人物、主线或转折（未定内容请明确注明）")
		}
	case "book":
		if !required(d.Mainline, d.Theme, d.Ending) || len(d.Volumes) == 0 {
			return invalid("全书主线、主题、结局及卷安排")
		}
	case "volume":
		if !required(d.Mainline) || len(d.Chapters) == 0 {
			return invalid("卷主线及完整暂定章节")
		}
	case "detail":
		if len(d.Scenes) == 0 {
			return invalid("实际场景细纲")
		}
	}
	return nil
}

func (d PlanningDocument) Dependencies() domain.DependencyPolicy {
	p := domain.DependencyPolicy{Inputs: []domain.Dependency{}}
	for _, pin := range d.Inputs {
		p.Inputs = append(p.Inputs, domain.Dependency{Source: pin, Kind: "production", Reason: "小说规划采用的确定版本"})
	}
	return p
}
