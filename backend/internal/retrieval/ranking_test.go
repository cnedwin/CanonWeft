package retrieval

import (
	"fmt"
	"math"
	"testing"
)

// A document ranked 65th in both modalities outranks each single-modality
// document ranked third or later. Truncating either input at 64 loses it.
func TestHybridRankKeepsEvidenceAcrossPageBoundaries(t *testing.T) {
	var semantic, lexical []Chunk
	for i := 0; i < 64; i++ {
		semantic = append(semantic, Chunk{ID: fmt.Sprintf("s%03d", i), Semantic: 1 - float64(i)/1000})
		lexical = append(lexical, Chunk{ID: fmt.Sprintf("l%03d", i), Lexical: 1000 - float64(i)})
	}
	// Mimic the published-index pagination boundary before the shared hit.
	semantic = Top(semantic, false)
	lexical = Top(lexical, true)
	shared := Chunk{ID: "shared", Semantic: .8, Lexical: 800}
	semantic = append(semantic, shared)
	lexical = append(lexical, shared)
	got := Rank(Top(semantic, false), Top(lexical, true), 6)
	if len(got) != 6 || got[4].ID != "shared" {
		t.Fatalf("shared evidence must rank fifth after fusion: %+v", got)
	}
	if math.Abs(got[4].Score-2.0/125) > 1e-12 {
		t.Fatalf("both modality contributions required: %v", got[4].Score)
	}
}

func TestHybridRankStableOrderAndLateSecondVote(t *testing.T) {
	var semantic, lexical []Chunk
	semantic = append(semantic, Chunk{ID: "dual", Semantic: 1})
	for i := 0; i < 130; i++ {
		lexical = append(lexical, Chunk{ID: fmt.Sprintf("l%03d", i), Lexical: float64(200 - i)})
	}
	lexical = append(lexical, Chunk{ID: "dual", Lexical: 1})
	got := Rank(Top(semantic, false), Top(lexical, true), 3)
	if got[0].ID != "dual" || math.Abs(got[0].Score-(1.0/61+1.0/191)) > 1e-12 {
		t.Fatalf("late vote must be included: %+v", got)
	}
	tied := Top([]Chunk{{ID: "z", Semantic: .9}, {ID: "a", Semantic: .9}}, false)
	if tied[0].ID != "a" {
		t.Fatal("equal-score ranking must use stable identity ordering")
	}
}
