package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"novelstudio/internal/retrieval"
	"testing"
)

func TestEmbeddingRejectsWrongRevisionHashAndDimension(t *testing.T) {
	for _, scenario := range []string{"revision", "hash", "dimension", "norm"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				vector := make([]float32, 1024)
				vector[0] = 1
				sum := sha256.Sum256([]byte("查询"))
				output := map[string]any{"model": "BAAI/bge-m3", "revision": retrieval.ModelRevision, "dimension": 1024, "vectors": [][]float32{vector}, "text_hashes": []string{hex.EncodeToString(sum[:])}, "token_counts": []int{4}}
				switch scenario {
				case "revision":
					output["revision"] = "different"
				case "hash":
					output["text_hashes"] = []string{"wrong"}
				case "dimension":
					output["vectors"] = [][]float32{{1}}
				case "norm":
					vector[0] = 10
				}
				json.NewEncoder(w).Encode(output)
			}))
			defer server.Close()
			client, err := retrieval.NewEmbedder(server.URL, "test-token-never-a-provider-secret-123")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Embed(context.Background(), []string{"查询"}); err == nil {
				t.Fatal("invalid embedding response accepted")
			}
		})
	}
}
