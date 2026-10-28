package domain

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// KnowledgePolicy belongs to an immutable source revision. "methods" is reference
// material, never a grant to execute tools or an evaluated Skill qualification.
type KnowledgePolicy struct {
	Withdrawn        bool     `json:"withdrawn,omitempty"`
	WithdrawalReason string   `json:"withdrawal_reason,omitempty"`
	Space            string   `json:"space"`
	Visibility       string   `json:"visibility"`
	KnownBy          []string `json:"known_by,omitempty"`
	RevealAt         int64    `json:"reveal_at"`
}

func DefaultKnowledgePolicy() KnowledgePolicy {
	return KnowledgePolicy{Space: "evidence", Visibility: "author"}
}

func (p KnowledgePolicy) Validate() error {
	if !utf8.ValidString(p.WithdrawalReason) || utf8.RuneCountInString(p.WithdrawalReason) > 2000 || (p.Withdrawn && strings.TrimSpace(p.WithdrawalReason) == "") || (!p.Withdrawn && p.WithdrawalReason != "") {
		return ErrValidation
	}
	if !slices.Contains([]string{"evidence", "working", "methods", "research"}, p.Space) || !slices.Contains([]string{"author", "public", "restricted"}, p.Visibility) || len(p.KnownBy) > 128 {
		return ErrValidation
	}
	if p.Space != "evidence" && p.Visibility != "author" {
		return ErrValidation
	}
	if p.Visibility == "restricted" && len(p.KnownBy) == 0 {
		return ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range p.KnownBy {
		if id == "" || len(id) > 128 || seen[id] {
			return ErrValidation
		}
		seen[id] = true
	}
	return nil
}

func (p KnowledgePolicy) Allows(purpose, perspective string, at int64, working, methods bool) bool {
	if p.Validate() != nil || p.Withdrawn {
		return false
	}
	if purpose == "npc" || purpose == "writer" {
		return p.Space == "evidence" && at >= p.RevealAt && (p.Visibility == "public" || (p.Visibility == "restricted" && slices.Contains(p.KnownBy, perspective)))
	}
	if !slices.Contains([]string{"author", "critic", "research"}, purpose) {
		return false
	}
	switch p.Space {
	case "evidence":
		return true
	case "working":
		return working
	case "methods":
		return methods
	case "research":
		return purpose == "research"
	}
	return false
}
