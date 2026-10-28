package memory

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLongHistoryDoesNotOverflowThroughOmissionDiagnostics(t *testing.T) {
	items := make([]Item, 2048)
	for i := range items {
		items[i] = testItem(fmt.Sprintf("history-%04d", i))
		items[i].Text = strings.Repeat("过去发生的可选细节。", 64)
	}
	items[0].Pinned = true
	items[0].Text = "核心：守护亲人，不忘主线。"
	request := Request{Scope: testScope(), Budget: 8192, SourceEpoch: "e1", CompilerVersion: Version}
	pack, err := Compile(request, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Items) == 0 || pack.Items[0].Item.ID != items[0].ID || pack.OmittedCount < 2000 || len(pack.Omissions) > 16 || pack.EstimatedTokens > 8192 {
		t.Fatal("long history lost core or exceeded budget", pack.OmittedCount, pack.EstimatedTokens)
	}
}

func BenchmarkCompileLongHistory(b *testing.B) {
	items := make([]Item, 2048)
	for i := range items {
		items[i] = testItem(fmt.Sprintf("history-%04d", i))
		items[i].Text = strings.Repeat("历史证据与情节摘要。", 64)
	}
	items[0].Pinned = true
	request := Request{Scope: testScope(), Budget: 65536, SourceEpoch: "e1", CompilerVersion: Version}
	b.ResetTimer()
	for b.Loop() {
		if _, err := Compile(request, items); err != nil {
			b.Fatal(err)
		}
	}
}

func testScope() Scope {
	return Scope{Project: "p", Branch: "b", Canon: "v1", Actor: "owner", PermissionEpoch: "1", Purpose: "writer", Perspective: "hero", StoryTime: 10, NarrativeTime: 10, Session: "s", Timeline: "main"}
}
func testItem(id string) Item {
	return Item{ID: id, Revision: "r1", Project: "p", Branch: "b", Kind: "fact", State: "published", Visibility: "public", Text: "原文证据：" + id, Score: 1, Sources: []Source{{Resource: "chapter", Revision: "rev1", Hash: strings.Repeat("a", 64), Start: 0, End: 8}}}
}

func TestCompileKeepsCoreAndDependenciesBeforeOptionalHistory(t *testing.T) {
	a := testItem("core")
	a.Pinned = true
	a.Dependencies = []string{"setup"}
	a.Text = strings.Repeat("核心", 30)
	b := testItem("setup")
	c := testItem("irrelevant")
	c.Text = strings.Repeat("细枝末节", 1000)
	c.Score = 100
	input := Request{Scope: testScope(), Budget: 10000, OutputReserve: 1000, OverheadReserve: 1000, SourceEpoch: "e1", CompilerVersion: Version}
	pack, err := Compile(input, []Item{c, a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Items) != 2 || pack.Items[0].Item.ID != "core" || pack.Items[1].Item.ID != "setup" || len(pack.Omissions) != 1 {
		t.Fatal(pack)
	}
	input.Budget = 2200
	if _, err := Compile(input, []Item{a, b}); !errors.Is(err, ErrBudget) {
		t.Fatal("required overflow was not rejected", err)
	}
}
func TestCompileFiltersScopeTimeAndPOVBeforeRanking(t *testing.T) {
	scope := testScope()
	scope.Purpose = "npc"
	scope.Save = "save-1"
	public := testItem("visible")
	secret := testItem("secret")
	secret.Visibility = "restricted"
	secret.KnownBy = []string{"villain"}
	future := testItem("future")
	future.RevealAt = 11
	other := testItem("other")
	other.Branch = "other"
	retired := testItem("retired")
	until := int64(10)
	retired.Until = &until
	later := testItem("later-save")
	later.Save = "save-2"
	plan := testItem("future-plan")
	plan.State = "adopted_plan"
	plan.Kind = "highlight"
	plan.Control = true
	request := Request{Scope: scope, Budget: 20000, SourceEpoch: "e1", CompilerVersion: Version}
	pack, err := Compile(request, []Item{public, secret, future, other, retired, later, plan})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.ID != "visible" || len(pack.Omissions) != 0 {
		t.Fatal(pack, err)
	}
	request.RequiredIDs = []string{"secret"}
	if _, err = Compile(request, []Item{secret}); !errors.Is(err, ErrCoverage) {
		t.Fatal("secret required input must fail closed", err)
	}
}
func TestCompileDoesNotPromotePlansOrPrivateDrafts(t *testing.T) {
	plan := testItem("contract")
	plan.Kind = "theme"
	plan.State = "adopted_plan"
	plan.Visibility = "author"
	plan.Control = true
	plan.Pinned = true
	draft := testItem("draft")
	draft.State = "working"
	draft.Actor = "someone-else"
	draft.Session = "s"
	req := Request{Scope: testScope(), Budget: 20000, SourceEpoch: "e1", CompilerVersion: Version}
	pack, err := Compile(req, []Item{plan, draft})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Channel != "control" || pack.Items[0].Item.State != "adopted_plan" {
		t.Fatal(pack, err)
	}
}
func TestCompileDependencyGapsAndCyclesFailClosed(t *testing.T) {
	req := Request{Scope: testScope(), Budget: 20000, SourceEpoch: "e1", CompilerVersion: Version}
	a := testItem("a")
	a.Pinned = true
	a.Dependencies = []string{"b"}
	if _, err := Compile(req, []Item{a}); !errors.Is(err, ErrCoverage) {
		t.Fatal(err)
	}
	b := testItem("b")
	b.Dependencies = []string{"a"}
	if _, err := Compile(req, []Item{a, b}); !errors.Is(err, ErrCycle) {
		t.Fatal(err)
	}
}
func TestCompileIsDeterministicAndPinsEveryRelevantCacheDimension(t *testing.T) {
	req := Request{Scope: testScope(), Budget: 20000, SourceEpoch: "e1", CompilerVersion: Version}
	a, b := testItem("a"), testItem("b")
	first, err := Compile(req, []Item{b, a})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(req, []Item{a, b})
	if err != nil || first.Hash != second.Hash {
		t.Fatal("unstable compile", err)
	}
	key := CacheKey(req)
	changes := []Request{req, req, req, req, req, req, req, req}
	changes[0].Scope.Actor = "other"
	changes[1].Scope.PermissionEpoch = "2"
	changes[2].Scope.Canon = "v2"
	changes[3].Scope.StoryTime = 9
	changes[4].Scope.Save = "new-save"
	changes[5].SourceEpoch = "e2"
	changes[6].Budget = 10000
	changes[7].Scope.NarrativeTime = 9
	for _, r := range changes {
		if CacheKey(r) == key {
			t.Fatal("cache key ignores scope or budget")
		}
	}
}
func TestCompileRejectsUnattributedOrConflictingMemory(t *testing.T) {
	req := Request{Scope: testScope(), Budget: 20000, SourceEpoch: "e1", CompilerVersion: Version}
	a := testItem("a")
	a.Sources = nil
	if _, err := Compile(req, []Item{a}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unattributed memory accepted", err)
	}
	a = testItem("a")
	b := a
	b.Text = "different"
	if _, err := Compile(req, []Item{a, b}); !errors.Is(err, ErrInvalid) {
		t.Fatal("conflicting duplicate accepted", err)
	}
}
