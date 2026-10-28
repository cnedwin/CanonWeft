package memory

import (
	"strings"
	"testing"
)

func TestSummaryTitleUsesCharacterLimit(t *testing.T) {
	item := testItem("chapter")
	item.Kind = "summary"
	item.Summary = &Summary{Level: "chapter", UnitID: "unit", Title: strings.Repeat("章", 160)}
	if err := ValidateItem(item); err != nil {
		t.Fatal("valid Chinese title rejected", err)
	}
	item.Summary.Title += "章"
	if ValidateItem(item) == nil {
		t.Fatal("overlong title accepted")
	}
}

func TestSummaryRequiresExactAdjacentConfirmedInputs(t *testing.T) {
	child := testItem("chapter-1")
	child.Kind = "summary"
	child.Summary = &Summary{Level: "chapter", UnitID: "unit-1", Title: "第一章"}
	parent := testItem("volume-1")
	parent.Kind = "summary"
	parent.Sources = nil
	pin := SummaryPin{ID: child.ID, Revision: child.Revision, Hash: SummaryTextHash(child.Text)}
	parent.Summary = &Summary{Level: "volume", UnitID: "volume-unit", Title: "第一卷", Inputs: []SummaryPin{pin}}
	if err := ValidateItem(parent); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSummarySource(parent, child, pin); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Item){
		func(i *Item) { i.Revision = "new" }, func(i *Item) { i.State = "candidate" }, func(i *Item) { i.Visibility = "author" },
		func(i *Item) { i.Control = true }, func(i *Item) { i.RevealAt = 20 }, func(i *Item) { i.Timeline = "other" },
		func(i *Item) { i.Summary = &Summary{Level: "scene", UnitID: "s", Title: "s"} },
	} {
		changed := child
		mutate(&changed)
		if ValidateSummarySource(parent, changed, pin) == nil {
			t.Fatal("invalid lower summary accepted", changed)
		}
	}
	parent.Summary.Inputs = nil
	if ValidateItem(parent) == nil {
		t.Fatal("volume without inputs accepted")
	}
}

func TestSummaryProvenanceDoesNotExpandIntoPromptDependencies(t *testing.T) {
	parent := testItem("volume")
	parent.Kind = "summary"
	parent.Pinned = true
	parent.Sources = nil
	parent.Summary = &Summary{Level: "volume", UnitID: "u", Title: "卷摘要", Inputs: []SummaryPin{{ID: "chapter", Revision: "r", Hash: SummaryTextHash("长篇正文")}}}
	pack, err := Compile(Request{Scope: testScope(), Budget: 8192, SourceEpoch: "validated", CompilerVersion: Version}, []Item{parent})
	if err != nil || len(pack.Items) != 1 {
		t.Fatal("provenance forced full lower text into prompt", err)
	}
}
