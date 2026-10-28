package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

func TestAuthorAuxiliaryExpiryBindsBytesAndScopeWithoutKeepingText(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		table, hashColumn string
		document          any
	}{
		{"source_imports", "", retrieval.ImportReport{Text: "PRIVATE_CANARY"}},
		{"ip_promotions", "", domain.PromotionInput{Reason: "PRIVATE_CANARY"}},
		{"ip_branch_deltas", "delta_hash", domain.BranchDelta{Schema: 1, Operations: []domain.DeltaOperation{{Reason: "PRIVATE_CANARY"}}}},
		{"change_plans", "content_hash", domain.ChangePlan{ID: id, BranchID: id, CandidateID: id, GraphHash: strings.Repeat("b", 64), SemanticReview: "PRIVATE_CANARY"}},
	} {
		t.Run(tc.table, func(t *testing.T) {
			field := "payload"
			if tc.table == "source_imports" {
				field = "report"
			}
			rule := tc.table + "." + field
			if len(TransientColumns(rule)) == 0 {
				t.Fatal("missing typed expiry contract")
			}
			raw, _ := json.Marshal(tc.document)
			row := SourceRow{Table: tc.table, Values: map[string]string{field: string(raw)}}
			for _, col := range TransientColumns(rule) {
				row.Values[col] = id
			}
			if tc.hashColumn != "" {
				row.Values[tc.hashColumn] = financialDigest(raw)
			}
			summary, err := TransientEvidence(row, field)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			expiry := Expiry{ReferenceID: strings.Repeat("c", 64), Rule: rule, PayloadHash: financialDigest(raw), Revision: 1, Deadline: at, Storage: Inline}
			proof, err := ProjectExpired(expiry, raw, summary, at)
			if err != nil || validateTransientOwner(row, field, proof) != nil {
				t.Fatal("valid expiry rejected", err)
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "PRIVATE_CANARY") {
				t.Fatal("expiry retained author text")
			}
			for _, col := range TransientColumns(rule) {
				old := row.Values[col]
				row.Values[col] = old + "wrong"
				if validateTransientOwner(row, field, proof) == nil {
					t.Fatal("scope substitution accepted", col)
				}
				row.Values[col] = old
			}
			row.Values[field] = strings.Replace(string(raw), "PRIVATE_CANARY", "changed", 1)
			if validateTransientOwner(row, field, proof) == nil {
				t.Fatal("changed original accepted")
			}
		})
	}
}
