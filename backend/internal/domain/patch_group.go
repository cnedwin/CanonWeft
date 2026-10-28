package domain

import "sort"

// TextPatchGroup declares an indivisible set of edits. The service additionally
// verifies that Members contains the complete dependency group of the plan.
type TextPatchGroup struct {
	CompensatesReleaseID string      `json:"compensates_release_id,omitempty"`
	ID                   string      `json:"id"`
	Members              []string    `json:"members"`
	Patches              []TextPatch `json:"patches"`
}

type PatchedResource struct {
	Target PatchTarget     `json:"target"`
	Result TextPatchResult `json:"result"`
}

// ApplyTextPatchGroup returns no partial result when any member is absent,
// stale or invalid. Results have deterministic resource order.
func ApplyTextPatchGroup(targets []PatchTarget, group TextPatchGroup) ([]PatchedResource, error) {
	if group.CompensatesReleaseID != "" && !patchHex(group.CompensatesReleaseID, 32) {
		return nil, ErrValidation
	}
	if !patchHex(group.ID, 32) || len(group.Members) == 0 || len(group.Members) > 32 || len(targets) != len(group.Members) || len(group.Patches) == 0 || len(group.Patches) > MaxTextPatches {
		return nil, ErrValidation
	}
	members := make(map[string]bool, len(group.Members))
	for _, id := range group.Members {
		if !patchHex(id, 32) || members[id] {
			return nil, ErrValidation
		}
		members[id] = true
	}
	byID := make(map[string]PatchTarget, len(targets))
	for _, target := range targets {
		if !members[target.ResourceID] {
			return nil, ErrValidation
		}
		if _, exists := byID[target.ResourceID]; exists {
			return nil, ErrValidation
		}
		byID[target.ResourceID] = target
	}
	patches := make(map[string][]TextPatch, len(targets))
	seen := make(map[string]bool, len(group.Patches))
	for _, p := range group.Patches {
		if !members[p.ResourceID] || p.GroupID != group.ID || seen[p.ID] {
			return nil, ErrValidation
		}
		seen[p.ID] = true
		patches[p.ResourceID] = append(patches[p.ResourceID], p)
	}
	ids := append([]string(nil), group.Members...)
	sort.Strings(ids)
	results := make([]PatchedResource, 0, len(ids))
	for _, id := range ids {
		result, err := ApplyTextPatches(byID[id], patches[id])
		if err != nil {
			return nil, err
		}
		results = append(results, PatchedResource{Target: byID[id], Result: result})
	}
	return results, nil
}
