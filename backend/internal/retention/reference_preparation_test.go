package retention

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReferenceVersionRoundTripsLargePinnedSourceSet(t *testing.T) {
	project := strings.Repeat("2", 32)
	owner, _ := OwnerKey(strings.Repeat("a", 64))
	id, _ := ReferenceID(project, "working_chunks", owner, "content")
	hash := financialDigest([]byte("private copy"))
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	v, err := materialReferenceVersion(project, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "working_chunks", OwnerKey: owner, Field: "content", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: RawRun}, TimeBasis: LegacyRegistered, FirstVerifiedAt: now, ValueFirstVerifiedAt: now}, Lifecycle{}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1024; i++ {
		v.Reference.Sources = append(v.Reference.Sources, ReferenceSource{financialDigest([]byte(fmt.Sprintf("parent-%d", i))), 1, strings.Repeat("b", 64)})
	}
	slices.SortFunc(v.Reference.Sources, func(a, b ReferenceSource) int { return strings.Compare(a.ReferenceID, b.ReferenceID) })
	v.LineageState = "PARTIAL"
	raw, _ := json.Marshal(v)
	if len(raw) <= 64<<10 {
		t.Fatal("fixture did not cross the previous decoder limit")
	}
	decoded, err := decodeReferenceVersion(string(raw), financialDigest(raw))
	if err != nil || len(decoded.Reference.Sources) != 1024 {
		t.Fatal("valid source set became unreadable", err)
	}
	if strictJSONWithLimit(raw, &ReferenceVersion{}, 64<<10) == nil {
		t.Fatal("ordinary financial payload limit was widened")
	}
}
