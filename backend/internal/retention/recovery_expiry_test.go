package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func recoveryExpiryJournal(t *testing.T, parent, next ReferenceVersion, proof ExpiredProjection) ExpiryJournalRecord {
	t.Helper()
	p, _ := json.Marshal(parent)
	v, _ := json.Marshal(next)
	e, _ := json.Marshal(proof)
	id, err := EvidenceID(next.Reference.ID, next.Reference.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return ExpiryJournalRecord{Version: string(v), VersionHash: financialDigest(v), Parent: string(p), ParentHash: financialDigest(p), Evidence: string(e), EvidenceHash: financialDigest(e), EvidenceID: id, OriginalPayloadHash: proof.OriginalPayloadHash, EvidenceExpiredAt: proof.ExpiredAt.Format("2006-01-02 15:04:05.000000")}
}

func TestRecoveryExpiryGraphRebindsExactAncestorsWithoutRevivingContent(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	source := archiveDeadlineVersion(t, strings.Repeat("b", 32), born, Lifecycle{})
	owner, _ := OwnerKey(strings.Repeat("c", 64))
	id, _ := ReferenceID(source.ProjectID, "knowledge_chunks", owner, "resource_name")
	hash := financialDigest([]byte("old source label"))
	prior, err := materialReferenceVersion(source.ProjectID, ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "knowledge_chunks", OwnerKey: owner, Field: "resource_name", PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Inline, Class: Content}, TimeBasis: LegacyRegistered, FirstVerifiedAt: born, ValueFirstVerifiedAt: born}, Lifecycle{}, born)
	if err != nil {
		t.Fatal(err)
	}
	prior.LineageState = "PARTIAL"
	prior.Reference.Sources = []ReferenceSource{sourcePin(source.Reference)}
	proof := ExpiredProjection{Schema: 1, Materialization: ClearScalar, State: Expired, ReferenceID: id, OriginalPayloadHash: hash, ExpiredAt: source.Deadline.Add(time.Hour)}
	next, err := expiredReferenceVersion(prior, source.Deadline, proof)
	if err != nil {
		t.Fatal(err)
	}
	originals := []ReferenceVersion{next, source, prior}
	before := stableHash(originals)
	journal := recoveryExpiryJournal(t, prior, next, proof)
	targetOwner, _ := OwnerKey(strings.Repeat("d", 64))
	mapping := RecoveryExpiryMap{SourceProject: source.ProjectID, TargetProject: strings.Repeat("e", 32), Owners: map[RecoveryOwner]string{{"knowledge_chunks", owner}: targetOwner}}
	result, err := RebindRecoveryExpiryGraph(originals, []ExpiryJournalRecord{journal}, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if stableHash(originals) != before {
		t.Fatal("source evidence mutated")
	}
	var copied, base ReferenceVersion
	for _, v := range result.Versions {
		if v.ProjectID != mapping.TargetProject || v.Reference.ID == id || v.Reference.ID == source.Reference.ID {
			t.Fatal("identity leaked", v)
		}
		if v.Table == "knowledge_chunks" {
			if v.Schema == 2 {
				copied = v
			} else {
				base = v
			}
		}
	}
	if copied.Reference.State != Expired || copied.Reference.PayloadHash != hash || copied.OwnerKey != targetOwner || !copied.Reference.ExpiredAt.Equal(proof.ExpiredAt) || len(copied.Reference.Sources) != 1 || copied.Reference.Sources[0].ReferenceID == source.Reference.ID {
		t.Fatal("expired history changed", copied)
	}
	p := result.Proofs[RecoveryReferenceKey{copied.Reference.ID, copied.Reference.Revision}]
	if _, _, err := ValidateExpiryJournalRecord(recoveryExpiryJournal(t, base, copied, p)); err != nil {
		t.Fatal("rebound proof is unverifiable", err)
	}
	// The recovered metadata can itself be rebound on the next recovery.
	mapping.SourceProject, mapping.TargetProject = mapping.TargetProject, strings.Repeat("f", 32)
	mapping.Owners = nil
	if _, err := RebindRecoveryExpiryGraph(result.Versions, []ExpiryJournalRecord{recoveryExpiryJournal(t, base, copied, p)}, mapping); err != nil {
		t.Fatal("second recovery failed", err)
	}
}

func TestRecoveryExpiryGraphRejectsMissingProofAndIncompleteLineage(t *testing.T) {
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	prior := archiveDeadlineVersion(t, strings.Repeat("b", 32), born, Lifecycle{})
	proof := ExpiredProjection{Schema: 1, Materialization: InlineEnvelope, State: Expired, ReferenceID: prior.Reference.ID, OriginalPayloadHash: prior.Reference.PayloadHash, ExpiredAt: prior.Deadline.Add(time.Hour)}
	next, err := expiredReferenceVersion(prior, prior.Deadline, proof)
	if err != nil {
		t.Fatal(err)
	}
	journal := recoveryExpiryJournal(t, prior, next, proof)
	mapping := RecoveryExpiryMap{SourceProject: prior.ProjectID, TargetProject: strings.Repeat("e", 32)}
	for name, check := range map[string]func() error{
		"missing independent proof": func() error {
			_, err := RebindRecoveryExpiryGraph([]ReferenceVersion{prior, next}, nil, mapping)
			return err
		},
		"missing retained parent": func() error {
			_, err := RebindRecoveryExpiryGraph([]ReferenceVersion{next}, []ExpiryJournalRecord{journal}, mapping)
			return err
		},
		"modified proof": func() error {
			bad := journal
			bad.EvidenceHash = strings.Repeat("0", 64)
			_, err := RebindRecoveryExpiryGraph([]ReferenceVersion{prior, next}, []ExpiryJournalRecord{bad}, mapping)
			return err
		},
		"wrong project": func() error {
			bad := mapping
			bad.SourceProject = strings.Repeat("9", 32)
			_, err := RebindRecoveryExpiryGraph([]ReferenceVersion{prior, next}, []ExpiryJournalRecord{journal}, bad)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if check() == nil {
				t.Fatal("invalid source evidence accepted")
			}
		})
	}
}
