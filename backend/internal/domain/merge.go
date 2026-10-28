package domain

import "fmt"

// ResolveMerge only accepts explicit choices for actual conflicts. The choice may
// preserve a deletion; relationship validation runs on the complete resulting graph.
func ResolveMerge(base, upstream, local Snapshot, resolutions map[string]string) (Snapshot, error) {
	merged, conflicts := MergeSnapshots(base, upstream, local)
	allowed := map[string]bool{}
	for _, c := range conflicts {
		key := c.Type + ":" + c.AssertionID
		allowed[key] = true
		choice := resolutions[key]
		if choice != "local" && choice != "upstream" {
			return merged, fmt.Errorf("合并冲突尚未解决: %s: %w", key, ErrConflict)
		}
		if choice == "local" {
			continue
		}
		switch c.Type {
		case "entity":
			merged.Entities = replaceMergeItem(merged.Entities, upstream.Entities, c.AssertionID, func(v Entity) string { return v.ID })
		case "assertion":
			merged.Assertions = replaceMergeItem(merged.Assertions, upstream.Assertions, c.AssertionID, func(v Assertion) string { return v.ID })
		case "rule":
			merged.Rules = replaceMergeItem(merged.Rules, upstream.Rules, c.AssertionID, func(v WorldRule) string { return v.ID })
		case "event":
			merged.Events = replaceMergeItem(merged.Events, upstream.Events, c.AssertionID, func(v StoryEvent) string { return v.ID })
		case "mapping":
			merged.Mappings = replaceMergeItem(merged.Mappings, upstream.Mappings, c.AssertionID, func(v EntityMapping) string { return v.ID })
		}
	}
	for key := range resolutions {
		if !allowed[key] {
			return merged, fmt.Errorf("合并选择不是当前冲突: %w", ErrValidation)
		}
	}
	return merged, ValidateSnapshot(merged)
}
func replaceMergeItem[T any](items, source []T, key string, id func(T) string) []T {
	result := []T{}
	for _, v := range items {
		if id(v) != key {
			result = append(result, v)
		}
	}
	for _, v := range source {
		if id(v) == key {
			result = append(result, v)
		}
	}
	return result
}
