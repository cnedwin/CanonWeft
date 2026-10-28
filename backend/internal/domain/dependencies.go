package domain

import (
	"slices"
	"strings"
	"unicode/utf8"
)

type DependencyRef struct {
	Kind     string `json:"kind"`
	BranchID string `json:"branch_id"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
}

func (r DependencyRef) Key() string { return r.Kind + ":" + r.BranchID + ":" + r.ID }

type Dependency struct {
	Pinned bool          `json:"pinned,omitempty"`
	Source DependencyRef `json:"source"`
	Kind   string        `json:"kind"`
	Reason string        `json:"reason"`
}
type DependencyPolicy struct {
	Frozen bool         `json:"frozen"`
	Inputs []Dependency `json:"inputs"`
}

func (p DependencyPolicy) Validate() error {
	if len(p.Inputs) > 256 {
		return ErrValidation
	}
	seen := map[string]bool{}
	for _, d := range p.Inputs {
		if !slices.Contains([]string{"production", "semantic", "evidence", "representation", "timeline", "capability"}, d.Kind) || !slices.Contains([]string{"entity", "assertion", "rule", "event", "mapping", "resource", "skill"}, d.Source.Kind) || d.Source.ID == "" || d.Source.Revision == "" || len(d.Source.Hash) != 64 || strings.TrimSpace(d.Reason) == "" || !utf8.ValidString(d.Reason) || utf8.RuneCountInString(d.Reason) > 500 {
			return ErrValidation
		}
		if d.Source.Kind != "skill" && d.Source.BranchID == "" {
			return ErrValidation
		}
		key := d.Source.Key() + ":" + d.Kind
		if seen[key] {
			return ErrValidation
		}
		seen[key] = true
	}
	return nil
}

type DependencyLink struct {
	Pinned bool          `json:"pinned,omitempty"`
	Source DependencyRef `json:"source"`
	Target DependencyRef `json:"target"`
	Kind   string        `json:"kind"`
	Reason string        `json:"reason"`
}
type DependencyChange struct {
	Before DependencyRef `json:"before"`
	After  DependencyRef `json:"after"`
}
type ImpactItem struct {
	Node   DependencyRef    `json:"node"`
	Action string           `json:"action"`
	Via    []DependencyLink `json:"via"`
}

// A frozen node forbids automatic writes, not propagation. Downstream effects
// remain reviewable even when the exact new output cannot yet be known.
func DependencyImpact(changes []DependencyChange, links []DependencyLink, frozen map[string]bool) []ImpactItem {
	ranks := map[string]int{"REVIEW": 1, "REBUILD": 2, "DECIDE": 3}
	states := map[string]string{}
	roots := map[string]DependencyChange{}
	queue := []string{}
	for _, change := range changes {
		key := change.Before.Key()
		if change.Before.ID == "" {
			key = change.After.Key()
		}
		roots[key] = change
		states[key] = "REBUILD"
		queue = append(queue, key)
	}
	result := map[string]ImpactItem{}
	seenVia := map[string]bool{}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, link := range links {
			if link.Source.Key() != key {
				continue
			}
			if root, ok := roots[key]; ok && root.After.ID != "" && root.After.Hash == link.Source.Hash {
				continue
			}
			target := link.Target.Key()
			action := "REBUILD"
			if states[key] == "REVIEW" || states[key] == "DECIDE" || link.Pinned || link.Kind == "semantic" || link.Kind == "timeline" {
				action = "REVIEW"
			}
			if frozen[target] {
				action = "DECIDE"
			}
			item, exists := result[target]
			if !exists {
				item = ImpactItem{Node: link.Target, Action: action, Via: []DependencyLink{}}
			}
			if ranks[action] > ranks[item.Action] {
				item.Action = action
			}
			via := key + "\x00" + target + "\x00" + link.Kind
			if !seenVia[via] {
				item.Via = append(item.Via, link)
				seenVia[via] = true
			}
			result[target] = item
			if ranks[item.Action] > ranks[states[target]] {
				states[target] = item.Action
				queue = append(queue, target)
			}
		}
	}
	items := make([]ImpactItem, 0, len(result))
	for _, item := range result {
		slices.SortFunc(item.Via, func(a, b DependencyLink) int { return strings.Compare(a.Source.Key()+a.Kind, b.Source.Key()+b.Kind) })
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b ImpactItem) int { return strings.Compare(a.Node.Key(), b.Node.Key()) })
	return items
}
