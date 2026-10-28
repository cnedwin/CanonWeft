package domain

import (
	"strings"
	"testing"
)

func TestPatchDraftPreservesUnfinishedFormsWithoutGrantingPatchValidity(t *testing.T) {
	draft := PatchDraftContent{Group: TextPatchGroup{ID: strings.Repeat("a", 32)}, OperationID: "pending-candidate", Snippet: "作者仍在输入🌙", Reason: "尚未选目标"}
	if err := draft.Validate(); err != nil {
		t.Fatal("unfinished form cannot be saved", err)
	}
	if _, err := ApplyTextPatchGroup(nil, draft.Group); err == nil {
		t.Fatal("saved draft became executable")
	}
}

func TestPatchDraftRejectsMalformedScopeEncodingAndExcessPayload(t *testing.T) {
	base := PatchDraftContent{Group: TextPatchGroup{ID: strings.Repeat("a", 32)}, OperationID: "draft-operation"}
	for _, change := range []func(*PatchDraftContent){
		func(d *PatchDraftContent) { d.SelectedResourceID = "../outside" },
		func(d *PatchDraftContent) { d.BaseIPHead = "unversioned" },
		func(d *PatchDraftContent) { d.Reason = string([]byte{0xff}) },
		func(d *PatchDraftContent) { d.Snippet = strings.Repeat("x", MaxPatchedTextBytes+1) },
		func(d *PatchDraftContent) { d.Group.CompensatesReleaseID = "other-project" },
		func(d *PatchDraftContent) { d.OperationID = "" },
	} {
		draft := base
		change(&draft)
		if err := draft.Validate(); err == nil {
			t.Fatal("invalid draft accepted", draft.SelectedResourceID)
		}
	}
}
