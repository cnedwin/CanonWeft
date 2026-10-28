package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func requestAliasSourceFixture(t *testing.T) ([]SourceRow, ReferenceVersion, ExpiryJournalRecord, RequestAliasRecord) {
	t.Helper()
	rows := sourceOwnershipRows()[:6]
	project, actor, workspace, source := strings.Repeat("2", 32), strings.Repeat("4", 32), strings.Repeat("1", 32), strings.Repeat("5", 32)
	key := "PRIVATE_REQUEST_BEFORE_ANONYMIZATION"
	owner, _ := RowOwnerKey("operations", map[string]string{"actor_id": actor, "operation_id": key})
	id, _ := ReferenceID(project, "operations", owner, "operation_id")
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	deadline, _ := Deadline(RawRun, born, Lifecycle{})
	at := deadline.Add(time.Hour)
	hash := financialDigest([]byte(key))
	prior := ReferenceVersion{Schema: 1, ProjectID: project, Table: "operations", OwnerKey: owner, Field: "operation_id", Class: RawRun, Storage: Inline, SourceID: id, SourcePayloadHash: hash, ValueFirstVerifiedAt: born, LineageState: "PENDING", Deadline: deadline, RecordedAt: born,
		Reference: Reference{ID: id, Rule: "operations.operation_id", PayloadHash: hash, CreatedAt: born, FirstVerifiedAt: born, TimeBasis: LegacyRegistered, Revision: 1, State: Retained}}
	input := RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor, Table: "operations", OriginalKey: key, SourceID: source, RequestHash: strings.Repeat("e", 64), Kind: "branch.create", OriginalOwnerKey: owner}
	proof, err := ProjectRetiredRequestKey(project, Expiry{ReferenceID: id, Rule: prior.Reference.Rule, PayloadHash: hash, Revision: 1, Deadline: deadline, Storage: Inline}, input, at)
	if err != nil {
		t.Fatal(err)
	}
	next, err := expiredReferenceVersion(prior, deadline, proof)
	if err != nil {
		t.Fatal(err)
	}
	parentRaw, _ := json.Marshal(prior)
	versionRaw, _ := json.Marshal(next)
	proofRaw, _ := json.Marshal(proof)
	evidenceID, _ := EvidenceID(id, 2)
	record := ExpiryJournalRecord{Version: string(versionRaw), VersionHash: financialDigest(versionRaw), Parent: string(parentRaw), ParentHash: financialDigest(parentRaw), EvidenceID: evidenceID, Evidence: string(proofRaw), EvidenceHash: financialDigest(proofRaw), OriginalPayloadHash: hash, EvidenceExpiredAt: at.Format("2006-01-02 15:04:05.000000")}
	alias := RequestAliasRecord{Table: "operations", OriginalKeyHash: hash, OriginalOwnerKey: owner, AnonymousKey: "retired-v1:" + hash, Identity: *proof.Summary.RequestIdentity, PolicyWorkspaceID: workspace, PolicyClosed: true, PolicyRevision: 2, PolicyCreatedAt: born, PolicyClosedAt: at, CreatedAt: at}
	rows = append(rows, inventoryRow("operations", map[string]string{"actor_id": actor, "operation_id": alias.AnonymousKey, "kind": input.Kind, "result_id": source, "request_hash": input.RequestHash}))
	return rows, next, record, alias
}

func TestArchivedRequestAliasSuppressesAnonymousSourceAndKeepsOldIdentity(t *testing.T) {
	rows, v, journal, alias := requestAliasSourceFixture(t)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := index.ArchivedMaterials(v.ProjectID, map[string]ReferenceVersion{v.Reference.ID: v}, map[string]ExpiryJournalRecord{v.Reference.ID: journal}, alias)
	if err != nil {
		t.Fatal("actual anonymous owner did not bind original journal", err)
	}
	for _, m := range materials {
		if m.Table == "operations" && m.Field == "operation_id" {
			t.Fatal("anonymous identifier registered as new private material")
		}
	}
	observed, err := index.SourceObservations(v.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range observed {
		if o.Table == "operations" && o.Field == "operation_id" {
			t.Fatal("anonymous identifier renewed source clock")
		}
	}
	if index.expired[v.Reference.ID].OwnerKey != v.OwnerKey {
		t.Fatal("original identity lost")
	}
	for _, missing := range []string{"alias", "proof", "version"} {
		bad, _ := NewSourceIndex(rows)
		versions, proofs, aliases := map[string]ReferenceVersion{v.Reference.ID: v}, map[string]ExpiryJournalRecord{v.Reference.ID: journal}, []RequestAliasRecord{alias}
		switch missing {
		case "alias":
			aliases = nil
		case "proof":
			proofs = nil
		case "version":
			versions = nil
		}
		if _, err := bad.ArchivedMaterials(v.ProjectID, versions, proofs, aliases...); err == nil {
			t.Fatal("unproved anonymous source accepted", missing)
		}
	}
}

func TestArchivedRequestAliasRejectsPolicyOrActualResultSubstitution(t *testing.T) {
	for _, change := range []string{"policy", "workspace", "original owner", "source", "actual result", "actual hash", "clock"} {
		rows, v, journal, alias := requestAliasSourceFixture(t)
		switch change {
		case "policy":
			alias.PolicyClosed = false
		case "workspace":
			alias.PolicyWorkspaceID = strings.Repeat("9", 32)
		case "original owner":
			alias.OriginalOwnerKey, _ = OwnerKey(strings.Repeat("9", 32))
		case "source":
			alias.Identity.SourceID = strings.Repeat("9", 32)
		case "actual result":
			rows[len(rows)-1].Values["result_id"] = strings.Repeat("6", 32)
		case "actual hash":
			rows[len(rows)-1].Values["request_hash"] = strings.Repeat("9", 64)
		case "clock":
			alias.PolicyClosedAt = alias.PolicyCreatedAt.Add(-time.Hour)
		}
		index, _ := NewSourceIndex(rows)
		if _, err := index.ArchivedMaterials(v.ProjectID, map[string]ReferenceVersion{v.Reference.ID: v}, map[string]ExpiryJournalRecord{v.Reference.ID: journal}, alias); err == nil {
			t.Fatal("retired identity substitution accepted", change)
		}
	}
}
