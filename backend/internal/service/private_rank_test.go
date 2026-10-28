package service

import (
	"fmt"
	"math"
	"novelstudio/internal/retrieval"
	"testing"
)

func TestPrivateFusionRetainsDualMatchAtRank65(t *testing.T) {
	semantic, lexical := []retrieval.Chunk{}, []retrieval.Chunk{}
	// Disjoint leaders: only the shared tail receives two modality votes.
	// Insert them unsorted, just as SQL ID pagination may return score order.
	for i := 64; i >= 1; i-- {
		semantic = append(semantic, retrieval.Chunk{ID: fmt.Sprintf("semantic-%02d", i), Semantic: 1 - float64(i)/1000})
		lexical = append(lexical, retrieval.Chunk{ID: fmt.Sprintf("lexical-%02d", i), Lexical: 100 - float64(i)})
	}
	dual := retrieval.Chunk{ID: "dual-65", Semantic: 0.6, Lexical: 1}
	semantic = append(semantic, dual)
	lexical = append(lexical, dual)
	hits := rankPrivateChunks(semantic, lexical, 6)
	if len(hits) != 6 || hits[4].ID != dual.ID {
		t.Fatalf("rank65 in both modalities must be fifth in UI top6; got %+v", hits)
	}
	if math.Abs(hits[4].Score-2.0/125) > 1e-12 {
		t.Fatalf("dual match lost a modality vote: %.12f", hits[4].Score)
	}
	if hits[5].Score >= hits[4].Score {
		t.Fatal("one-sided third place must rank below the dual match")
	}
}

func TestPrivateFusionKeepsTailVoteForRetainedCandidate(t *testing.T) {
	dual := retrieval.Chunk{ID: "dual-head-tail", Semantic: 0.99, Lexical: 1}
	lexical := []retrieval.Chunk{}
	for i := 1; i <= 64; i++ {
		lexical = append(lexical, retrieval.Chunk{ID: fmt.Sprintf("lexical-%02d", i), Lexical: 100 - float64(i)})
	}
	lexical = append(lexical, dual)
	hits := rankPrivateChunks([]retrieval.Chunk{dual}, lexical, 1)
	want := 1.0/61 + 1.0/125
	if len(hits) != 1 || hits[0].ID != dual.ID || math.Abs(hits[0].Score-want) > 1e-12 {
		t.Fatalf("head candidate must keep its rank65 second vote; want %.12f got %+v", want, hits)
	}
}

func TestPrivateFusionTiesHaveStableRanks(t *testing.T) {
	semantic := []retrieval.Chunk{{ID: "b", Semantic: 0.8}, {ID: "a", Semantic: 0.8}}
	lexical := []retrieval.Chunk{{ID: "b", Lexical: 2}, {ID: "a", Lexical: 2}}
	hits := rankPrivateChunks(semantic, lexical, 2)
	if len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "b" || math.Abs(hits[0].Score-2.0/61) > 1e-12 || math.Abs(hits[1].Score-2.0/62) > 1e-12 {
		t.Fatalf("equal modality scores must break ties by stable chunk ID: %+v", hits)
	}
}
