package domain

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Additional review is an author's inspection scope, not a discovered dependency
// or a verdict that content is affected, unaffected, or already repaired.
type ChangeReviewScope struct {
	Node   DependencyRef `json:"node"`
	Reason string        `json:"reason"`
}

func ChangeReviewItems(changes []DependencyChange, graph DependencyGraph, additional []ChangeReviewScope) ([]ImpactItem, error) {
	if len(additional) > 128 {
		return nil, ErrValidation
	}
	items := DependencyImpact(changes, graph.Links, graph.Frozen)
	entries := map[string]DependencyRef{}
	for _, entry := range graph.Entries {
		entries[entry.Ref.Key()] = entry.Ref
	}
	seen, included := map[string]bool{}, map[string]bool{}
	for _, item := range items {
		included[item.Node.Key()] = true
	}
	for _, choice := range additional {
		ref := choice.Node
		if ref.Kind != "resource" || entries[ref.Key()] != ref || seen[ref.Key()] || strings.TrimSpace(choice.Reason) == "" || !utf8.ValidString(choice.Reason) || utf8.RuneCountInString(choice.Reason) > 400 {
			return nil, ErrValidation
		}
		seen[ref.Key()] = true
		if included[ref.Key()] {
			continue
		}
		action := "REVIEW"
		if graph.Frozen[ref.Key()] {
			action = "DECIDE"
		}
		items = append(items, ImpactItem{Node: ref, Action: action, Via: []DependencyLink{}})
	}
	slices.SortFunc(items, func(a, b ImpactItem) int { return strings.Compare(a.Node.Key(), b.Node.Key()) })
	return items, nil
}

func ChangeOutsideRefs(changes []DependencyChange, items []ImpactItem, graph DependencyGraph) []DependencyRef {
	affected := map[string]bool{}
	for _, change := range changes {
		affected[change.Before.Key()], affected[change.After.Key()] = true, true
	}
	for _, item := range items {
		affected[item.Node.Key()] = true
	}
	outside := []DependencyRef{}
	for _, entry := range graph.Entries {
		if entry.Ref.Kind == "resource" && !affected[entry.Ref.Key()] {
			outside = append(outside, entry.Ref)
		}
	}
	return outside
}
