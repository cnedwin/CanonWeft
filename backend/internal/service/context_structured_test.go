package service

import (
	"encoding/json"
	"testing"
)

func TestPreStructuredContextCanonicalPayloadRemainsStable(t *testing.T) {
	// Persisted contexts are hashed over canonical JSON. Absent new fields must
	// remain absent when an older record is decoded and verified.
	const legacy = `{"id":"context-old","index_id":"index-old","input":{"query":"人物","purpose":"author","perspective":"","story_time":0,"limit":1},"ip_head":"head-old","retrieval_version":"ngram-bge-exact-rrf60-v1","facts":[],"hidden_constraints":[],"entities":[],"rules":[],"evidence":[],"omissions":[]}`
	var p ContextPackage
	if err := json.Unmarshal([]byte(legacy), &p); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil || string(raw) != legacy {
		t.Fatalf("older context hash would change: %v %s", err, raw)
	}
}
