package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/qualification"
	"novelstudio/internal/retrieval"
)

func TestTransientEvidenceBindsExactOwnerWithoutRecoverableText(t *testing.T) {
	for _, tc := range []struct{ table, field, raw string }{
		{"generation_previews", "content", "PRIVATE_CANARY小说预览"},
		{"ingest_jobs", "progress", `{"plan":null,"units":[]}`},
		{"skill_discovery_runs", "inventory", `{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","paths":["PRIVATE_CANARY"],"total":1}`},
		{"skill_discovery_runs", "source_snapshot", `{"id":"1","repository":"PRIVATE_CANARY","ref":"main","path":"skills","keyword":"PRIVATE_CANARY","enabled":true,"limit":1,"revision":1}`},
		{"qualification_steps", "metrics", `{"output":"PRIVATE_CANARY","reason":"PRIVATE_CANARY"}`},
		{"capability_qualifications", "payload", `{}`},
		{"qualification_approvals", "payload", `{"note":"PRIVATE_CANARY","policy":"owner-maintenance-separate-from-authored-skill-v1"}`},
		{"qualification_schedules", "config", `{"input":{},"enabled":false,"budget_limit_micros":101,"revision":0}`},
		{"working_indexes", "manifest", ""},
		{"working_chunks", "content", "PRIVATE_CANARY🌙"},
		{"working_chunks", "vector", ""},
		{"ip_drafts", "payload", `{"unfinished":"PRIVATE_CANARY"}`},
		{"patch_drafts", "payload", `{"unfinished":"PRIVATE_CANARY"}`},
		{"import_drafts", "payload", `{"unfinished":"PRIVATE_CANARY"}`},
		{"resource_drafts", "body", "PRIVATE_CANARY🌙"},
		{"resource_policy_drafts", "payload", `{"unfinished":"PRIVATE_CANARY"}`},
		{"memory_sessions", "document", `{"title":"PRIVATE_CANARY","goal":"","notes":"PRIVATE_CANARY","focus_ids":[]}`},
	} {
		t.Run(tc.table, func(t *testing.T) {
			// Use the same complete wire contracts written by the real services.
			var typed any
			switch tc.table {
			case "qualification_steps":
				typed = qualification.EvaluationMetrics{Output: "PRIVATE_CANARY", Reason: "PRIVATE_CANARY"}
			case "capability_qualifications":
				typed = qualification.QualificationFrozen{}
			case "qualification_approvals":
				typed = qualification.ExecutableApproval{Note: "PRIVATE_CANARY", Policy: qualification.ExecutableApprovalPolicy}
			case "qualification_schedules":
				typed = struct {
					Input    qualification.QualificationInput `json:"input"`
					Enabled  bool                             `json:"enabled"`
					Budget   int64                            `json:"budget_limit_micros"`
					Revision int64                            `json:"revision"`
				}{Budget: 101}
			}
			if typed != nil {
				raw, _ := json.Marshal(typed)
				tc.raw = string(raw)
			}
			key := tc.table + "." + tc.field
			row := SourceRow{Table: tc.table, Values: map[string]string{tc.field: tc.raw, "state": "QUEUED"}}
			for _, column := range TransientColumns(key) {
				row.Values[column] = "1"
			}
			row.Values["id"], row.Values["generation_id"] = strings.Repeat("a", 32), strings.Repeat("a", 32)
			row.Values["content_hash"], row.Values["progress_hash"] = financialDigest([]byte(tc.raw)), financialDigest([]byte(tc.raw))
			switch key {
			case "working_indexes.manifest":
				manifest := domain.PrivateManifest{Project: "1", Branch: "1", Actor: "1", Model: "1", Parser: "PRIVATE_CANARY"}
				raw, _ := json.Marshal(manifest)
				tc.raw = string(raw)
				row.Values["id"] = stableHash(manifest)
			case "working_chunks.content":
				row.Values["start_rune"], row.Values["end_rune"] = "0", "15"
			case "working_chunks.vector":
				vector := make([]float32, retrieval.Dimension)
				vector[0] = 1
				raw, err := retrieval.PackVector(vector)
				if err != nil {
					t.Fatal(err)
				}
				tc.raw = string(raw)
				row.Values["vector_hash"] = financialDigest(raw)
			}
			row.Values[tc.field] = tc.raw
			summary, err := TransientEvidence(row, tc.field)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			expiry := Expiry{Rule: key, Storage: Inline, ReferenceID: strings.Repeat("b", 64), PayloadHash: financialDigest([]byte(tc.raw)), Revision: 1, Deadline: now.Add(-time.Hour)}
			proof, err := ProjectExpired(expiry, []byte(tc.raw), summary, now)
			if err != nil || validateTransientOwner(row, tc.field, proof) != nil {
				t.Fatal("valid owner rejected", err)
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "PRIVATE_CANARY") {
				t.Fatal("erased text retained in evidence")
			}
			for _, column := range TransientColumns(key) {
				old := row.Values[column]
				row.Values[column] = "changed"
				if validateTransientOwner(row, tc.field, proof) == nil {
					t.Fatal("changed owner accepted", column)
				}
				delete(row.Values, column)
				if _, err := TransientIdentity(key, row.Values); err == nil {
					t.Fatal("missing identity accepted", column)
				}
				row.Values[column] = old
			}
			for _, bad := range []EvidenceSummary{{}, {ContentScopeHash: summary.ContentScopeHash, State: "PRIVATE_CANARY"}} {
				if _, err := ProjectExpired(expiry, []byte(tc.raw), bad, now); err == nil {
					t.Fatal("open or missing summary accepted")
				}
			}
			{
				original := proof.OriginalPayloadHash
				proof.OriginalPayloadHash = strings.Repeat("c", 64)
				if validateTransientOwner(row, tc.field, proof) == nil {
					t.Fatal("different original text accepted")
				}
				proof.OriginalPayloadHash = original
			}
			if tc.table == "qualification_schedules" {
				if proof.Summary.Schedule == nil || proof.Summary.Schedule.BudgetLimit != 101 {
					t.Fatal("budget ceiling lost")
				}
				proof.Summary.Schedule.BudgetLimit = 102
				if validateTransientOwner(row, tc.field, proof) == nil {
					t.Fatal("forged budget ceiling accepted")
				}
				proof.Summary.Schedule.BudgetLimit = -1
				if ValidateProjectionForRule(proof, key) == nil {
					t.Fatal("invalid budget ceiling accepted")
				}
			}
		})
	}
}
