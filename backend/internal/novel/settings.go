package novel

import "reflect"

type SettingChange struct {
	Field  string `json:"field"`
	Label  string `json:"label"`
	Scope  string `json:"scope"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

var settingsFields = []struct{ goName, key, label, scope string }{
	{"Title", "title", "书名", "display"},
	{"Genre", "genre", "题材", "planning"}, {"Language", "language", "语言", "planning"},
	{"Idea", "idea", "核心梗概", "planning"}, {"Conflict", "conflict", "主线目标与冲突", "planning"},
	{"Audience", "audience", "目标读者", "planning"}, {"World", "world", "世界背景", "planning"},
	{"Theme", "theme", "主题", "planning"}, {"Include", "include", "希望出现的内容", "planning"}, {"Avoid", "avoid", "希望避免的内容", "planning"},
	{"Perspective", "perspective", "叙事视角", "planning"}, {"Style", "style", "文风", "planning"},
	{"TargetWords", "target_words", "目标总字数", "planning"}, {"VolumeCount", "volume_count", "计划卷数", "planning"},
	{"ChapterMin", "chapter_min", "每章最少字数", "planning"}, {"ChapterMax", "chapter_max", "每章最多字数", "planning"},
	{"Protection", "protection", "核心设定保护", "planning"},
	{"CreationMode", "creation_mode", "创作方式", "generation"}, {"ModelChoice", "model_choice", "模型准备", "generation"}, {"ConnectionID", "connection_id", "写作模型", "generation"},
}

// Creation provenance and initial characters are immutable here. Subsequent
// character changes belong to the shared IP draft and its own review process.
func ValidateSettingsEdit(base, next Setup, complete bool) error {
	if err := next.Validate(complete); err != nil {
		return err
	}
	a, b := base, next
	av, bv := reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem()
	for _, field := range settingsFields {
		av.FieldByName(field.goName).SetZero()
		bv.FieldByName(field.goName).SetZero()
	}
	if !reflect.DeepEqual(a, b) {
		return &Validation{Fields: map[string]string{"creation_record": "原稿来源、IP来源与初始角色属于创建记录；角色后续修改请使用角色库"}}
	}
	return nil
}

func SettingsChanges(base, next Setup) []SettingChange {
	result := []SettingChange{}
	a, b := reflect.ValueOf(base), reflect.ValueOf(next)
	for _, field := range settingsFields {
		before, after := a.FieldByName(field.goName).Interface(), b.FieldByName(field.goName).Interface()
		if !reflect.DeepEqual(before, after) {
			result = append(result, SettingChange{field.key, field.label, field.scope, before, after})
		}
	}
	return result
}
