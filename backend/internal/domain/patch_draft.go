package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// PatchDraftContent is an unfinished private work surface. Validate only bounds
// its data shape; it does not approve anchors, compensation or publication.
type PatchDraftContent struct {
	BaseIPHead           string         `json:"base_ip_head"`
	OperationID          string         `json:"operation_id"`
	Group                TextPatchGroup `json:"group"`
	SelectedResourceID   string         `json:"selected_resource_id"`
	Snippet              string         `json:"snippet"`
	Replacement          string         `json:"replacement"`
	Reason               string         `json:"reason"`
	CompensationSourceID string         `json:"compensation_source_id"`
	CompensationReason   string         `json:"compensation_reason"`
}

func (d PatchDraftContent) Validate() error {
	for _, id := range []string{d.BaseIPHead, d.SelectedResourceID, d.CompensationSourceID, d.Group.CompensatesReleaseID} {
		if id != "" && !patchHex(id, 32) {
			return ErrValidation
		}
	}
	if !patchHex(d.Group.ID, 32) || !utf8.ValidString(d.OperationID) || strings.TrimSpace(d.OperationID) == "" || utf8.RuneCountInString(d.OperationID) > 120 || len(d.Group.Members) > 32 || len(d.Group.Patches) > MaxTextPatches {
		return ErrValidation
	}
	for _, text := range []string{d.Snippet, d.Replacement} {
		if !utf8.ValidString(text) || len(text) > MaxPatchedTextBytes {
			return ErrValidation
		}
	}
	for _, reason := range []string{d.Reason, d.CompensationReason} {
		if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 2000 {
			return ErrValidation
		}
	}
	members := map[string]bool{}
	for _, id := range d.Group.Members {
		if !patchHex(id, 32) || members[id] {
			return ErrValidation
		}
		members[id] = true
	}
	ids := map[string]bool{}
	for _, p := range d.Group.Patches {
		if !patchHex(p.ID, 32) || ids[p.ID] || p.GroupID != d.Group.ID || !members[p.ResourceID] || !patchHex(p.BaseRevision, 32) || !patchHex(p.BaseHash, 64) || !patchHex(p.ExpectedSliceHash, 64) || p.StartRune < 0 || p.EndRune < p.StartRune || p.EndRune > MaxPatchedTextBytes {
			return ErrValidation
		}
		if !utf8.ValidString(p.Replacement) || len(p.Replacement) > MaxPatchedTextBytes || !utf8.ValidString(p.Reason) || utf8.RuneCountInString(p.Reason) > 2000 {
			return ErrValidation
		}
		ids[p.ID] = true
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > 1024*1024 {
		return ErrValidation
	}
	return nil
}
