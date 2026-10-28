package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPublicationExpiryBindsPayloadAndNestedObjects(t *testing.T) {
	rows, manifest := contentRootFixture()
	for _, table := range []string{"ip_revisions", "artifact_releases"} {
		t.Run(table, func(t *testing.T) {
			var row SourceRow
			for _, r := range rows {
				if r.Table == table {
					row = r
					break
				}
			}
			row.Values["created_by"] = strings.Repeat("4", 32)
			row.Values["base_head"] = ""
			field := "payload"
			if table == "artifact_releases" {
				field = "manifest"
			}
			summary, err := TransientEvidence(row, field)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			raw := []byte(row.Values[field])
			proof, err := ProjectExpired(Expiry{ReferenceID: strings.Repeat("a", 64), Rule: table + "." + field, Revision: 1, PayloadHash: financialDigest(raw), Storage: Inline, Deadline: at}, raw, summary, at)
			if err != nil || validateTransientOwner(row, field, proof) != nil {
				t.Fatal("valid publication proof", err)
			}
			original := row.Values["content_hash"]
			row.Values["content_hash"] = strings.Repeat("d", 64)
			if validateTransientOwner(row, field, proof) == nil {
				t.Fatal("changed content identity accepted")
			}
			row.Values["content_hash"] = original
			if table == "artifact_releases" {
				field, _ := ArtifactField(manifest.Artifacts[0].ResourceID, false)
				value, err := PublicationArtifactSourceValue(row, field)
				if err != nil || value != manifest.Artifacts[0].Hash {
					t.Fatal("nested object identity", err)
				}
				bad := proof
				s := *proof.Summary.Release
				s.Artifacts = append([]ReleaseExpiryArtifact(nil), s.Artifacts...)
				s.Artifacts[0].Hash = strings.Repeat("e", 64)
				bad.Summary.Release = &s
				if validateTransientOwner(row, "manifest", bad) == nil {
					t.Fatal("forged nested object admitted")
				}
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "manual") || strings.Contains(string(encoded), "entities") {
				t.Fatal("original document leaked into proof")
			}
		})
	}
}
