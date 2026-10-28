package retention

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

func TestPrivateTextProofPreservesUnicodeWhitespaceAndAllParts(t *testing.T) {
	texts := []string{"", strings.Repeat(" \n", 600), strings.Repeat(" ", 960) + strings.Repeat("夜🌙", 500), strings.Repeat("细节🌙", 700)}
	vector := make([]float32, retrieval.Dimension)
	vector[0] = 1
	raw, err := retrieval.PackVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	for n, text := range texts {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			proof, err := textCopyEvidence(text)
			if err != nil {
				t.Fatal(err)
			}
			pin := domain.PrivatePin{Ref: domain.WorkingRef{Kind: "resource_draft", ID: strings.Repeat("a", 32), Version: "1"}, State: "PRIVATE_DRAFT", Name: "原文", Format: "text", Hash: financialDigest([]byte(text))}
			key, indexID := stableHash(pin), strings.Repeat("b", 64)
			chunks, _ := retrieval.Split(text)
			rows := []SourceRow{}
			for _, chunk := range chunks {
				rows = append(rows, SourceRow{Table: "working_chunks", Values: map[string]string{
					"id": financialDigest([]byte(fmt.Sprintf("%s:%s:%d", indexID, key, chunk.Start))), "index_id": indexID,
					"source_key": key, "source_name": pin.Name, "source_hash": pin.Hash,
					"ref_kind": pin.Ref.Kind, "ref_id": pin.Ref.ID, "ref_version": pin.Ref.Version, "part_number": "0",
					"start_rune": strconv.Itoa(chunk.Start), "end_rune": strconv.Itoa(chunk.End), "content": chunk.Text,
					"vector": string(raw), "vector_hash": financialDigest(raw),
				}})
			}
			slices.Reverse(rows) // SQL row order is irrelevant; source ranges are not.
			if err := verifyPrivatePart(indexID, key, pin, rows, proof); err != nil {
				t.Fatal(err)
			}
			v := ReferenceVersion{Table: "resource_drafts", Field: "body", DraftVersion: &DraftVersionEvidence{Version: "1", ProjectionHash: pin.Hash}, TextProof: proof}
			if !validTextCopyEvidence(v) {
				t.Fatal("valid split rejected")
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "细节") || strings.Contains(string(encoded), "🌙") {
				t.Fatal("proof copied source text")
			}
			if len(rows) > 0 && verifyPrivatePart(indexID, key, pin, rows[1:], proof) == nil {
				t.Fatal("missing segment accepted")
			}
			if len(rows) > 0 {
				// Partial cleanup must preserve split integrity without retaining
				// the erased text. Only independently verified digests may replace it.
				erased := map[string]ExpiredProjection{}
				row := rows[0].Values
				for _, field := range []string{"content", "vector", "source_name"} {
					erased[row["id"]+"."+field] = ExpiredProjection{OriginalPayloadHash: financialDigest([]byte(row[field]))}
					row[field] = ""
				}
				if verifyPrivatePart(indexID, key, pin, rows, proof) == nil {
					t.Fatal("erased parts accepted without independent evidence")
				}
				if err := verifyPrivatePart(indexID, key, pin, rows, proof, erased); err != nil {
					t.Fatal("valid partial cleanup rejected", err)
				}
				for field, original := range erased {
					erased[field] = ExpiredProjection{OriginalPayloadHash: strings.Repeat("f", 64)}
					if verifyPrivatePart(indexID, key, pin, rows, proof, erased) == nil {
						t.Fatal("changed erased segment digest accepted", field)
					}
					erased[field] = original
				}
			}
			changed := *proof
			changed.SourceHash = strings.Repeat("f", 64)
			v.TextProof = &changed
			if validTextCopyEvidence(v) {
				t.Fatal("proof for different source accepted")
			}
		})
	}
	if _, err := textCopyEvidence(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF8 silently normalized")
	}
}
