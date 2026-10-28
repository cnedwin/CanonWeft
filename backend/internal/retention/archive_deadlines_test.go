package retention

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func archiveDeadlineVersion(t *testing.T, row string, born time.Time, lifecycle Lifecycle) ReferenceVersion {
	t.Helper()
	project := strings.Repeat("a", 32)
	owner, _ := OwnerKey(row)
	id, _ := ReferenceID(project, "generation_tasks", owner, "frozen")
	hash := financialDigest([]byte(row))
	v, err := materialReferenceVersion(project, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "generation_tasks", OwnerKey: owner, Field: "frozen", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: RawRun}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, lifecycle, born)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestArchivedDeadlineUsesLifecycleAtOriginalTime(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	project := strings.Repeat("a", 32)
	for _, legacy := range []bool{false, true} {
		source := archiveDeadlineVersion(t, strings.Repeat("b", 32), born, Lifecycle{})
		child := archiveDeadlineVersion(t, strings.Repeat("c", 32), born.Add(day), Lifecycle{})
		child.LineageState = "PARTIAL"
		child.Reference.Sources = []ReferenceSource{sourcePin(source.Reference)}
		lifecycle, _ := DeletedLifecycle(strings.Repeat("d", 32), 1, born.Add(10*day))
		if legacy {
			lifecycle, _ = LegacyDeletedLifecycle(strings.Repeat("d", 32), 1, born.Add(-day), born.Add(10*day))
		}
		index, err := ResolveArchivedDeadlines([]ReferenceVersion{child, source}, map[string]Lifecycle{project: lifecycle})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ at, want time.Time }{{born.Add(9 * day), source.Deadline}, {lifecycle.DeletedAt, lifecycle.CleanupEligibleAt}, {born.Add(90 * day), lifecycle.CleanupEligibleAt}} {
			got, err := index.At(project, child.Reference.ID, 1, tc.at)
			if err != nil || !got.Equal(tc.want) {
				t.Fatal("historical lifecycle changed original deadline", legacy, got, tc.want, err)
			}
		}
		if _, err := index.At(project, child.Reference.ID, 1, born); err == nil {
			t.Fatal("future version used before recording")
		}
		if _, err := index.At(strings.Repeat("f", 32), child.Reference.ID, 1, born.Add(40*day)); err == nil {
			t.Fatal("other project used deadline")
		}
		if _, err := index.At(project, child.Reference.ID, 2, born.Add(40*day)); err == nil {
			t.Fatal("missing revision substituted")
		}
		// A completed pre-deletion expiry remains bound to the old active rules,
		// even when a later backup also records the project's deletion event.
		oldExpiry := archiveDeadlineExpiry(t, child, source.Deadline, source.Deadline.Add(time.Hour))
		later, _ := DeletedLifecycle(strings.Repeat("d", 32), 1, born.Add(50*day))
		if _, err := ResolveArchivedDeadlines([]ReferenceVersion{source, child, oldExpiry}, map[string]Lifecycle{project: later}); err != nil {
			t.Fatal("later deletion rewrote original expiry", err)
		}
		if _, err := ResolveArchivedDeadlines([]ReferenceVersion{source, child, oldExpiry}, map[string]Lifecycle{project: lifecycle}); err == nil {
			t.Fatal("expiry hid an already-effective deletion lifecycle")
		}
	}
}

func TestArchivedDeadlineMatchesLivePlannerAcrossDeepPinnedHistory(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	versions := make([]ReferenceVersion, 4096)
	for i := range versions {
		v := archiveDeadlineVersion(t, fmt.Sprintf("%032x", i+1), born.Add(time.Duration(i)*time.Microsecond), Lifecycle{})
		if i > 0 {
			v.LineageState = "PARTIAL"
			v.Reference.Sources = []ReferenceSource{sourcePin(versions[i-1].Reference)}
		}
		versions[i] = v
	}
	index, err := ResolveArchivedDeadlines(versions, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := versions[len(versions)-1]
	now := born.Add(40 * day)
	history := make([]Reference, len(versions)-1)
	for i := range history {
		history[i] = versions[i].Reference
	}
	live, err := referenceDeadlinesWithHistory([]Reference{root.Reference}, history, Lifecycle{}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := index.At(root.ProjectID, root.Reference.ID, root.Reference.Revision, now)
	if err != nil || !got.Equal(live[root.Reference.ID]) || !got.Equal(versions[0].Deadline) {
		t.Fatal("archived and live pinned graphs diverged", got, err)
	}
	newHead := versions[0]
	newHead.Reference.Revision = 2
	newHead.Reference.PayloadHash = financialDigest([]byte("changed original source"))
	newHead.SourcePayloadHash = newHead.Reference.PayloadHash
	newHead.RecordedAt = now
	versions = append(versions, newHead)
	index, err = ResolveArchivedDeadlines(versions, nil)
	if err != nil {
		t.Fatal("historical pin replaced by current head", err)
	}
	got, err = index.At(root.ProjectID, root.Reference.ID, 1, now)
	if err != nil || !got.Equal(versions[0].Deadline) {
		t.Fatal("new head renewed original pinned chain", got, err)
	}
}

func archiveDeadlineExpiry(t *testing.T, prior ReferenceVersion, deadline, at time.Time) ReferenceVersion {
	t.Helper()
	proof := ExpiredProjection{Schema: 1, Materialization: InlineEnvelope, State: Expired, ReferenceID: prior.Reference.ID, OriginalPayloadHash: prior.Reference.PayloadHash, ExpiredAt: at}
	v, err := expiredReferenceVersion(prior, deadline, proof)
	if err != nil {
		t.Fatal("invalid expiry fixture", err)
	}
	return v
}

func TestArchivedPinsRejectCoordinatedWrongInheritedExpiry(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	source := archiveDeadlineVersion(t, strings.Repeat("b", 32), born, Lifecycle{})
	child := archiveDeadlineVersion(t, strings.Repeat("c", 32), born.Add(20*day), Lifecycle{})
	child.LineageState = "PARTIAL"
	child.Reference.Sources = []ReferenceSource{sourcePin(source.Reference)}
	good := archiveDeadlineExpiry(t, child, source.Deadline, source.Deadline.Add(time.Hour))
	if err := ValidateArchivedPins([]ReferenceVersion{source, child, good}); err != nil {
		t.Fatal("exact inherited expiry rejected", err)
	}
	for _, deadline := range []time.Time{source.Deadline.Add(-time.Microsecond), source.Deadline.Add(time.Microsecond), child.Deadline} {
		forged := archiveDeadlineExpiry(t, child, deadline, child.Deadline.Add(time.Hour))
		if !validReferenceVersion(forged) {
			t.Fatal("fixture must pass standalone integrity")
		}
		if err := ValidateArchivedPins([]ReferenceVersion{source, child, forged}); err == nil {
			t.Fatal("coordinated rewritten deadline accepted", deadline)
		}
	}
}

func TestArchivedDeadlineAdoptedHeadDoesNotRenewHistoricalCopies(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	project := strings.Repeat("a", 32)
	owner, _ := OwnerKey(strings.Repeat("b", 32))
	id, _ := ReferenceID(project, "resources", owner, "content_hash")
	object := financialDigest([]byte("author source"))
	value := financialDigest([]byte(object))
	source, err := materialReferenceVersion(project, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "resources", OwnerKey: owner, Field: "content_hash", PayloadHash: value, ObjectHash: object, SourceID: id, SourcePayloadHash: value, Storage: Blob, Class: Candidate}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, Lifecycle{}, born)
	if err != nil {
		t.Fatal(err)
	}
	child := archiveDeadlineVersion(t, strings.Repeat("c", 32), born.Add(20*day), Lifecycle{})
	child.LineageState = "PARTIAL"
	child.Reference.Sources = []ReferenceSource{sourcePin(source.Reference)}
	adopted := source
	adopted.Reference.Revision = 2
	adopted.Reference.ContentRoot = true
	adopted.Deadline = time.Time{}
	adopted.RecordedAt = born.Add(21 * day)
	deleted, _ := DeletedLifecycle(strings.Repeat("d", 32), 1, born.Add(25*day))
	index, err := ResolveArchivedDeadlines([]ReferenceVersion{child, adopted, source}, map[string]Lifecycle{project: deleted})
	if err != nil {
		t.Fatal(err)
	}
	got, err := index.At(project, child.Reference.ID, 1, born.Add(24*day))
	if err != nil || !got.Equal(source.Deadline) {
		t.Fatal("adopting newer source extended pinned old copy", got, err)
	}
	got, err = index.At(project, adopted.Reference.ID, 2, born.Add(24*day))
	if err != nil || !got.IsZero() {
		t.Fatal("author root unexpectedly expires while project active", got, err)
	}
	got, err = index.At(project, adopted.Reference.ID, 2, born.Add(26*day))
	if err != nil || !got.Equal(deleted.CleanupEligibleAt) {
		t.Fatal("deleted author root did not retain original recovery window", got, err)
	}
	got, err = index.At(project, child.Reference.ID, 1, born.Add(26*day))
	if err != nil || !got.Equal(source.Deadline) {
		t.Fatal("deletion extended the earlier copy deadline", got, err)
	}
}
