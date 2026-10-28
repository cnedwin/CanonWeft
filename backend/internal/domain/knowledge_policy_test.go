package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWithdrawnKnowledgeExcludedWithoutChangingLegacyHashes(t *testing.T) {
	p := KnowledgePolicy{Space: "working", Visibility: "author"}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "withdraw") {
		t.Fatal("legacy policy serialization changed")
	}
	p.Withdrawn = true
	if p.Validate() == nil {
		t.Fatal("withdrawal requires reason")
	}
	p.WithdrawalReason = "原机构撤回结论，等待复核"
	if p.Validate() != nil {
		t.Fatal("valid withdrawal rejected")
	}
	for _, role := range []string{"writer", "npc", "author", "critic", "research"} {
		if p.Allows(role, "", 100, true, true) {
			t.Fatal("withdrawn source allowed", role)
		}
	}
	p.Withdrawn = false
	if p.Validate() == nil {
		t.Fatal("active policy retained ambiguous withdrawal reason")
	}
	p.WithdrawalReason = ""
	if !p.Allows("author", "", 0, true, false) {
		t.Fatal("explicit restored policy rejected")
	}
}

func TestKnowledgeSpaceAndRevealGates(t *testing.T) {
	p := KnowledgePolicy{Space: "evidence", Visibility: "restricted", KnownBy: []string{"a"}, RevealAt: 12}
	if !p.Allows("author", "", 0, false, false) || p.Allows("npc", "a", 11, false, false) || p.Allows("npc", "b", 12, false, false) || !p.Allows("npc", "a", 12, false, false) {
		t.Fatal("knowledge/time gate failed")
	}
	for _, space := range []string{"working", "methods", "research"} {
		p.Space = space
		p.Visibility = "author"
		if p.Allows("npc", "a", 99, true, true) || p.Allows("writer", "a", 99, true, true) {
			t.Fatal("non-story source entered character expression", space)
		}
	}
	p.Space = "research"
	if p.Allows("author", "", 0, true, true) || !p.Allows("research", "", 0, false, false) {
		t.Fatal("research activated in production")
	}
	p.Space = "working"
	if p.Allows("author", "", 0, false, false) || !p.Allows("author", "", 0, true, false) {
		t.Fatal("working materials require explicit selection")
	}
	p.Space = "methods"
	if p.Allows("critic", "", 0, false, false) || !p.Allows("critic", "", 0, false, true) {
		t.Fatal("methods require explicit selection")
	}
	if !DefaultKnowledgePolicy().Allows("author", "", 0, false, false) || DefaultKnowledgePolicy().Allows("npc", "a", 0, false, false) {
		t.Fatal("legacy source defaults leaked")
	}
}
