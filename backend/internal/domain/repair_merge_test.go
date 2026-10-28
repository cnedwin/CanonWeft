package domain

import (
	"strings"
	"testing"
)

func TestMergeContinuityRepairPreservesCandidateAndReportsOverlap(t *testing.T) {
	target := PatchTarget{ResourceID: strings.Repeat("a", 32), Revision: strings.Repeat("b", 32), Text: "🌙旧城。人物低声说话。"}
	target.Hash = patchHash(target.Text)
	p := TextPatch{ID: strings.Repeat("d", 32), GroupID: strings.Repeat("c", 32), ResourceID: target.ResourceID, BaseRevision: target.Revision, BaseHash: target.Hash, StartRune: 1, EndRune: 3, ExpectedSliceHash: patchHash("旧城"), Replacement: "新城", Reason: "修订地点"}
	merged, err := MergeContinuityRepair(target, "🌙旧城。人物低声说话。雨下得更大了。", []TextPatch{p})
	if err != nil || merged != "🌙新城。人物低声说话。雨下得更大了。" {
		t.Fatal(merged, err)
	}
	if _, err := MergeContinuityRepair(target, "🌙海港。人物低声说话。", []TextPatch{p}); err != ErrConflict {
		t.Fatal("overlap did not require decision", err)
	}
	same, err := MergeContinuityRepair(target, "🌙新城。人物低声说话。", []TextPatch{p})
	if err != nil || same != "🌙新城。人物低声说话。" {
		t.Fatal("same correction conflicted", same, err)
	}
	if merged, err = MergeContinuityRepair(target, "题记。🌙旧城。人物低声说话。", []TextPatch{p}); err != nil || merged != "题记。🌙新城。人物低声说话。" {
		t.Fatal("unicode anchor did not move", merged, err)
	}
}
