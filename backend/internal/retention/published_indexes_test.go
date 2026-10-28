package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

func publishedIndexFixture(t *testing.T) ([]SourceRow, []byte) {
	t.Helper()
	rows, release := contentRootFixture()
	body := []byte("药师在雨夜等候故人。")
	hash := financialDigest(body)
	release.Artifacts[0].Hash = hash
	release.Artifacts[0].Size = int64(len(body))
	release.Artifacts[0].Origin.ContentHash = hash
	raw, _ := json.Marshal(release)
	rows[10].Values["content_hash"] = hash
	rows[11].Values["manifest"], rows[11].Values["content_hash"], rows[11].Values["adopted_hash"] = string(raw), financialDigest(raw), financialDigest(raw)
	m := domain.KnowledgeIndexManifest{Branch: strings.Repeat("5", 32), IPHead: strings.Repeat("7", 32), Model: retrieval.ModelRevision, Parser: "unicode-512-overlap32-v1", Sources: []domain.KnowledgeSourcePin{{ID: strings.Repeat("8", 32), Name: "背景", Head: strings.Repeat("9", 32), Hash: hash}}}
	raw, _ = json.Marshal(m)
	project := strings.Repeat("2", 32)
	id := financialDigest(append([]byte(project+"\x00"), raw...))
	vector := make([]float32, retrieval.Dimension)
	vector[0] = 1
	packed, err := retrieval.PackVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	rows = append(rows,
		inventoryRow("knowledge_indexes", map[string]string{"id": id, "project_id": project, "branch_id": m.Branch, "ip_head": m.IPHead, "model_revision": m.Model, "manifest": string(raw), "chunks": "1"}),
		inventoryRow("knowledge_chunks", map[string]string{"id": financialDigest([]byte(fmt.Sprintf("%s:%s:0", id, m.Sources[0].ID))), "index_id": id, "resource_id": m.Sources[0].ID, "resource_name": "背景", "resource_revision": m.Sources[0].Head, "source_hash": hash, "start_rune": "0", "end_rune": fmt.Sprint(len([]rune(string(body)))), "content": string(body), "vector": string(packed), "vector_hash": financialDigest(packed)}),
	)
	return rows, body
}

func TestPublishedIndexCertificationRequiresExactPublishedSourcesAndBytes(t *testing.T) {
	for _, mode := range []string{"published", "ready", "missing-object", "wrong-object", "changed-text", "changed-name", "changed-range", "changed-count", "missing-chunk", "changed-vector", "invalid-vector", "wrong-index-id", "wrong-policy", "wrong-source", "unknown-parser"} {
		t.Run(mode, func(t *testing.T) {
			rows, body := publishedIndexFixture(t)
			switch mode {
			case "ready":
				rows[11].Values["status"] = "READY"
				rows[10].Values["head_revision"], rows[10].Values["content_hash"] = "", ""
			case "changed-text":
				rows[13].Values["content"] = "另一段正文"
			case "changed-name":
				rows[13].Values["resource_name"] = "替换的名称"
			case "changed-range":
				rows[13].Values["start_rune"] = "1"
			case "changed-count":
				rows[12].Values["chunks"] = "2"
			case "missing-chunk":
				rows = rows[:13]
			case "changed-vector":
				rows[13].Values["vector_hash"] = strings.Repeat("0", 64)
			case "invalid-vector":
				rows[13].Values["vector"] = strings.Repeat("\x00", retrieval.Dimension*4)
				rows[13].Values["vector_hash"] = financialDigest([]byte(rows[13].Values["vector"]))
			case "wrong-index-id":
				rows[12].Values["id"] = strings.Repeat("f", 64)
				rows[13].Values["index_id"] = rows[12].Values["id"]
			case "wrong-policy", "wrong-source", "unknown-parser":
				var m domain.KnowledgeIndexManifest
				if err := json.Unmarshal([]byte(rows[12].Values["manifest"]), &m); err != nil {
					t.Fatal(err)
				}
				if mode == "wrong-policy" {
					m.Sources[0].Policy = &domain.KnowledgePolicy{}
				}
				if mode == "wrong-source" {
					m.Sources[0].Hash = strings.Repeat("e", 64)
				}
				if mode == "unknown-parser" {
					m.Parser = "future-parser"
				}
				raw, _ := json.Marshal(m)
				rows[12].Values["manifest"] = string(raw)
				rows[12].Values["id"] = financialDigest(append([]byte(strings.Repeat("2", 32)+"\x00"), raw...))
				rows[13].Values["index_id"] = rows[12].Values["id"]
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := index.MaterialReferences(strings.Repeat("2", 32))
			if err != nil {
				t.Fatal(err)
			}
			materials := make([]ObservedMaterial, len(refs))
			for i, ref := range refs {
				materials[i].MaterialReference = ref
			}
			before := stableHash(materials)
			reads := 0
			read := func(ctx context.Context, project, hash string) ([]byte, error) {
				reads++
				if project != strings.Repeat("2", 32) || hash != financialDigest(body) {
					t.Fatal("wrong blob scope")
				}
				if mode == "missing-object" {
					return nil, ErrSourceOwnership
				}
				if mode == "wrong-object" {
					return []byte("坏正文"), nil
				}
				return body, nil
			}
			err = retainPublishedIndexes(context.Background(), index, strings.Repeat("2", 32), materials, read)
			if mode != "published" && mode != "ready" {
				if err == nil || stableHash(materials) != before {
					t.Fatal("invalid proof accepted or leaked roots", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ready" {
				if reads != 0 || stableHash(materials) != before {
					t.Fatal("prospective index promoted or read objects")
				}
				return
			}
			if reads != 1 {
				t.Fatal("unexpected object reads", reads)
			}
			promoted := 0
			for _, m := range materials {
				if m.Table == "knowledge_indexes" || (m.Table == "knowledge_chunks" && m.Field != "resource_name") {
					if !m.ContentRoot || len(m.Sources) != 0 {
						t.Fatal("published content kept temporary provenance", m)
					}
					promoted++
				}
			}
			if promoted != 4 {
				t.Fatal("incomplete preservation", promoted)
			}
		})
	}
}
