package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestTextPatchGroupRequiresEveryMemberAndUsesOriginalSnapshots(t *testing.T) {
	first, p := patchFixture()
	second := first
	second.ResourceID = strings.Repeat("e", 32)
	q := p
	q.ID, q.ResourceID = strings.Repeat("f", 32), second.ResourceID
	group := TextPatchGroup{ID: p.GroupID, Members: []string{first.ResourceID, second.ResourceID}, Patches: []TextPatch{q, p}}
	results, err := ApplyTextPatchGroup([]PatchTarget{second, first}, group)
	if err != nil || len(results) != 2 || results[0].Target.ResourceID != first.ResourceID || results[1].Result.Text != "序章：林舟🌙走进新城。\n他没有回头。" {
		t.Fatalf("group failed: %+v %v", results, err)
	}
	group.Patches = group.Patches[:1]
	if _, err = ApplyTextPatchGroup([]PatchTarget{first, second}, group); !errors.Is(err, ErrValidation) {
		t.Fatalf("accepted incomplete group: %v", err)
	}
}

func TestTextPatchGroupRejectsUndeclaredDuplicateAndMixedGroups(t *testing.T) {
	first, p := patchFixture()
	cases := []struct {
		name   string
		mutate func(*TextPatchGroup, *[]PatchTarget)
	}{
		{"duplicate-member", func(g *TextPatchGroup, _ *[]PatchTarget) { g.Members = append(g.Members, g.Members[0]) }},
		{"missing-snapshot", func(_ *TextPatchGroup, targets *[]PatchTarget) { *targets = nil }},
		{"duplicate-snapshot", func(_ *TextPatchGroup, targets *[]PatchTarget) { *targets = append(*targets, (*targets)[0]) }},
		{"undeclared-member", func(g *TextPatchGroup, _ *[]PatchTarget) { g.Members = []string{strings.Repeat("e", 32)} }},
		{"wrong-group", func(g *TextPatchGroup, _ *[]PatchTarget) { g.ID = strings.Repeat("e", 32) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := TextPatchGroup{ID: p.GroupID, Members: []string{first.ResourceID}, Patches: []TextPatch{p}}
			targets := []PatchTarget{first}
			c.mutate(&g, &targets)
			if _, err := ApplyTextPatchGroup(targets, g); !errors.Is(err, ErrValidation) {
				t.Fatalf("accepted invalid group: %v", err)
			}
		})
	}
}
