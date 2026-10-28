package novel

import (
	"strings"
	"unicode/utf8"
)

const SetupPresetRevision = "2026-10-27.1"

// SetupPreset describes an original editable starting point, not generated
// analysis, an installed agent, or a promise of provider qualification.
type SetupPreset struct {
	ID            string   `json:"id"`
	Revision      string   `json:"revision"`
	Name          string   `json:"name"`
	Audience      string   `json:"audience"`
	Description   string   `json:"description"`
	ExampleIdea   string   `json:"example_idea"`
	ReaderPromise string   `json:"reader_promise"`
	SkillNames    []string `json:"skill_names"`
	Guidance      []string `json:"guidance"`
	Source        string   `json:"source"`
}

type SetupPresetPreview struct {
	PresetID       string   `json:"preset_id"`
	PresetRevision string   `json:"preset_revision"`
	Setup          Setup    `json:"setup"`
	ReaderPromise  string   `json:"reader_promise"`
	SkillNames     []string `json:"skill_names"`
	Notes          []string `json:"notes"`
}

type setupPresetDefinition struct {
	SetupPreset
	Setup Setup
}

// Each call builds fresh values, including nested slices. No caller can mutate
// the catalog or another author's preview, and no service dependency is needed.
func SetupPresets() []SetupPreset {
	definitions := originalSetupPresets()
	items := make([]SetupPreset, 0, len(definitions))
	for _, p := range definitions {
		items = append(items, p.SetupPreset)
	}
	return items
}

// SetupPresetFor returns guidance only for the exact saved source revision.
// Unknown/historical revisions stay editable without claiming today's methods.
func SetupPresetFor(s Setup) (SetupPreset, bool) {
	for _, p := range SetupPresets() {
		if p.ID == s.PresetID && p.Revision == s.PresetRevision {
			return p, true
		}
	}
	return SetupPreset{}, false
}

func PreviewSetupPreset(id, idea string) (SetupPresetPreview, error) {
	fields := map[string]string{}
	if !utf8.ValidString(id) || utf8.RuneCountInString(id) > 80 {
		fields["preset_id"] = "预设标识无效"
	}
	if !utf8.ValidString(idea) || utf8.RuneCountInString(idea) > 16000 {
		fields["idea"] = "想法最多16000字且须为有效文本"
	} else if idea != "" && strings.TrimSpace(idea) == "" {
		fields["idea"] = "请填写想法或留空查看原创示例"
	}
	if len(fields) != 0 {
		return SetupPresetPreview{}, &Validation{Fields: fields}
	}
	for _, p := range originalSetupPresets() {
		if p.ID != id {
			continue
		}
		notes := []string{
			"这是本项目独立编写的静态预设建议，未调用AI，不表示已理解你的想法；所有字段均可编辑。",
			"预览不保存草稿、不建正式小说、不采用规划或正史、不更改权限，也不产生模型费用。",
			"所列技能是已有方法引用；AI完善沿用现有模型、费用与作者确认流程，展示不代表已安装或真实质量通过。",
		}
		if idea != "" {
			p.Setup.Idea = idea
			notes = append(notes, "你的原始想法已原样保留；书名、人物、世界等其他预设建议未自动改写，请编辑或按需使用AI完善。")
		} else {
			notes = append(notes, "尚未填写想法，当前展示该题材的原创示例，待作者修改和确认。")
		}
		return SetupPresetPreview{PresetID: p.ID, PresetRevision: p.Revision, Setup: p.Setup, ReaderPromise: p.ReaderPromise, SkillNames: p.SkillNames, Notes: notes}, nil
	}
	return SetupPresetPreview{}, &Validation{Fields: map[string]string{"preset_id": "请选择有效的小说预设"}}
}

func originalPreset(id, name, description, promise string, guidance []string, s Setup) setupPresetDefinition {
	s.PresetID, s.PresetRevision = id, SetupPresetRevision
	s.Language, s.SourceMode, s.IPMode = "简体中文", "fresh", "new"
	s.CharacterMode, s.CreationMode, s.ModelChoice = "manual", "assisted", "later"
	s.Protection = "core_identity_life_world"
	s.Include = "读者期待：" + promise + "；希望出现：" + s.Include
	return setupPresetDefinition{SetupPreset: SetupPreset{
		ID: id, Revision: SetupPresetRevision, Name: name, Audience: s.Audience,
		Description: description, ExampleIdea: s.Idea, ReaderPromise: promise,
		SkillNames: []string{"novel-setup-assistant", "novel-planning", "character-ensemble", "novel-chapter-writer", "novel-chapter-bookkeeper", "novel-book-review"},
		Guidance:   guidance, Source: "本项目独立原创；2026-10-27。题材方法为创作建议，无外部榜单或商业成绩背书。",
	}, Setup: s}
}
