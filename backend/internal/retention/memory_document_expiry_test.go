package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryExpiryRequiresIndependentProofAndMatchingOriginalHead(t *testing.T) {
	rows := memoryOwnershipFixture(t, "candidate")
	proofs := map[string]ExpiredProjection{}
	for i := range rows {
		row := &rows[i]
		if row.Table != "memory_items" && row.Table != "memory_revisions" {
			continue
		}
		summary, err := TransientEvidence(*row, "document")
		if err != nil {
			t.Fatal(err)
		}
		owner, _ := RowOwnerKey(row.Table, row.Values)
		id, _ := ReferenceID(row.Values["project_id"], row.Table, owner, "document")
		raw := []byte(row.Values["document"])
		proof, err := ProjectExpired(Expiry{ReferenceID: id, Rule: row.Table + ".document", Storage: Inline, Revision: 1, PayloadHash: financialDigest(raw), Deadline: time.Now().Add(-time.Hour)}, raw, summary, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(proof)
		if strings.Contains(string(encoded), "作者摘要") {
			t.Fatal("erased memory retained in proof")
		}
		row.Values["document"] = string(encoded)
		proofs[row.Table] = proof
	}
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, ok := proofs[row.Table]; !ok {
			continue
		}
		if _, err := index.Owner(row.Table, row.Values); err == nil {
			t.Fatal("bare expiry envelope trusted", row.Table)
		}
		if err := index.BindVerifiedMemoryExpiry(row, proofs[row.Table]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range rows {
		if _, ok := proofs[row.Table]; !ok {
			continue
		}
		if _, err := index.Owner(row.Table, row.Values); err != nil {
			t.Fatal("verified pair rejected", row.Table, err)
		}
	}
	// Even two correctly scoped envelopes must prove the same original head.
	for i := range rows {
		if rows[i].Table != "memory_revisions" {
			continue
		}
		proof := proofs[rows[i].Table]
		proof.Summary.InputHash = strings.Repeat("f", 64)
		raw, _ := json.Marshal(proof)
		rows[i].Values["document"] = string(raw)
		proofs[rows[i].Table] = proof
	}
	changed, _ := NewSourceIndex(rows)
	for _, row := range rows {
		if proof, ok := proofs[row.Table]; ok {
			if err := changed.BindVerifiedMemoryExpiry(row, proof); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, row := range rows {
		if row.Table == "memory_items" {
			if _, err := changed.Owner(row.Table, row.Values); err == nil {
				t.Fatal("different original head accepted")
			}
		}
	}
}
