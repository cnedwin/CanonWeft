package retrieval

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

type Chunk struct {
	ID         string  `json:"id"`
	ResourceID string  `json:"resource_id"`
	Name       string  `json:"name"`
	Revision   string  `json:"revision"`
	SourceHash string  `json:"source_hash"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
	Semantic   float64 `json:"semantic_score"`
	Lexical    float64 `json:"lexical_score"`
}

// Offsets are Unicode code points, always into the unchanged immutable source.
func Split(text string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("source is not UTF-8")
	}
	runes := []rune(text)
	chunks := []Chunk{}
	for start := 0; start < len(runes); start += 480 {
		end := min(start+512, len(runes))
		part := string(runes[start:end])
		if strings.TrimSpace(part) != "" {
			chunks = append(chunks, Chunk{Start: start, End: end, Text: part})
		}
		if end == len(runes) {
			break
		}
	}
	return chunks, nil
}

func PackVector(vector []float32) ([]byte, error) {
	if !ValidVector(vector) {
		return nil, ErrEmbedding
	}
	data := make([]byte, Dimension*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(data[index*4:], math.Float32bits(value))
	}
	return data, nil
}
func UnpackVector(data []byte) ([]float32, error) {
	if len(data) != Dimension*4 {
		return nil, ErrEmbedding
	}
	vector := make([]float32, Dimension)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(data[index*4:]))
	}
	if !ValidVector(vector) {
		return nil, ErrEmbedding
	}
	return vector, nil
}

// Keep separate top lists so lexical hits cannot disappear before rank fusion.
func Rank(semantic, lexical []Chunk, limit int) []Chunk {
	combined := map[string]Chunk{}
	for _, list := range [][]Chunk{semantic, lexical} {
		for rank, c := range list {
			item, exists := combined[c.ID]
			if !exists {
				item = c
				item.Score = 0
			}
			item.Score += 1 / float64(60+rank+1)
			combined[c.ID] = item
		}
	}
	result := []Chunk{}
	for _, c := range combined {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].ID < result[j].ID
		}
		return result[i].Score > result[j].Score
	})
	return result[:min(limit, len(result))]
}

// Top orders the complete modality pool. Truncating before Rank can discard
// dual-modality hits and their second vote; only fused results may be limited.
func Top(items []Chunk, lexical bool) []Chunk {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].Semantic, items[j].Semantic
		if lexical {
			a, b = items[i].Lexical, items[j].Lexical
		}
		if a == b {
			return items[i].ID < items[j].ID
		}
		return a > b
	})
	return items
}
