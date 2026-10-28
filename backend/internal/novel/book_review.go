package novel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"novelstudio/internal/domain"
	"slices"
	"strings"
)

var BookReviewCategories = []string{"causality", "character", "foreshadow", "theme", "highlight", "language"}

type BookReviewEvidence struct {
	ChapterID string `json:"chapter_id"`
	Quote     string `json:"quote"`
}
type BookReviewFinding struct {
	ID          string               `json:"id"`
	Category    string               `json:"category"`
	Severity    string               `json:"severity"`
	Title       string               `json:"title"`
	Explanation string               `json:"explanation"`
	Suggestion  string               `json:"suggestion"`
	Evidence    []BookReviewEvidence `json:"evidence"`
}
type BookReviewCarry struct {
	Key      string               `json:"key"`
	Category string               `json:"category"`
	Summary  string               `json:"summary"`
	Status   string               `json:"status"`
	Evidence []BookReviewEvidence `json:"evidence"`
}
type BookReviewResult struct {
	Coverage []string            `json:"coverage"`
	Checks   map[string]string   `json:"checks"`
	Findings []BookReviewFinding `json:"findings"`
	Carry    []BookReviewCarry   `json:"carry"`
	Limits   []string            `json:"limits"`
}

// Structural coverage and exact quotations can be checked deterministically;
// the interpretation of an apparent inconsistency remains a critic proposal.
func ReadBookReview(raw []byte, expected []string, corpus map[string]string) (BookReviewResult, error) {
	var r BookReviewResult
	if len(raw) > 256*1024 {
		return r, domain.ErrValidation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || len(r.Coverage) != len(expected) || len(r.Checks) != len(BookReviewCategories) || len(r.Findings) > 64 || len(r.Carry) > 64 || len(r.Limits) > 24 {
		return r, domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range r.Coverage {
		if seen[id] || !slices.Contains(expected, id) {
			return r, fmt.Errorf("%w：审稿章节覆盖不完整", domain.ErrValidation)
		}
		seen[id] = true
	}
	for _, key := range BookReviewCategories {
		if !slices.Contains([]string{"no_issue_found", "issues_found", "insufficient"}, r.Checks[key]) {
			return r, domain.ErrValidation
		}
	}
	quote := func(evidence []BookReviewEvidence) error {
		if len(evidence) < 1 || len(evidence) > 6 {
			return domain.ErrValidation
		}
		for _, e := range evidence {
			if len([]rune(e.Quote)) < 2 || len([]rune(e.Quote)) > 1200 || !strings.Contains(corpus[e.ChapterID], e.Quote) {
				return fmt.Errorf("%w：审稿引用未在对应章节原文中找到", domain.ErrValidation)
			}
		}
		return nil
	}
	validText := func(s string, n int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= n }
	seen = map[string]bool{}
	for _, f := range r.Findings {
		if seen[f.ID] || !validText(f.ID, 80) || !slices.Contains(BookReviewCategories, f.Category) || r.Checks[f.Category] != "issues_found" || !slices.Contains([]string{"major", "minor", "question"}, f.Severity) || !validText(f.Title, 160) || !validText(f.Explanation, 2000) || !validText(f.Suggestion, 2000) {
			return r, domain.ErrValidation
		}
		seen[f.ID] = true
		if e := quote(f.Evidence); e != nil {
			return r, e
		}
	}
	seen = map[string]bool{}
	for _, c := range r.Carry {
		if seen[c.Key] || !validText(c.Key, 80) || !slices.Contains(BookReviewCategories, c.Category) || !validText(c.Summary, 1000) || !slices.Contains([]string{"open", "resolved", "uncertain"}, c.Status) {
			return r, domain.ErrValidation
		}
		seen[c.Key] = true
		if e := quote(c.Evidence); e != nil {
			return r, e
		}
	}
	for _, limit := range r.Limits {
		if !validText(limit, 1200) {
			return r, domain.ErrValidation
		}
	}
	return r, nil
}
