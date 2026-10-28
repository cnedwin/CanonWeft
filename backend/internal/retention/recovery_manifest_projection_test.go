package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecoveredManifestProjectionKeepsOriginalObjectEvidence(t *testing.T) {
	project, release := strings.Repeat("a", 32), strings.Repeat("b", 32)
	key, _ := RowOwnerKey("artifact_releases", map[string]string{"id": release})
	id, _ := ReferenceID(project, "artifact_releases", key, "content_hash")
	original, mapped := strings.Repeat("c", 64), strings.Repeat("d", 64)
	at := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	proof := ExpiredProjection{
		Schema: 1, State: Expired, Materialization: BlobReference,
		ReferenceID: id, OriginalPayloadHash: financialDigest([]byte(original)), ExpiredAt: at,
		Summary: EvidenceSummary{ValueHash: original, RecoveryManifestHash: mapped},
	}
	raw, _ := json.Marshal(proof)
	ref := StoredReference{
		ID: id, ProjectID: project, Table: "artifact_releases", OwnerKey: key, Field: "content_hash",
		Storage: Blob, State: Expired, Revision: 2, EvidenceRevision: 2,
		PayloadHash: proof.OriginalPayloadHash, ObjectHash: original, ProjectionHash: financialDigest(raw),
		Deadline: at.Add(-time.Hour), ExpiredAt: at, EvidenceExpiredAt: at,
	}
	owner := StoredOwner{ProjectID: project, Table: ref.Table, Key: key, Field: ref.Field, Value: []byte(mapped)}
	if _, err := ValidateStoredProjection(ref, owner, raw); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"wrong alias", "original object changed", "original digest changed", "missing evidence"} {
		t.Run(mutation, func(t *testing.T) {
			r, o, evidence := ref, owner, raw
			switch mutation {
			case "wrong alias":
				o.Value = []byte(original)
			case "original object changed":
				r.ObjectHash = mapped
			case "original digest changed":
				r.PayloadHash = financialDigest([]byte(mapped))
			case "missing evidence":
				evidence = nil
			}
			if _, err := ValidateStoredProjection(r, o, evidence); err == nil {
				t.Fatal("invalid recovery binding accepted")
			}
		})
	}
	if ValidateProjectionForRule(proof, "source_imports.source_hash") == nil {
		t.Fatal("manifest recovery binding accepted for another object type")
	}
	proof.Summary.RecoveryManifestHash = "not a digest"
	if ValidateProjectionForRule(proof, "artifact_releases.content_hash") == nil {
		t.Fatal("unbounded recovery metadata accepted")
	}
}
