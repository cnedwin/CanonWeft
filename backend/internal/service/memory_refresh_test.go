package service

import (
	"novelstudio/internal/memory"
	"testing"
)

func TestRefreshContextRemovesOnlyTargetAndSummaryAncestors(t *testing.T) {
	items := []memory.Item{{ID: "chapter", Summary: &memory.Summary{Level: "chapter"}}, {ID: "volume", Summary: &memory.Summary{Level: "volume", Inputs: []memory.SummaryPin{{ID: "chapter"}}}}, {ID: "book", Summary: &memory.Summary{Level: "book", Inputs: []memory.SummaryPin{{ID: "volume"}}}}, {ID: "theme", Kind: "theme", Pinned: true, Dependencies: []string{"chapter"}}, {ID: "unrelated", Summary: &memory.Summary{Level: "chapter"}, Pinned: true}}
	got := refreshContextItems(items, "chapter")
	if len(got) != 2 || got[0].ID != "theme" || got[1].ID != "unrelated" || len(got[0].Dependencies) != 1 {
		t.Fatal("refresh weakened unrelated anchors or hard dependencies", got)
	}
}

func TestRefreshPreservesPolicyAndIdentity(t *testing.T) {
	until := int64(9)
	prior := memory.Item{ID: "same", Pinned: true, Control: true, Visibility: "restricted", KnownBy: []string{"reader"}, Dependencies: []string{"core"}, From: 3, RevealAt: 4, Until: &until}
	got := preserveRefreshPolicy(memory.Item{ID: "new", Text: "new", From: 5}, prior)
	if got.ID != "same" || !got.Pinned || !got.Control || got.From != 5 || got.RevealAt != 4 || got.Until == nil || *got.Until != 9 || got.Visibility != "restricted" || got.Dependencies[0] != "core" || got.KnownBy[0] != "reader" {
		t.Fatal("refresh lost policy", got)
	}
}
