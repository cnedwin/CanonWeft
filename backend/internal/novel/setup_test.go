package novel

import (
	"errors"
	"fmt"
	"testing"

	"novelstudio/internal/domain"
)

func completeSetup() Setup {
	s := Defaults()
	s.Title = "同名之城"
	s.Genre = "悬疑"
	s.Idea = "两位同名者追查失踪案"
	s.Conflict = "寻找真相与保护亲人冲突"
	s.CharacterMode = "manual"
	s.Characters = []Character{{Name: "林舟", Identity: "记者", Goal: "找到妹妹"}, {Name: "林舟", Identity: "医生", Goal: "保护病人"}}
	s.CreationMode = "manual"
	return s
}
func TestSetupDraftAndConfirmationAreDifferent(t *testing.T) {
	if err := Defaults().Validate(false); err != nil {
		t.Fatal(err)
	}
	if err := Defaults().Validate(true); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("unanswered choices accepted", err)
	}
	s := completeSetup()
	if err := s.Validate(true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Setup){func(s *Setup) { s.Title = " \n" }, func(s *Setup) { s.SourceMode = "import" }, func(s *Setup) { s.CharacterMode = "architect" }, func(s *Setup) { s.CreationMode = "assisted" }, func(s *Setup) { s.IPMode = "existing" }, func(s *Setup) { s.ChapterMax = s.ChapterMin - 1 }, func(s *Setup) { s.TargetWords = 0 }, func(s *Setup) { s.Protection = "" }} {
		invalid := completeSetup()
		mutate(&invalid)
		if err := invalid.Validate(true); !errors.Is(err, domain.ErrValidation) {
			t.Fatal("invalid final setup accepted", invalid, err)
		}
	}
	s = completeSetup()
	s.CreationMode = "assisted"
	s.ModelChoice = "later"
	s.CharacterMode = "architect"
	s.Characters = nil
	if err := s.Validate(true); err != nil {
		t.Fatal("explicit deferred model/character choice should permit creation", err)
	}
}
func TestSetupStableCharacterCandidatesNeverPublish(t *testing.T) {
	s := completeSetup()
	n := 0
	snapshot := s.InitialSnapshot(func() string { n++; return fmt.Sprintf("%032x", n) }, "2026-10-27T00:00:00Z")
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entities) != 2 || snapshot.Entities[0].ID == snapshot.Entities[1].ID || snapshot.Entities[0].Name != snapshot.Entities[1].Name {
		t.Fatal("namesakes merged")
	}
	for _, a := range snapshot.Assertions {
		if a.Locked || a.Source == "" {
			t.Fatal("unreviewed character became locked or lost provenance")
		}
	}
	s.CharacterMode = "architect"
	if len(s.InitialSnapshot(func() string { t.Fatal("creation invoked character generation"); return "" }, "").Entities) != 0 {
		t.Fatal("invented character")
	}
}
