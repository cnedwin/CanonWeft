// Package novel owns author choices and planning gates, never model side effects.
package novel

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

type Character struct {
	Name        string `json:"name"`
	Identity    string `json:"identity"`
	Goal        string `json:"goal"`
	Background  string `json:"background"`
	Personality string `json:"personality"`
}

type Setup struct {
	PresetID       string      `json:"preset_id,omitempty"`
	PresetRevision string      `json:"preset_revision,omitempty"`
	Title          string      `json:"title"`
	Genre          string      `json:"genre"`
	Language       string      `json:"language"`
	SourceMode     string      `json:"source_mode"`
	SourceText     string      `json:"source_text"`
	IPMode         string      `json:"ip_mode"`
	ProjectID      string      `json:"project_id"`
	BranchID       string      `json:"branch_id"`
	BaseHead       string      `json:"base_head"`
	Idea           string      `json:"idea"`
	Conflict       string      `json:"conflict"`
	Audience       string      `json:"audience"`
	World          string      `json:"world"`
	Theme          string      `json:"theme"`
	Include        string      `json:"include"`
	Avoid          string      `json:"avoid"`
	Perspective    string      `json:"perspective"`
	Style          string      `json:"style"`
	TargetWords    int64       `json:"target_words"`
	VolumeCount    int         `json:"volume_count"`
	ChapterMin     int         `json:"chapter_min"`
	ChapterMax     int         `json:"chapter_max"`
	CharacterMode  string      `json:"character_mode"`
	Characters     []Character `json:"characters"`
	CreationMode   string      `json:"creation_mode"`
	ModelChoice    string      `json:"model_choice"`
	ConnectionID   string      `json:"connection_id"`
	Protection     string      `json:"protection"`
}

func Defaults() Setup {
	return Setup{Language: "简体中文", SourceMode: "fresh", IPMode: "new", Audience: "大众读者", Perspective: "第三人称限知", Style: "中文类型小说表达", TargetWords: 300000, VolumeCount: 3, ChapterMin: 2000, ChapterMax: 4000, Protection: "core_identity_life_world", Characters: []Character{}}
}

type Validation struct {
	Fields map[string]string `json:"fields"`
}

func (v *Validation) Error() string { return "请补充或修正小说设置中的标记项" }
func (v *Validation) Unwrap() error { return domain.ErrValidation }

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Validate permits unfinished private drafts. Confirmation is a separate call;
// default values do not authorize generation, publication, or any expenditure.
func (s Setup) Validate(complete bool) error {
	v := &Validation{Fields: map[string]string{}}
	text := func(key, value string, max int, required bool) {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
			v.Fields[key] = "内容超出支持长度或编码无效"
		} else if complete && required && strings.TrimSpace(value) == "" {
			v.Fields[key] = "请填写此项"
		}
	}
	choice := func(key, value string, required bool, allowed ...string) {
		if value == "" && !complete {
			return
		}
		if value == "" && !required {
			return
		}
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		v.Fields[key] = "请选择一个有效选项"
	}
	text("title", s.Title, 120, true)
	text("preset_id", s.PresetID, 80, false)
	text("preset_revision", s.PresetRevision, 80, false)
	if (s.PresetID == "") != (s.PresetRevision == "") {
		v.Fields["preset_id"] = "预设来源和版本须同时保留或同时清空"
	} else if s.PresetID != "" {
		found := false
		for _, p := range SetupPresets() {
			if p.ID == s.PresetID {
				found = true
				break
			}
		}
		if !found {
			v.Fields["preset_id"] = "请选择有效的小说预设"
		}
		// Historical revisions remain recoverable; exact-version lookup alone
		// may attach current guidance, never silently upgrade a saved draft.
		if strings.TrimSpace(s.PresetRevision) != s.PresetRevision || strings.TrimSpace(s.PresetRevision) == "" {
			v.Fields["preset_revision"] = "请保留有效的预设来源版本"
		}
	}
	text("genre", s.Genre, 120, true)
	text("language", s.Language, 80, true)
	text("idea", s.Idea, 16000, true)
	text("conflict", s.Conflict, 8000, true)
	text("audience", s.Audience, 1000, true)
	text("world", s.World, 24000, false)
	text("theme", s.Theme, 8000, false)
	text("include", s.Include, 8000, false)
	text("avoid", s.Avoid, 8000, false)
	text("perspective", s.Perspective, 1000, true)
	text("style", s.Style, 4000, true)
	text("source_text", s.SourceText, 200000, s.SourceMode == "import")
	choice("source_mode", s.SourceMode, true, "fresh", "import")
	choice("ip_mode", s.IPMode, true, "new", "existing")
	choice("character_mode", s.CharacterMode, true, "manual", "architect")
	choice("creation_mode", s.CreationMode, true, "manual", "assisted")
	choice("protection", s.Protection, true, "core_identity_life_world", "review_all_changes")
	choice("model_choice", s.ModelChoice, s.CreationMode == "assisted", "later", "configured")
	if s.IPMode == "existing" {
		for key, id := range map[string]string{"project_id": s.ProjectID, "branch_id": s.BranchID, "base_head": s.BaseHead} {
			if (complete || id != "") && !ValidID(id) {
				v.Fields[key] = "请选择已发布的IP分支和版本"
			}
		}
	} else if s.ProjectID != "" || s.BranchID != "" || s.BaseHead != "" {
		v.Fields["ip_mode"] = "新IP不能携带其他作品的分支身份"
	}
	if s.ModelChoice == "configured" {
		if (complete || s.ConnectionID != "") && !ValidID(s.ConnectionID) {
			v.Fields["connection_id"] = "请选择已配置的模型连接"
		}
	} else if s.ConnectionID != "" {
		v.Fields["connection_id"] = "稍后配置时请移除旧模型选择"
	}
	for _, r := range []struct {
		key        string
		value, max int64
	}{{"target_words", s.TargetWords, 100000000}, {"volume_count", int64(s.VolumeCount), 10000}, {"chapter_min", int64(s.ChapterMin), 100000}, {"chapter_max", int64(s.ChapterMax), 100000}} {
		if r.value < 0 || r.value > r.max || complete && r.value == 0 {
			v.Fields[r.key] = "请输入支持范围内的正整数"
		}
	}
	if s.ChapterMax > 0 && s.ChapterMin > s.ChapterMax {
		v.Fields["chapter_max"] = "单章字数上限不能小于下限"
	}
	if complete && s.TargetWords < int64(s.ChapterMin) {
		v.Fields["target_words"] = "总字数目标不能小于单章最低字数"
	}
	if len(s.Characters) > 100 {
		v.Fields["characters"] = "初始人物最多100名，其余可在工作台添加"
	}
	if complete && s.CharacterMode == "manual" && len(s.Characters) == 0 {
		v.Fields["characters"] = "请至少填写一名主角"
	}
	if s.CharacterMode == "architect" && len(s.Characters) > 0 {
		v.Fields["characters"] = "已有手填人物时请选择手动设置，避免静默丢弃人物"
	}
	for i, c := range s.Characters {
		prefix := fmt.Sprintf("characters.%d.", i)
		text(prefix+"name", c.Name, 300, true)
		text(prefix+"identity", c.Identity, 4000, true)
		text(prefix+"goal", c.Goal, 4000, true)
		text(prefix+"background", c.Background, 12000, false)
		text(prefix+"personality", c.Personality, 8000, false)
	}
	if len(v.Fields) > 0 {
		return v
	}
	return nil
}

// InitialSnapshot gives each person a stable independent ID, including namesakes.
// All assertions stay in the author's private IP draft until normal review/adoption.
func (s Setup) InitialSnapshot(nextID func() string, recordedAt string) domain.Snapshot {
	result := domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
	if s.CharacterMode != "manual" {
		return result
	}
	for _, c := range s.Characters {
		id := nextID()
		result.Entities = append(result.Entities, domain.Entity{ID: id, Kind: "character", Name: strings.TrimSpace(c.Name), Description: c.Background, Aliases: []string{}})
		for _, field := range []struct{ name, value string }{{"角色定位", "主角"}, {"职业", c.Identity}, {"目标", c.Goal}, {"背景", c.Background}, {"性格", c.Personality}} {
			if strings.TrimSpace(field.value) == "" {
				continue
			}
			result.Assertions = append(result.Assertions, domain.Assertion{ID: nextID(), Subject: id, Predicate: field.name, Value: field.value, Kind: "fact", Visibility: "public", KnownBy: []string{}, RecordedAt: recordedAt, Source: "作者确认的小说初始设置"})
		}
	}
	return result
}
