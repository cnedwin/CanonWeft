package retention

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRetiredRequestProjectionBindsOriginalAndPhysicalIdentity(t *testing.T) {
	for _, table := range []string{"operations", "model_calls", "source_imports", "ingest_jobs", "generation_tasks", "recipe_runs", "semantic_review_runs"} {
		t.Run(table, func(t *testing.T) {
			project, actor, workspace, source := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
			original := "PRIVATE_REQUEST_CANARY_中文"
			primary := map[string]string{"id": source}
			kind := ""
			if table == "operations" {
				primary, kind = map[string]string{"actor_id": actor, "operation_id": original}, "project.create"
			}
			key, _ := RowOwnerKey(table, primary)
			id, _ := ReferenceID(project, table, key, "operation_id")
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			expiry := Expiry{ReferenceID: id, Rule: table + ".operation_id", PayloadHash: financialDigest([]byte(original)), Revision: 1, Deadline: at, Storage: Inline}
			input := RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor, Table: table, OriginalKey: original, SourceID: source, RequestHash: strings.Repeat("e", 64), Kind: kind, OriginalOwnerKey: key}
			proof, err := ProjectRetiredRequestKey(project, expiry, input, at)
			if err != nil || proof.Materialization != AnonymizedRequestKey {
				t.Fatal("request proof rejected", err)
			}
			raw, _ := json.Marshal(proof)
			if bytes.Contains(raw, []byte("PRIVATE_REQUEST_CANARY")) || bytes.Contains(raw, []byte(original)) {
				t.Fatal("proof retained private original request text")
			}
			if _, err := ParseExpiredProjection(raw, expiry.Rule); err != nil {
				t.Fatal("request proof cannot be read canonically", err)
			}
			anonymous := "retired-v1:" + expiry.PayloadHash
			if table == "operations" {
				primary["operation_id"] = anonymous
			}
			physicalKey, _ := RowOwnerKey(table, primary)
			ref := StoredReference{ID: id, ProjectID: project, Table: table, OwnerKey: key, Field: "operation_id", Storage: Inline, State: Expired, Revision: 2, EvidenceRevision: 2, PayloadHash: expiry.PayloadHash, ProjectionHash: financialDigest(raw), Deadline: at, ExpiredAt: at, EvidenceExpiredAt: at}
			actualIdentity := *proof.Summary.RequestIdentity
			owner := StoredOwner{ProjectID: project, Table: table, Key: physicalKey, Field: "operation_id", Value: []byte(anonymous), RequestIdentity: &actualIdentity}
			if _, err := ValidateStoredProjection(ref, owner, raw); err != nil {
				t.Fatal("original proof failed to bind actual anonymous source", err)
			}
			actualIdentity.RequestHash = strings.Repeat("f", 64)
			if _, err := ValidateStoredProjection(ref, owner, raw); err == nil {
				t.Fatal("different request fingerprint accepted")
			}
			actualIdentity = *proof.Summary.RequestIdentity
			for _, field := range []string{"workspace", "actor", "source", "kind"} {
				actualIdentity = *proof.Summary.RequestIdentity
				switch field {
				case "workspace":
					actualIdentity.WorkspaceID = strings.Repeat("9", 32)
				case "actor":
					actualIdentity.ActorID = strings.Repeat("9", 32)
				case "source":
					actualIdentity.SourceID = strings.Repeat("9", 32)
				case "kind":
					actualIdentity.Kind = "project.delete"
				}
				if _, err := ValidateStoredProjection(ref, owner, raw); err == nil {
					t.Fatal("different actual result identity accepted", field)
				}
			}
			actualIdentity = *proof.Summary.RequestIdentity
			owner.Value = []byte("retired-v1:" + strings.Repeat("f", 64))
			if _, err := ValidateStoredProjection(ref, owner, raw); err == nil {
				t.Fatal("different anonymous key accepted")
			}
			owner.Value, owner.RequestIdentity = []byte(anonymous), nil
			if _, err := ValidateStoredProjection(ref, owner, raw); err == nil {
				t.Fatal("proof accepted without independently resolved result identity")
			}
			input.OriginalKey = "changed original"
			if _, err := ProjectRetiredRequestKey(project, expiry, input, at); err == nil {
				t.Fatal("original key substitution accepted")
			}
			proof.Summary.RequestIdentity.Kind = "PRIVATE_REQUEST_CANARY"
			if ValidateProjectionForRule(proof, expiry.Rule) == nil {
				t.Fatal("free text operation kind admitted to proof")
			}
		})
	}
}

func TestRetiredRequestProjectionCannotMasqueradeAsBodyOrAccounting(t *testing.T) {
	summary := &RequestIdentitySummary{WorkspaceID: strings.Repeat("a", 32), ActorID: strings.Repeat("b", 32), SourceID: strings.Repeat("c", 32), RequestHash: strings.Repeat("d", 64)}
	proof := ExpiredProjection{Schema: 1, State: Expired, Materialization: AnonymizedRequestKey, ReferenceID: strings.Repeat("e", 64), OriginalPayloadHash: strings.Repeat("f", 64), ExpiredAt: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), Summary: EvidenceSummary{RequestIdentity: summary}}
	if ValidateProjectionForRule(proof, "model_calls.operation_id") != nil {
		t.Fatal("valid request proof rejected")
	}
	for _, rule := range []string{"generation_tasks.frozen", "model_calls.state", "branches.name", "model_call_receipts.payload"} {
		if ValidateProjectionForRule(proof, rule) == nil {
			t.Fatal("request proof substituted for other field", rule)
		}
	}
	proof.Summary.ChargedMicros = 1
	if ValidateProjectionForRule(proof, "model_calls.operation_id") == nil {
		t.Fatal("request key proof rewrote accounting")
	}
	proof.Summary.ChargedMicros = 0
	proof.Materialization = InlineEnvelope
	if ValidateProjectionForRule(proof, "model_calls.operation_id") == nil {
		t.Fatal("request key replaced by JSON envelope")
	}
}

func TestRetiredRequestProjectionRequiresOriginalJournal(t *testing.T) {
	project, actor, source := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	key := "PRIVATE_ORIGINAL_REQUEST"
	owner, _ := RowOwnerKey("operations", map[string]string{"actor_id": actor, "operation_id": key})
	id, _ := ReferenceID(project, "operations", owner, "operation_id")
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	deadline, _ := Deadline(RawRun, born, Lifecycle{})
	at := deadline.Add(time.Hour)
	hash := financialDigest([]byte(key))
	prior := ReferenceVersion{Schema: 1, ProjectID: project, Table: "operations", OwnerKey: owner, Field: "operation_id", Class: RawRun, Storage: Inline, SourceID: id, SourcePayloadHash: hash, ValueFirstVerifiedAt: born, LineageState: "PENDING", Deadline: deadline, RecordedAt: born,
		Reference: Reference{ID: id, Rule: "operations.operation_id", PayloadHash: hash, CreatedAt: born, FirstVerifiedAt: born, TimeBasis: LegacyRegistered, Revision: 1, State: Retained}}
	if !validReferenceVersion(prior) {
		t.Fatal("invalid original journal fixture")
	}
	expiry := Expiry{ReferenceID: id, Rule: prior.Reference.Rule, PayloadHash: hash, Revision: 1, Deadline: deadline, Storage: Inline}
	proof, err := ProjectRetiredRequestKey(project, expiry, RequestKeyRetirement{WorkspaceID: strings.Repeat("d", 32), ActorID: actor, Table: "operations", OriginalKey: key, SourceID: source, RequestHash: strings.Repeat("e", 64), Kind: "project.create", OriginalOwnerKey: owner}, at)
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
	if _, _, err := ValidateExpiryJournalRecord(record); err != nil {
		t.Fatal("detached original request journal rejected", err)
	}
	record.Parent = ""
	if _, _, err := ValidateExpiryJournalRecord(record); err == nil {
		t.Fatal("missing original request version accepted")
	}
	// Adding a proof format must not enable the generic text-replacement SQL
	// writer for unique keys; request retirement needs its own lock/proof path.
	if supportedExpiryField("operations", "operation_id") {
		t.Fatal("unsafe generic request-key writer enabled")
	}
}
