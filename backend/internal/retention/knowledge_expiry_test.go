package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeExpiryBindsImmutableManifestAndChunkScope(t *testing.T) {
	for _, field := range []string{"manifest", "content", "vector"} {
		t.Run(field, func(t *testing.T) {
			rows, _ := publishedIndexFixture(t)
			row := rows[len(rows)-1]
			if field == "manifest" {
				row = rows[len(rows)-2]
			}
			summary, err := TransientEvidence(row, field)
			if err != nil {
				t.Fatal("missing typed knowledge handler", err)
			}
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			raw := []byte(row.Values[field])
			proof, err := ProjectExpired(Expiry{ReferenceID: strings.Repeat("a", 64), Rule: row.Table + "." + field, Revision: 1, PayloadHash: financialDigest(raw), Storage: Inline, Deadline: at}, raw, summary, at)
			if err != nil || validateTransientOwner(row, field, proof) != nil {
				t.Fatal("valid knowledge expiry rejected", err)
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "药师") || strings.Contains(string(encoded), "背景") {
				t.Fatal("original text retained")
			}
			for _, col := range TransientColumns(row.Table + "." + field) {
				old := row.Values[col]
				row.Values[col] = old + "x"
				if validateTransientOwner(row, field, proof) == nil {
					t.Fatal("scope changed", col)
				}
				row.Values[col] = old
			}
		})
	}
}
