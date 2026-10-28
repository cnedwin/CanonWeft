package service

import (
	"encoding/json"
	"novelstudio/internal/novel"
	"reflect"
	"strings"
	"testing"
)

func TestSetupFieldsPreserveProtectedChoicesAndRejectWrongTypes(t *testing.T) {
	current := novel.Defaults()
	current.Title = "旧书名"
	current.ModelChoice = "later"
	current.Characters = []novel.Character{{Name: "不可丢失的旧角色", Identity: "医生"}}
	next, err := applySetupFields(current, []SetupFieldCandidate{{Field: "title", Value: json.RawMessage(`"新书名"`)}})
	if err != nil || next.Title != "新书名" || next.ModelChoice != "later" || len(next.Characters) != 1 {
		t.Fatal(next, err)
	}
	for _, field := range []SetupFieldCandidate{{Field: "model_choice", Value: json.RawMessage(`"configured"`)}, {Field: "protection", Value: json.RawMessage(`"none"`)}, {Field: "project_id", Value: json.RawMessage(`"foreign"`)}, {Field: "target_words", Value: json.RawMessage(`"很多"`)}, {Field: "characters", Value: json.RawMessage(`"角色"`)}, {Field: "chapter_min", Value: json.RawMessage(`50000`)}} {
		if _, err = applySetupFields(current, []SetupFieldCandidate{field}); err == nil {
			t.Fatal("invalid creative patch", field.Field)
		}
	}
}

func TestSetupAssistanceArchitectCharactersBecomeManualOnlyWhenSelected(t *testing.T) {
	p, err := novel.PreviewSetupPreset("mystery-rules", "作者的自由开书想法")
	if err != nil {
		t.Fatal(err)
	}
	characters, _ := json.Marshal(p.Setup.Characters)
	current := p.Setup
	current.CharacterMode, current.Characters = "architect", nil
	if err := current.Validate(true); err != nil {
		t.Fatal("invalid architect fixture", err)
	}
	fields := []SetupFieldCandidate{{Field: "characters", Value: characters}}
	next, err := applySetupFields(current, fields)
	if err != nil {
		t.Fatalf("valid nonempty character candidate cannot be previewed/adopted from architect mode: %v", err)
	}
	if next.CharacterMode != "manual" || !reflect.DeepEqual(next.Characters, p.Setup.Characters) || next.Validate(true) != nil {
		t.Fatal("adopted concrete characters did not become complete manual settings", next)
	}
	if current.CharacterMode != "architect" || len(current.Characters) != 0 {
		t.Fatal("preview mutated the author's current draft")
	}
	if next.PresetID != current.PresetID || next.PresetRevision != current.PresetRevision || next.IPMode != current.IPMode || next.ModelChoice != current.ModelChoice || next.Protection != current.Protection || next.CreationMode != current.CreationMode {
		t.Fatal("character adoption changed protected choices")
	}
	titleOnly, err := applySetupFields(current, []SetupFieldCandidate{{Field: "title", Value: json.RawMessage(`"只修改书名"`)}})
	if err != nil || titleOnly.CharacterMode != "architect" || len(titleOnly.Characters) != 0 {
		t.Fatal("adopting another field silently selected character candidates", err)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`null`)} {
		empty, err := applySetupFields(current, []SetupFieldCandidate{{Field: "characters", Value: raw}})
		if err != nil || empty.CharacterMode != "architect" || len(empty.Characters) != 0 {
			t.Fatal("empty character proposal changed mode", err)
		}
	}
	for _, fields := range [][]SetupFieldCandidate{
		{{Field: "character_mode", Value: json.RawMessage(`"manual"`)}},
		{{Field: "characters", Value: characters}, {Field: "character_mode", Value: json.RawMessage(`"manual"`)}},
		{{Field: "characters", Value: json.RawMessage(`"角色"`)}},
		{{Field: "characters", Value: json.RawMessage(`[{"name":"错误类型","identity":123,"goal":"调查"}]`)}},
	} {
		if _, err := applySetupFields(current, fields); err == nil {
			t.Fatal("model can directly change mode or submit malformed characters", fields)
		}
	}
	manual, err := applySetupFields(p.Setup, []SetupFieldCandidate{{Field: "title", Value: json.RawMessage(`"保留手填人物"`)}})
	if err != nil || manual.CharacterMode != "manual" || !reflect.DeepEqual(manual.Characters, p.Setup.Characters) {
		t.Fatal("existing manual characters were lost", err)
	}
}

func TestSetupAssistanceArchitectCandidateNoticeExplainsExplicitAdoption(t *testing.T) {
	p, err := novel.PreviewSetupPreset("mystery-rules", "作者的自由开书想法")
	if err != nil {
		t.Fatal(err)
	}
	characters, _ := json.Marshal(p.Setup.Characters)
	current := p.Setup
	current.CharacterMode, current.Characters = "architect", nil
	warnings, err := setupSuggestionWarnings(current, []SetupFieldCandidate{{Field: "characters", Value: characters}}, []string{"原有核对提示"})
	if err != nil || len(warnings) != 2 || warnings[0] != "原有核对提示" || !strings.Contains(warnings[1], "明确采用非空人物建议后") || !strings.Contains(warnings[1], "人物模式保持原选择") {
		t.Fatal("suggestion rejected valid candidates or lost explicit-adoption notice", warnings, err)
	}
	for _, fields := range [][]SetupFieldCandidate{
		{{Field: "title", Value: json.RawMessage(`"只修改书名"`)}},
		{{Field: "characters", Value: json.RawMessage(`[]`)}},
	} {
		warnings, err := setupSuggestionWarnings(current, fields, []string{"原有核对提示"})
		if err != nil || len(warnings) != 1 {
			t.Fatal("unselected or empty characters claimed a mode change", warnings, err)
		}
	}
	if _, err := setupSuggestionWarnings(current, []SetupFieldCandidate{{Field: "character_mode", Value: json.RawMessage(`"manual"`)}}, nil); err == nil {
		t.Fatal("suggestion accepted a direct model mode change")
	}
	if current.CharacterMode != "architect" || len(current.Characters) != 0 {
		t.Fatal("showing suggestion adopted characters")
	}
}

func TestSetupAssistanceUsesVersionedPresetMethodsWithoutChangingAuthority(t *testing.T) {
	p, err := novel.PreviewSetupPreset("mystery-rules", "作者的新悬念")
	if err != nil {
		t.Fatal(err)
	}
	system := setupAssistanceSystem(p.Setup)
	if !strings.Contains(system, p.ReaderPromise) || !strings.Contains(system, "novel-planning") || !strings.Contains(system, "线索") || !strings.Contains(system, "不得更改IP关联") || !strings.Contains(system, "current_setup.idea") {
		t.Fatal("preset method or authority boundary missing", system)
	}
	p.Setup.PresetRevision = "2026-10-26.1"
	if strings.Contains(setupAssistanceSystem(p.Setup), p.ReaderPromise) {
		t.Fatal("current method silently substituted for old preset")
	}
	if strings.Contains(setupAssistanceSystem(novel.Defaults()), "2026-10-27.1") {
		t.Fatal("legacy draft gained preset")
	}
}

func TestSetupAssistancePreservesAuthorPresetProvenance(t *testing.T) {
	p, err := novel.PreviewSetupPreset("urban-career", "作者的事业线")
	if err != nil {
		t.Fatal(err)
	}
	next, err := applySetupFields(p.Setup, []SetupFieldCandidate{{Field: "title", Value: json.RawMessage(`"作者的新书名"`)}})
	if err != nil || next.PresetID != p.Setup.PresetID || next.PresetRevision != p.Setup.PresetRevision || next.Idea != p.Setup.Idea {
		t.Fatal("field adoption changed provenance or untouched author input", err)
	}
	for _, field := range []string{"preset_id", "preset_revision"} {
		if _, err := applySetupFields(p.Setup, []SetupFieldCandidate{{Field: field, Value: json.RawMessage(`"different"`)}}); err == nil {
			t.Fatal("model can change author preset provenance", field)
		}
	}
}
