package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func patchTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func patchFixture() (PatchTarget, TextPatch) {
	target := PatchTarget{ResourceID: strings.Repeat("a", 32), Revision: strings.Repeat("b", 32), Text: "序章：林舟🌙走进旧城。\n他没有回头。"}
	target.Hash = patchTestHash(target.Text)
	patch := TextPatch{ID: strings.Repeat("c", 32), GroupID: strings.Repeat("d", 32), ResourceID: target.ResourceID, BaseRevision: target.Revision, BaseHash: target.Hash, StartRune: 8, EndRune: 10, ExpectedSliceHash: patchTestHash("旧城"), Replacement: "新城", Reason: "按已确认的地点设定修改"}
	return target, patch
}

func TestTextPatchUsesExactUnicodeAnchorAndPreservesOtherText(t *testing.T) {
	target, patch := patchFixture()
	result, err := ApplyTextPatches(target, []TextPatch{patch})
	if err != nil {
		t.Fatal(err)
	}
	want := "序章：林舟🌙走进新城。\n他没有回头。"
	if result.Text != want || result.Hash != patchTestHash(want) || len(result.Applied) != 1 || result.Applied[0].ID != patch.ID {
		t.Fatalf("incorrect patched result: %+v", result)
	}
	if target.Text != "序章：林舟🌙走进旧城。\n他没有回头。" {
		t.Fatal("input mutated")
	}
}

func TestTextPatchRejectsStaleWrongScopeOverlapAndInvalidBytes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PatchTarget, *[]TextPatch)
		want   error
	}{
		{"stale-revision", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].BaseRevision = strings.Repeat("e", 32) }, ErrConflict},
		{"stale-hash", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].BaseHash = strings.Repeat("e", 64) }, ErrConflict},
		{"wrong-resource", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].ResourceID = strings.Repeat("e", 32) }, ErrConflict},
		{"shifted-anchor", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].StartRune-- }, ErrConflict},
		{"outside-range", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].EndRune = 10000 }, ErrValidation},
		{"negative-offset", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].StartRune = -1 }, ErrValidation},
		{"reversed-range", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].EndRune = 7 }, ErrValidation},
		{"duplicate-id", func(_ *PatchTarget, p *[]TextPatch) { *p = append(*p, (*p)[0]) }, ErrValidation},
		{"overlapping", func(_ *PatchTarget, p *[]TextPatch) {
			next := (*p)[0]
			next.ID = strings.Repeat("e", 32)
			next.StartRune = 9
			next.EndRune = 11
			next.ExpectedSliceHash = patchTestHash("城。")
			*p = append(*p, next)
		}, ErrValidation},
		{"mixed-group", func(_ *PatchTarget, p *[]TextPatch) {
			next := (*p)[0]
			next.ID = strings.Repeat("e", 32)
			next.GroupID = strings.Repeat("f", 32)
			*p = append(*p, next)
		}, ErrValidation},
		{"no-reason", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].Reason = "  " }, ErrValidation},
		{"invalid-utf8", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].Replacement = string([]byte{0xff}) }, ErrValidation},
		{"oversize", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].Replacement = strings.Repeat("x", 256*1024) }, ErrValidation},
		{"no-change", func(_ *PatchTarget, p *[]TextPatch) { (*p)[0].Replacement = "旧城" }, ErrValidation},
		{"corrupt-original", func(target *PatchTarget, _ *[]TextPatch) { target.Text += "被改动" }, ErrNotReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, p := patchFixture()
			patches := []TextPatch{p}
			c.mutate(&target, &patches)
			if _, err := ApplyTextPatches(target, patches); !errors.Is(err, c.want) {
				t.Fatalf("got %v want %v", err, c.want)
			}
		})
	}
}

func TestTextPatchOrderIndependentInsertionAndDeletion(t *testing.T) {
	target, first := patchFixture()
	first.StartRune, first.EndRune, first.ExpectedSliceHash, first.Replacement = 0, 0, patchTestHash(""), "开篇\n"
	last := first
	last.ID = strings.Repeat("e", 32)
	last.StartRune, last.EndRune = 12, len([]rune(target.Text))
	last.ExpectedSliceHash, last.Replacement = patchTestHash("他没有回头。"), ""
	a, err := ApplyTextPatches(target, []TextPatch{last, first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ApplyTextPatches(target, []TextPatch{first, last})
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "开篇\n序章：林舟🌙走进旧城。\n" || a.Hash != b.Hash || a.Applied[0].ID != first.ID {
		t.Fatalf("order affected patch result: %+v %+v", a, b)
	}
}
