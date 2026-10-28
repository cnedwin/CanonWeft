package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDerivedContentExpiryKeepsUnboundedRootProtected(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	source := archiveDeadlineVersion(t, strings.Repeat("b", 32), born, Lifecycle{})
	owner, _ := OwnerKey(strings.Repeat("c", 64))
	id, _ := ReferenceID(source.ProjectID, "knowledge_chunks", owner, "resource_name")
	hash := financialDigest([]byte("source label"))
	prior, err := materialReferenceVersion(source.ProjectID, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "knowledge_chunks", OwnerKey: owner, Field: "resource_name", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: Content}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, Lifecycle{}, born)
	if err != nil || !prior.Deadline.IsZero() {
		t.Fatal("content should have no independent expiry", err)
	}
	prior.LineageState = "PARTIAL"
	prior.Reference.Sources = []ReferenceSource{sourcePin(source.Reference)}
	proof := ExpiredProjection{Schema: 1, Materialization: ClearScalar, State: Expired, ReferenceID: id, OriginalPayloadHash: hash, ExpiredAt: source.Deadline.Add(time.Hour)}
	next, err := expiredReferenceVersion(prior, source.Deadline, proof)
	if err != nil {
		t.Fatal("derived copy cannot inherit source expiry", err)
	}
	if err := ValidateArchivedPins([]ReferenceVersion{source, prior, next}); err != nil {
		t.Fatal("pinned expiry cannot be archived", err)
	}
	wrong, err := expiredReferenceVersion(prior, source.Deadline.Add(-time.Hour), proof)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateArchivedPins([]ReferenceVersion{source, prior, wrong}) == nil {
		t.Fatal("incorrect inherited deadline accepted")
	}
	prior.LineageState = "NONE"
	prior.Reference.Sources = nil
	if _, err := expiredReferenceVersion(prior, source.Deadline, proof); err == nil {
		t.Fatal("unbounded root expired without a source")
	}
}

func TestExpiredReferenceVersionKeepsOriginalAgeAndImmutableParent(t *testing.T) {
	project := strings.Repeat("2", 32)
	owner, _ := OwnerKey(strings.Repeat("a", 32))
	id, _ := ReferenceID(project, "generation_tasks", owner, "frozen")
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	hash := financialDigest([]byte("original"))
	prior, err := materialReferenceVersion(project, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "generation_tasks", OwnerKey: owner, Field: "frozen", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: RawRun}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, Lifecycle{}, born)
	if err != nil {
		t.Fatal(err)
	}
	at := born.Add(31 * day)
	proof := ExpiredProjection{Schema: 1, Materialization: InlineEnvelope, State: Expired, ReferenceID: id, OriginalPayloadHash: hash, ExpiredAt: at}
	next, err := expiredReferenceVersion(prior, prior.Deadline, proof)
	if err != nil {
		t.Fatal("cannot represent an actual expiry transaction", err)
	}
	if next.Reference.Revision != prior.Reference.Revision+1 || next.Reference.State != Expired || next.Reference.PayloadHash != hash || !next.Reference.FirstVerifiedAt.Equal(born) || prior.Reference.State != Retained {
		t.Fatal("expiry altered original identity, age or parent")
	}
	raw, _ := json.Marshal(next)
	if _, err := decodeReferenceVersion(string(raw), financialDigest(raw)); err != nil {
		t.Fatal("new expiry history cannot be read", err)
	}
	parentJSON, _ := json.Marshal(prior)
	proofJSON, _ := json.Marshal(proof)
	evidenceID, _ := EvidenceID(id, next.Reference.Revision)
	journal := ExpiryJournalRecord{Version: string(raw), VersionHash: financialDigest(raw), Parent: string(parentJSON), ParentHash: financialDigest(parentJSON), EvidenceID: evidenceID, Evidence: string(proofJSON), EvidenceHash: financialDigest(proofJSON), OriginalPayloadHash: hash, EvidenceExpiredAt: at.Format("2006-01-02 15:04:05.000000")}
	if _, _, err := ValidateExpiryJournalRecord(journal); err != nil {
		t.Fatal("detached journal cannot verify actual transition", err)
	}
	for name, mutate := range map[string]func(*ExpiryJournalRecord){
		"missing parent":      func(r *ExpiryJournalRecord) { r.Parent = "" },
		"changed parent":      func(r *ExpiryJournalRecord) { r.Parent += " " },
		"wrong version":       func(r *ExpiryJournalRecord) { r.Version = r.Parent; r.VersionHash = r.ParentHash },
		"changed version":     func(r *ExpiryJournalRecord) { r.VersionHash = strings.Repeat("0", 64) },
		"missing proof":       func(r *ExpiryJournalRecord) { r.Evidence = "" },
		"wrong evidence id":   func(r *ExpiryJournalRecord) { r.EvidenceID = strings.Repeat("0", 64) },
		"wrong original hash": func(r *ExpiryJournalRecord) { r.OriginalPayloadHash = strings.Repeat("0", 64) },
		"wrong proof clock":   func(r *ExpiryJournalRecord) { r.EvidenceExpiredAt = born.Format("2006-01-02 15:04:05.000000") },
	} {
		t.Run(name, func(t *testing.T) {
			bad := journal
			mutate(&bad)
			if _, _, err := ValidateExpiryJournalRecord(bad); err == nil {
				t.Fatal("incomplete or altered detached history accepted")
			}
		})
	}
	for _, change := range []func(*ReferenceVersion){
		func(v *ReferenceVersion) { v.Reference.ExpiredAt = born },
		func(v *ReferenceVersion) { v.Reference.Revision++ },
		func(v *ReferenceVersion) { v.Reference.FirstVerifiedAt = born.Add(day) },
		func(v *ReferenceVersion) { v.Reference.ContentRoot = true },
		func(v *ReferenceVersion) { v.Expiration = nil },
	} {
		bad := next
		change(&bad)
		if validReferenceVersion(bad) {
			t.Fatal("invalid expiry history accepted")
		}
	}
	if _, err := expiredReferenceVersion(next, prior.Deadline, proof); err == nil {
		t.Fatal("an expired version was expired twice")
	}
	proof.ExpiredAt = at.Add(time.Nanosecond)
	if _, err := expiredReferenceVersion(prior, prior.Deadline, proof); err == nil {
		t.Fatal("expiry clock cannot survive MySQL microsecond persistence")
	}
}
