package integration

import (
	"context"
	"math"
	"os"
	"testing"

	"novelstudio/internal/retrieval"
)

func TestRealBGEChineseSemantics(t *testing.T) {
	client, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"剑客进入山洞寻找失落的宝剑", "侠客在洞穴中寻找遗失的兵器", "今天股票市场价格上涨"}
	vectors, err := client.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	related, unrelated := retrieval.Cosine(vectors[0], vectors[1]), retrieval.Cosine(vectors[0], vectors[2])
	if related <= unrelated+0.1 {
		t.Fatalf("related=%f unrelated=%f", related, unrelated)
	}
	for _, vector := range vectors {
		if len(vector) != 1024 || math.Abs(retrieval.Cosine(vector, vector)-1) > 0.001 {
			t.Fatal("invalid normalized vectors")
		}
	}
	t.Logf("real BGE-M3 related %.4f unrelated %.4f", related, unrelated)
}
