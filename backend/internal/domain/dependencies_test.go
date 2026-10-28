package domain

import "testing"

func TestFrozenDependenciesStillPropagateAndCyclesTerminate(t *testing.T) {
	root := DependencyRef{Kind: "entity", BranchID: "canon", ID: "e-a", Revision: "ip-old", Hash: "before"}
	a := DependencyRef{Kind: "resource", BranchID: "adapted", ID: "a", Revision: "a1", Hash: "a-hash"}
	b := DependencyRef{Kind: "resource", BranchID: "adapted", ID: "b", Revision: "b1", Hash: "b-hash"}
	c := DependencyRef{Kind: "resource", BranchID: "adapted", ID: "c", Revision: "c1", Hash: "c-hash"}
	links := []DependencyLink{
		{Source: root, Target: a, Kind: "representation", Reason: "角色形象"},
		{Source: a, Target: b, Kind: "production", Reason: "后续说明"},
		{Source: b, Target: c, Kind: "semantic", Reason: "因果关系"},
		{Source: c, Target: a, Kind: "timeline", Reason: "交叉引用"},
	}
	changes := []DependencyChange{{Before: root, After: DependencyRef{Kind: "entity", BranchID: "canon", ID: "e-a", Revision: "ip-new", Hash: "after"}}}
	impact := DependencyImpact(changes, links, map[string]bool{a.Key(): true})
	if len(impact) != 3 {
		t.Fatal("frozen node swallowed descendants or cycle repeated", impact)
	}
	for _, item := range impact {
		if item.Node.ID == "a" && item.Action != "DECIDE" {
			t.Fatal(item)
		}
		if item.Node.ID != "a" && item.Action != "REVIEW" {
			t.Fatal("frozen downstream must be reviewed", item)
		}
	}
	// The separate branch with a same-named stable ID is not the changed source.
	changes[0].Before.BranchID = "other"
	changes[0].After.BranchID = "other"
	if got := DependencyImpact(changes, links, nil); len(got) != 0 {
		t.Fatal("cross-branch contamination", got)
	}
}

func TestUnrelatedDependencyAndAlreadyMatchingHashStayUsable(t *testing.T) {
	source := DependencyRef{Kind: "assertion", BranchID: "canon", ID: "f-1", Revision: "v1", Hash: "old"}
	target := DependencyRef{Kind: "resource", BranchID: "canon", ID: "chapter", Revision: "c1", Hash: "unchanged"}
	newSource := source
	newSource.Revision = "v2"
	newSource.Hash = "new"
	links := []DependencyLink{{Source: source, Target: target, Kind: "evidence", Reason: "伤势依据"}}
	impact := DependencyImpact([]DependencyChange{{Before: source, After: newSource}}, links, nil)
	if len(impact) != 1 || impact[0].Action != "REBUILD" || impact[0].Node.Hash != "unchanged" {
		t.Fatal(impact)
	}
	links[0].Source = newSource
	if got := DependencyImpact([]DependencyChange{{Before: source, After: newSource}}, links, nil); len(got) != 0 {
		t.Fatal("matching immutable input unnecessarily rebuilt", got)
	}
}
