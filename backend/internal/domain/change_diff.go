package domain

import (
	"encoding/json"
	"slices"
	"strings"
)

func SnapshotValues(s Snapshot) map[string]any {
	result := map[string]any{}
	for _, e := range s.Entities {
		result["entity:"+e.ID] = e
	}
	for _, e := range s.Assertions {
		result["assertion:"+e.ID] = e
	}
	for _, e := range s.Rules {
		result["rule:"+e.ID] = e
	}
	for _, e := range s.Events {
		result["event:"+e.ID] = e
	}
	for _, e := range s.Mappings {
		result["mapping:"+e.ID] = e
	}
	return result
}
func diffValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "（未设置）"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}
func DependencyDifferences(changes []DependencyChange, before, after Snapshot) []DependencyDifference {
	old, newer := SnapshotValues(before), SnapshotValues(after)
	result := []DependencyDifference{}
	for _, change := range changes {
		ref := change.After
		if ref.ID == "" {
			ref = change.Before
		}
		key := ref.Kind + ":" + ref.ID
		a, b := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		raw, _ := json.Marshal(old[key])
		_ = json.Unmarshal(raw, &a)
		raw, _ = json.Marshal(newer[key])
		_ = json.Unmarshal(raw, &b)
		names := map[string]bool{}
		for name := range a {
			names[name] = true
		}
		for name := range b {
			names[name] = true
		}
		diff := DependencyDifference{Ref: ref, Fields: []ChangedField{}}
		if name := b["name"]; len(name) > 0 {
			diff.Name = diffValue(name)
		} else if name := a["name"]; len(name) > 0 {
			diff.Name = diffValue(name)
		} else {
			diff.Name = ref.ID
		}
		for name := range names {
			if string(a[name]) != string(b[name]) {
				diff.Fields = append(diff.Fields, ChangedField{Name: name, Before: diffValue(a[name]), After: diffValue(b[name])})
			}
		}
		slices.SortFunc(diff.Fields, func(a, b ChangedField) int { return strings.Compare(a.Name, b.Name) })
		result = append(result, diff)
	}
	return result
}
