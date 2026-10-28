package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

func TestMemoryHistoricalPrivateControlContextRequiresRecompile(t *testing.T) {
	// This is an authority-channel regression, not an embedding-quality test.
	// Keep indexing entirely test-local rather than using a running studio's
	// model worker or reading its private credentials.
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Texts []string `json:"texts"`
		}
		if r.URL.Path != "/embed" || json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid test embedding input", http.StatusBadRequest)
			return
		}
		vectors := make([][]float32, len(input.Texts))
		hashes, tokens := make([]string, len(input.Texts)), make([]int, len(input.Texts))
		for i, text := range input.Texts {
			vectors[i] = make([]float32, retrieval.Dimension)
			vectors[i][0] = 1
			hash := sha256.Sum256([]byte(text))
			hashes[i], tokens[i] = hex.EncodeToString(hash[:]), 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "BAAI/bge-m3", "revision": retrieval.ModelRevision, "dimension": retrieval.Dimension, "vectors": vectors, "text_hashes": hashes, "token_counts": tokens})
	}))
	t.Cleanup(embeddings.Close)
	t.Setenv("STUDIO_EMBEDDING_URL", embeddings.URL)
	t.Setenv("STUDIO_EMBEDDING_TOKEN", "memory-authority-test-only-not-a-provider-key")
	b, db := setup(t)
	project := createProject(b, "候选通道隔离", "memory-authority-project")
	branchID := branch(b, project, "正史")
	source := resource(b, project, branchID, "主题依据")
	release := prepareRelease(b, project, branchID, "", "", "memory-authority-source", []map[string]any{{"resource_id": source, "text": "主人公希望守护亲人。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	src := b.request("GET", "/api/projects/"+project+"/resources/"+source, nil, 200)
	root := "/api/projects/" + project + "/branches/" + branchID
	session := b.request("POST", root+"/memory/sessions", service.MemorySessionInput{OperationID: "memory-authority-session", Document: service.MemorySessionDocument{Title: "未确认主题"}}, 200)
	sessionID := session["id"].(string)
	input := service.MemoryInput{OperationID: "memory-authority-proposal", Item: memory.Item{Session: sessionID, Kind: "theme", Visibility: "author", Control: true, Text: "尚待作者决定的复仇主题", Sources: []memory.Source{{Resource: source, Revision: release, Hash: src["hash"].(string), Start: 0, End: 5}}}}
	b.request("POST", root+"/memory/items", input, 200)
	search := map[string]any{"query": "主题", "purpose": "critic", "limit": 3, "memory": service.MemoryCompileInput{Session: sessionID, Query: "主题"}}
	response := b.request("POST", root+"/knowledge/search", search, 200)
	raw, _ := json.Marshal(response)
	var current service.ContextPackage
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if current.Memory == nil || len(current.Memory.Items) != 1 || current.Memory.Items[0].Channel != "unconfirmed" {
		t.Fatal("new critic context promoted an unadopted proposal")
	}
	contextPath := root + "/knowledge/contexts/" + current.ID
	b.request("GET", contextPath, nil, 200)
	// Reconstruct a valid historical row, including both hashes. A rejection
	// must come from current memory semantics, not a broken envelope checksum.
	oldPack, err := memory.CompileLegacyPrivateControl(current.Memory.Request, []memory.Item{current.Memory.Items[0].Item})
	if err != nil || oldPack.Hash == current.Memory.Hash || oldPack.Items[0].Channel != "control" {
		t.Fatalf("invalid historical fixture: %v", err)
	}
	current.Memory = &oldPack
	raw, _ = json.Marshal(current)
	hash := sha256.Sum256(raw)
	if _, err := db.Exec("UPDATE context_packages SET payload=?,content_hash=? WHERE id=?", raw, hex.EncodeToString(hash[:]), current.ID); err != nil {
		t.Fatal(err)
	}
	b.request("GET", contextPath, nil, 409)
	recompiled := b.request("POST", root+"/knowledge/search", search, 200)
	b.request("GET", root+"/knowledge/contexts/"+recompiled["id"].(string), nil, 200)
	if recompiled["id"] == current.ID {
		t.Fatal("recompilation reused the immutable historical context identity")
	}
}
