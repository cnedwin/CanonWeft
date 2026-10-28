package retention

import (
	"strings"
	"testing"
	"time"
)

func TestArchivedVersionPinsKeepExactHistoricalSources(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	makeVersion := func(project, row string) ReferenceVersion {
		t.Helper()
		owner, _ := OwnerKey(strings.Repeat(row, 32))
		id, _ := ReferenceID(project, "generation_tasks", owner, "frozen")
		hash := financialDigest([]byte(row))
		v, err := materialReferenceVersion(project, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "generation_tasks", OwnerKey: owner, Field: "frozen", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: RawRun}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, Lifecycle{}, born)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	project := strings.Repeat("a", 32)
	parent, child := makeVersion(project, "b"), makeVersion(project, "c")
	child.LineageState = "PARTIAL"
	child.Reference.Sources = []ReferenceSource{{ReferenceID: parent.Reference.ID, Revision: 1, PayloadHash: parent.Reference.PayloadHash}}
	newer := parent
	newer.Reference.Revision = 2
	newer.Reference.PayloadHash = financialDigest([]byte("newer body"))
	newer.SourcePayloadHash = newer.Reference.PayloadHash
	if err := ValidateArchivedPins([]ReferenceVersion{newer, child, parent}); err != nil {
		t.Fatal("valid pin to original version rejected", err)
	}
	for name, versions := range map[string][]ReferenceVersion{
		"new head cannot replace pin": {newer, child},
		"duplicate version":           {parent, child, parent},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateArchivedPins(versions) == nil {
				t.Fatal("invalid historical graph accepted")
			}
		})
	}
	wrongHash := child
	wrongHash.Reference.Sources = []ReferenceSource{{ReferenceID: parent.Reference.ID, Revision: 1, PayloadHash: newer.Reference.PayloadHash}}
	if ValidateArchivedPins([]ReferenceVersion{parent, wrongHash}) == nil {
		t.Fatal("wrong source hash accepted")
	}
	foreign := makeVersion(strings.Repeat("d", 32), "e")
	cross := child
	cross.Reference.Sources = []ReferenceSource{{ReferenceID: foreign.Reference.ID, Revision: 1, PayloadHash: foreign.Reference.PayloadHash}}
	if ValidateArchivedPins([]ReferenceVersion{foreign, cross}) == nil {
		t.Fatal("cross-project source accepted")
	}
	late := parent
	late.RecordedAt = born.Add(time.Hour)
	if ValidateArchivedPins([]ReferenceVersion{late, child}) == nil {
		t.Fatal("source recorded after consumer accepted")
	}
	cycle := parent
	cycle.LineageState = "PARTIAL"
	cycle.Reference.Sources = []ReferenceSource{{ReferenceID: child.Reference.ID, Revision: 1, PayloadHash: child.Reference.PayloadHash}}
	if ValidateArchivedPins([]ReferenceVersion{cycle, child}) == nil {
		t.Fatal("historical dependency cycle accepted")
	}
}

func TestSourceSlotIdentityUsesOnlyObservedColumns(t *testing.T) {
	project := strings.Repeat("a", 32)
	owner, _ := OwnerKey(strings.Repeat("b", 32))
	for _, item := range [][2]string{{"generation_tasks", "frozen"}, {"model_calls", "accounting"}, {"projects", "name"}} {
		if _, err := SourceSlotID(project, item[0], owner, item[1]); err != nil {
			t.Fatal("observed slot rejected", item, err)
		}
	}
	for _, item := range [][2]string{{"users", "password_hash"}, {"projects", "id"}, {"unknown", "name"}, {"generation_tasks", "frozen.input_hash"}, {"model_calls", ""}} {
		if _, err := SourceSlotID(project, item[0], owner, item[1]); err == nil {
			t.Fatal("non-source column accepted", item)
		}
	}
}
