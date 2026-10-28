package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"
)

// Summary inputs are exact provenance pins, not mandatory prompt dependencies.
// Storage must revalidate their full lineage before the compiler may use them.
type SummaryPin struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
}
type Summary struct {
	Level  string       `json:"level"`
	UnitID string       `json:"unit_id"`
	Title  string       `json:"title"`
	Inputs []SummaryPin `json:"inputs,omitempty"`
}

func SummaryRank(level string) int {
	return map[string]int{"scene": 1, "chapter": 2, "volume": 3, "book": 4}[level]
}
func SummaryTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func validateSummary(i Item) error {
	if i.Summary == nil {
		return nil
	}
	s := i.Summary
	rank := SummaryRank(s.Level)
	if i.Kind != "summary" || rank == 0 || !nonempty(s.UnitID) || strings.TrimSpace(s.Title) == "" || !utf8.ValidString(s.Title) || utf8.RuneCountInString(s.Title) > 160 || len(s.Inputs) > 128 {
		return ErrInvalid
	}
	if rank == 1 && len(s.Inputs) > 0 || rank >= 3 && len(s.Inputs) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, pin := range s.Inputs {
		if !nonempty(pin.ID) || !nonempty(pin.Revision) || !hashPattern.MatchString(pin.Hash) || pin.ID == i.ID || seen[pin.ID] {
			return ErrInvalid
		}
		seen[pin.ID] = true
	}
	return nil
}

func ValidateSummarySource(parent, child Item, pin SummaryPin) error {
	if parent.Summary == nil || child.Summary == nil || parent.Kind != "summary" || child.Kind != "summary" || SummaryRank(parent.Summary.Level) != SummaryRank(child.Summary.Level)+1 {
		return ErrInvalid
	}
	if child.ID != pin.ID || child.Revision != pin.Revision || SummaryTextHash(child.Text) != pin.Hash || child.Project != parent.Project || child.Branch != parent.Branch || child.Actor != "" || child.Session != "" || !slices.Contains([]string{"published", "adopted_plan"}, child.State) {
		return ErrInvalid
	}
	timeline := func(s string) string {
		if s == "" {
			return "main"
		}
		return s
	}
	if timeline(parent.Timeline) != timeline(child.Timeline) || parent.Save != child.Save || parent.From < child.From || parent.RevealAt < child.RevealAt {
		return ErrInvalid
	}
	if child.Until != nil && (parent.Until == nil || *parent.Until > *child.Until) {
		return ErrInvalid
	}
	if (child.Control || child.State == "adopted_plan") && !parent.Control {
		return ErrInvalid
	}
	if parent.Visibility == "public" && child.Visibility != "public" || parent.Visibility == "restricted" && child.Visibility == "author" {
		return ErrInvalid
	}
	if parent.Visibility == "restricted" && child.Visibility == "restricted" {
		for _, actor := range parent.KnownBy {
			if !slices.Contains(child.KnownBy, actor) {
				return ErrInvalid
			}
		}
	}
	return nil
}
