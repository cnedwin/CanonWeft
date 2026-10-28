package domain

import "testing"

func TestChangeReviewScopeBindsResourcesWithoutInventingDependencies(t *testing.T) {
	ref := DependencyRef{Kind: "resource", BranchID: "b", ID: "chapter", Revision: "revision", Hash: "hash"}
	graph := DependencyGraph{Entries: []DependencyEntry{{Ref: ref}}, Frozen: map[string]bool{ref.Key(): true}}
	additional := []ChangeReviewScope{{Node: ref, Reason: "没有写出角色姓名，但这一段动作可能受其能力变化影响"}}
	items, err := ChangeReviewItems(nil, graph, additional)
	if err != nil || len(items) != 1 || items[0].Action != "DECIDE" || len(items[0].Via) != 0 {
		t.Fatal(items, err)
	}
	if len(graph.Links) != 0 {
		t.Fatal("review scope became a canonical dependency")
	}
	stale := additional[0]
	stale.Node.Revision = "old"
	if _, err := ChangeReviewItems(nil, graph, []ChangeReviewScope{stale}); err == nil {
		t.Fatal("old revision accepted")
	}
	if _, err := ChangeReviewItems(nil, graph, append(additional, additional...)); err == nil {
		t.Fatal("duplicate target accepted")
	}
	additional[0].Reason = " "
	if _, err := ChangeReviewItems(nil, graph, additional); err == nil {
		t.Fatal("missing decision reason accepted")
	}
}
