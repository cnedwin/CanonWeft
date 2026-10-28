package retention

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Decode the wire field so this regression fails on behavior before adding
// the new Go field, rather than failing solely because a symbol is absent.
func attachSource(t *testing.T, target, source Reference) Reference {
	t.Helper()
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["sources"] = []any{map[string]any{"reference_id": source.ID, "revision": source.Revision, "payload_hash": source.PayloadHash}}
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	return target
}

func sourcePin(source Reference) ReferenceSource {
	return ReferenceSource{ReferenceID: source.ID, Revision: source.Revision, PayloadHash: source.PayloadHash}
}

func TestDerivedDeadlineUsesPinnedHistoricalVersionAfterAdoption(t *testing.T) {
	inv, now := fixtureInventory()
	old := fixtureRef("1", "ip_revisions.payload", "", now.Add(-31*day))
	current := old
	current.Revision++
	current.ContentRoot = true
	copy := attachSource(t, fixtureRef("2", "working_chunks.content", "", now), old)
	inv.References = []Reference{current, copy}
	raw, _ := json.Marshal(inv)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["historical_references"] = []Reference{old}
	raw, _ = json.Marshal(wire)
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 1 || plan.Expire[0].ReferenceID != copy.ID || !plan.Expire[0].Deadline.Equal(now.Add(-day)) {
		t.Fatal("old copy rebound to adopted source", plan, err)
	}
	old.PayloadHash = strings.Repeat("e", 64)
	wire["historical_references"] = []Reference{old}
	raw, _ = json.Marshal(wire)
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("substituted historical payload accepted")
	}
}

func TestDerivedDeadlineHistoryCanonicalAndOnlyReachableEvidence(t *testing.T) {
	inv, now := fixtureInventory()
	old := fixtureRef("1", "ip_revisions.payload", "", now.Add(-31*day))
	other := fixtureRef("3", "generation_previews.content", "", now.Add(-29*day))
	current := old
	current.Revision++
	current.ContentRoot = true
	copy := fixtureRef("2", "working_chunks.content", "", now)
	copy.Sources = []ReferenceSource{sourcePin(old), sourcePin(other)}
	inv.References = []Reference{copy, current}
	inv.HistoricalReferences = []Reference{old, other}
	before, _ := json.Marshal(inv)
	first, err := BuildPlan(inv, now)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(inv)
	if string(before) != string(after) {
		t.Fatal("historical evidence mutated")
	}
	slices.Reverse(inv.HistoricalReferences)
	slices.Reverse(inv.References[0].Sources)
	second, err := BuildPlan(inv, now)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("historical ordering changed plan", first, second, err)
	}
	inv.HistoricalReferences = append(inv.HistoricalReferences, fixtureRef("4", "generation_previews.content", "", now))
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("unrelated historical record accepted")
	}
}

func TestDerivedDeadlineRejectsUnprovenLineage(t *testing.T) {
	for _, mode := range []string{"missing", "self", "cycle", "duplicate", "stale-version", "wrong-hash", "content-promotion", "accounting-target", "accounting-source", "too-many-sources"} {
		t.Run(mode, func(t *testing.T) {
			inv, now := fixtureInventory()
			source := fixtureRef("1", "generation_previews.content", "", now.Add(-31*day))
			copy := attachSource(t, fixtureRef("2", "working_chunks.content", "", now), source)
			switch mode {
			case "missing":
				copy.Sources[0].ReferenceID = strings.Repeat("3", 64)
			case "self":
				copy.Sources[0] = sourcePin(copy)
			case "cycle":
				source.Sources = []ReferenceSource{sourcePin(copy)}
			case "duplicate":
				copy.Sources = append(copy.Sources, copy.Sources[0])
			case "stale-version":
				source.Revision++
			case "wrong-hash":
				source.PayloadHash = strings.Repeat("e", 64)
			case "content-promotion":
				copy.Rule, copy.ContentRoot = "knowledge_chunks.content", true
			case "accounting-target":
				copy.Rule, copy.AccountingOpen = "model_calls.accounting", true
			case "accounting-source":
				source.Rule = "model_calls.accounting"
			case "too-many-sources":
				copy.Sources = make([]ReferenceSource, 4097)
			}
			inv.References = []Reference{source, copy}
			if plan, err := BuildPlan(inv, now); err == nil || !reflect.DeepEqual(plan, Plan{}) {
				t.Fatal("unproven lineage returned a usable plan", plan, err)
			}
		})
	}
}

func TestDerivedDeadlineMultipleSourcesCanonicalHashAndOwnTTL(t *testing.T) {
	inv, now := fixtureInventory()
	content := fixtureRef("1", "ip_drafts.payload", "", now.Add(-100*day))
	content.ContentRoot = true
	runtime := fixtureRef("2", "generation_previews.content", "", now.Add(-29*day))
	copy := fixtureRef("3", "working_chunks.content", "", now)
	copy.Sources = []ReferenceSource{sourcePin(runtime), sourcePin(content)}
	inv.References = []Reference{copy, content, runtime}
	before, _ := json.Marshal(inv)
	first, err := BuildPlan(inv, now)
	if err != nil || len(first.Expire) != 0 {
		t.Fatal(first, err)
	}
	after, _ := json.Marshal(inv)
	if string(before) != string(after) {
		t.Fatal("planner mutated the caller's lineage order")
	}
	slices.Reverse(copy.Sources)
	inv.References = []Reference{runtime, copy, content}
	second, err := BuildPlan(inv, now)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("source order changed the plan", err)
	}
	deadline, err := referenceDeadlines(inv.References, inv.Lifecycle, now)
	if err != nil || !deadline[copy.ID].Equal(now.Add(day)) || !deadline[content.ID].IsZero() {
		t.Fatal("published parent suppressed an earlier raw source expiry", deadline, err)
	}
	// Removing a source changes the integrity binding even before anything is due.
	copy.Sources = []ReferenceSource{sourcePin(content)}
	inv.References = []Reference{runtime, copy, content}
	third, err := BuildPlan(inv, now)
	if err != nil || third.InventoryHash == first.InventoryHash || third.Hash == first.Hash {
		t.Fatal("lineage missing from inventory integrity", err)
	}
	deadline, err = referenceDeadlines(inv.References, inv.Lifecycle, now)
	if err != nil || !deadline[copy.ID].Equal(now.Add(30*day)) {
		t.Fatal("copied published content became immortal", deadline, err)
	}
	copy.Rule = "platform_tasks.reason"
	copy.Sources = []ReferenceSource{sourcePin(runtime)}
	runtime.CreatedAt = now
	inv.References = []Reference{runtime, copy}
	deadline, err = referenceDeadlines(inv.References, inv.Lifecycle, now)
	if err != nil || !deadline[copy.ID].Equal(now.Add(7*day)) {
		t.Fatal("later source extended a shorter own TTL", deadline, err)
	}
}

func TestDerivedDeadlineLegacyClockLeaseAndExpiredProof(t *testing.T) {
	inv, now := fixtureInventory()
	inv.Objects = nil // This fixture has inline material only, no orphan blob.
	source := fixtureRef("1", "generation_previews.content", "", now.Add(-500*day))
	source.TimeBasis, source.FirstVerifiedAt = LegacyRegistered, now.Add(-30*day)
	source.LeaseUntil = now.Add(time.Minute)
	copy := attachSource(t, fixtureRef("2", "working_chunks.content", "", now.Add(-day)), source)
	inv.References = []Reference{source, copy}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Blocked) != 1 || plan.Blocked[0] != source.ID || len(plan.Expire) != 1 || plan.Expire[0].ReferenceID != copy.ID || !plan.Expire[0].Deadline.Equal(now) {
		t.Fatal("source lease renewed copy or legacy wall time was trusted", plan, err)
	}
	copy.State, copy.ExpiredAt = Expired, now
	inv.References = []Reference{source, copy}
	if plan, err = BuildPlan(inv, now); err != nil || len(plan.Expire) != 0 {
		t.Fatal("valid inherited expiry proof rejected or revived", plan, err)
	}
	copy.ExpiredAt = now.Add(-time.Microsecond)
	inv.References = []Reference{source, copy}
	if _, err = BuildPlan(inv, now); err == nil {
		t.Fatal("premature expired proof accepted")
	}
}

func TestDerivedDeadlineSharesObjectWithIndependentContent(t *testing.T) {
	inv, now := fixtureInventory()
	source := fixtureRef("1", "generation_previews.content", "", now.Add(-30*day))
	copy := attachSource(t, fixtureRef("2", "recipe_steps.output_hash", inv.Objects[0].Hash, now), source)
	content := fixtureRef("3", "resources.content_hash", inv.Objects[0].Hash, now)
	content.ContentRoot = true
	inv.References = []Reference{source, copy, content}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 2 || len(plan.DeleteObjects) != 0 {
		t.Fatal("derived expiry removed separately adopted content", plan, err)
	}
	inv.References = []Reference{source, copy}
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 1 {
		t.Fatal("unreferenced derived blob was kept alive", plan, err)
	}
}

func TestDerivedDeadlineDeepChainAndDeletedProject(t *testing.T) {
	inv, now := fixtureInventory()
	for i := 0; i < 12000; i++ {
		ref := fixtureRef("1", "working_chunks.content", "", now)
		ref.ID = fmt.Sprintf("%064x", i+1)
		if i == 0 {
			ref.CreatedAt = now.Add(-30 * day)
		} else {
			ref.Sources = []ReferenceSource{sourcePin(inv.References[i-1])}
		}
		inv.References = append(inv.References, ref)
	}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 12000 {
		t.Fatal("deep lineage lost its original deadline", len(plan.Expire), err)
	}
	inv.References = inv.References[:2]
	inv.References[0].CreatedAt = now
	inv.Lifecycle, err = DeletedLifecycle(strings.Repeat("a", 32), 1, now.Add(-7*day))
	if err != nil {
		t.Fatal(err)
	}
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 2 || !plan.Expire[1].Deadline.Equal(now) {
		t.Fatal("project deletion did not cap the inherited deadline", plan, err)
	}
}

func TestDerivedDeadlineReindexAndContextCannotRenewOriginal(t *testing.T) {
	inv, now := fixtureInventory()
	source := fixtureRef("1", "generation_previews.content", "", now.Add(-30*day))
	index := attachSource(t, fixtureRef("2", "working_chunks.content", "", now.Add(-time.Hour)), source)
	context := attachSource(t, fixtureRef("3", "context_packages.payload", "", now), index)
	inv.References = []Reference{context, source, index}
	plan, err := BuildPlan(inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Expire) != 3 {
		t.Fatalf("copying renewed original retention: got %d expiries, want 3", len(plan.Expire))
	}
	for _, expiry := range plan.Expire {
		if !expiry.Deadline.Equal(now) {
			t.Fatal("derived expiry did not inherit the earliest source deadline", expiry)
		}
	}
}
