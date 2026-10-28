package novel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/presets"
)

func TestSetupPresetsCompleteOriginalAndFree(t *testing.T) {
	ids := []string{"urban-career", "fantasy-growth", "mystery-rules", "historical-strategy", "modern-romance", "era-career", "ancient-intrigue", "heroine-fantasy"}
	items := SetupPresets()
	if len(items) != len(ids) {
		t.Fatalf("want eight complete presets, got %d", len(items))
	}
	titles, leads := map[string]bool{}, map[string]bool{}
	for i, item := range items {
		if item.ID != ids[i] || item.Revision != "2026-10-27.1" || item.Name == "" || item.Audience == "" || item.Description == "" || item.ExampleIdea == "" || item.ReaderPromise == "" || len(item.Guidance) < 3 {
			t.Fatalf("incomplete catalog: %+v", item)
		}
		preview, err := PreviewSetupPreset(item.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		s := preview.Setup
		if err := s.Validate(true); err != nil {
			t.Fatalf("%s: %v", item.ID, err)
		}
		if s.PresetID != item.ID || s.PresetRevision != item.Revision || preview.PresetID != item.ID || preview.PresetRevision != item.Revision {
			t.Fatal("missing version provenance")
		}
		if s.CreationMode != "assisted" || s.ModelChoice != "later" || s.ConnectionID != "" || s.IPMode != "new" || s.ProjectID != "" || s.BranchID != "" || s.BaseHead != "" || s.CharacterMode != "manual" {
			t.Fatal("preview changed authority or selected paid generation", s)
		}
		if len(s.Characters) < 3 || s.World == "" || s.Theme == "" || s.Include == "" || s.Avoid == "" || s.Conflict == "" || len(preview.Notes) == 0 {
			t.Fatal("incomplete story start", item.ID)
		}
		if titles[s.Title] || leads[s.Characters[0].Name] {
			t.Fatal("genres share a reskinned title or lead", item.ID)
		}
		titles[s.Title], leads[s.Characters[0].Name] = true, true
		for _, c := range s.Characters {
			if c.Name == "" || c.Identity == "" || c.Goal == "" || c.Background == "" || c.Personality == "" {
				t.Fatal("placeholder character", item.ID, c)
			}
		}
		for _, name := range item.SkillNames {
			if _, err := presets.Bundle(name); err != nil {
				t.Fatalf("nonexistent method %q: %v", name, err)
			}
		}
	}
}

func TestSetupPresetIdeaPreservedAndTemplatesNeverPretendToUnderstandIt(t *testing.T) {
	idea := "  我想写女航天员与月面植物学家的故事\n保留空格。  "
	base, _ := PreviewSetupPreset("modern-romance", "")
	preview, err := PreviewSetupPreset("modern-romance", idea)
	if err != nil || preview.Setup.Idea != idea {
		t.Fatal("author idea changed", err, preview.Setup.Idea)
	}
	if preview.Setup.Title != base.Setup.Title || preview.Setup.World != base.Setup.World || preview.Setup.Characters[0] != base.Setup.Characters[0] {
		t.Fatal("static template pretended to customize unrelated fields")
	}
	if !strings.Contains(strings.Join(preview.Notes, " "), "未自动改写") {
		t.Fatal("missing honest template disclosure", preview.Notes)
	}
}

func TestSetupPresetRejectsInvalidInputsAndIsolatesReturnedReferences(t *testing.T) {
	for _, in := range []struct{ id, idea string }{{"", ""}, {"missing", ""}, {" urban-career", ""}, {string([]byte{0xff}), ""}, {strings.Repeat("x", 1000), ""}, {"urban-career", string([]byte{0xff})}, {"urban-career", strings.Repeat("字", 16001)}, {"urban-career", " \t\n "}} {
		if _, err := PreviewSetupPreset(in.id, in.idea); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("accepted invalid input: id=%q err=%v", in.id, err)
		}
	}
	items := SetupPresets()
	items[0].SkillNames[0], items[0].Guidance[0] = "polluted", "polluted"
	p, _ := PreviewSetupPreset("urban-career", "")
	p.Setup.Characters[0].Name, p.SkillNames[0], p.Notes[0] = "polluted", "polluted", "polluted"
	again, _ := PreviewSetupPreset("urban-career", "")
	if again.Setup.Characters[0].Name == "polluted" || again.SkillNames[0] == "polluted" || SetupPresets()[0].Guidance[0] == "polluted" || again.Notes[0] == "polluted" {
		t.Fatal("shared mutable preset references")
	}
	if _, err := PreviewSetupPreset("urban-career", strings.Repeat("字", 16000)); err != nil {
		t.Fatal("valid UTF8 rune boundary rejected", err)
	}
}

func TestSetupPresetMetadataBackwardCompatibilityAndVersionBoundary(t *testing.T) {
	old := completeSetup()
	if err := old.Validate(true); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(old)
	if strings.Contains(string(raw), "preset_id") || strings.Contains(string(raw), "preset_revision") {
		t.Fatal("old draft gained metadata")
	}
	p, _ := PreviewSetupPreset("urban-career", "")
	if _, ok := SetupPresetFor(p.Setup); !ok {
		t.Fatal("current preset lost guidance")
	}
	p.Setup.PresetRevision = "2026-10-26.1"
	if err := p.Setup.Validate(true); err != nil {
		t.Fatal("historical draft cannot be restored", err)
	}
	if _, ok := SetupPresetFor(p.Setup); ok {
		t.Fatal("historical version impersonates current guidance")
	}
	for _, mutate := range []func(*Setup){func(s *Setup) { s.PresetID = "not-a-preset" }, func(s *Setup) { s.PresetRevision = "" }, func(s *Setup) { s.PresetID = "" }, func(s *Setup) { s.PresetRevision = string([]byte{0xff}) }, func(s *Setup) { s.PresetRevision = strings.Repeat("x", 81) }} {
		s := p.Setup
		mutate(&s)
		if err := s.Validate(false); !errors.Is(err, domain.ErrValidation) {
			t.Fatal("accepted malformed provenance", s.PresetID, err)
		}
	}
}
