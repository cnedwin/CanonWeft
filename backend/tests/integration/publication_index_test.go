package integration

import (
	"testing"
)

func TestPublicationPreparesExactIndexAndFailureKeepsOldHeads(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "发布与证据同步", "index-publication")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "渡口记录")
	root := "/api/projects/" + pid
	first := prepareRelease(b, pid, bid, "", "", "first-index", []map[string]any{{"resource_id": rid, "expected_head": "", "text": "渡口在黎明前关闭，守卫带走了木牌。"}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	index := b.request("GET", root+"/branches/"+bid+"/knowledge", nil, 200)
	if index["chunks"].(float64) < 1 {
		t.Fatal("published without real index")
	}
	second := prepareRelease(b, pid, bid, "", "", "second-index", []map[string]any{{"resource_id": rid, "expected_head": first, "text": "渡口重新开放，守卫放回木牌。"}})
	releaseAdopt(b, pid, second)
	t.Setenv("STUDIO_EMBEDDING_URL", "")
	b.request("POST", root+"/releases/"+second+"/publish", map[string]any{}, 503)
	current := b.request("GET", root+"/resources/"+rid, nil, 200)
	if current["head"] != first {
		t.Fatal("failed index advanced authority")
	}
	oldIndex := b.request("GET", root+"/branches/"+bid+"/knowledge", nil, 200)
	if oldIndex["id"] != index["id"] {
		t.Fatal("failed publication invalidated old index")
	}
}

func TestIPPublicationReusesVerifiedUnchangedSourceVectors(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "局部索引复用", "reuse-index")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "背景")
	first := prepareRelease(b, pid, bid, "", "", "source-index", []map[string]any{{"resource_id": rid, "expected_head": "", "text": "客栈中只有一位旅人，他正等待天明。"}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	candidate := propose(b, pid, bid, "", "ip-change", originalSnapshot())
	t.Setenv("STUDIO_EMBEDDING_URL", "")
	publish(b, pid, bid, candidate)
	var different int
	if err := db.QueryRow("SELECT COUNT(DISTINCT vector_hash) FROM knowledge_chunks").Scan(&different); err != nil || different != 1 {
		t.Fatal("unchanged source was not reused", different, err)
	}
	next := propose(b, pid, bid, candidate, "ip-next", originalSnapshot())
	root := "/api/projects/" + pid + "/branches/" + bid + "/candidates/" + next
	b.request("POST", root+"/adopt", map[string]any{}, 200)
	if _, err := db.Exec("UPDATE knowledge_chunks SET vector_hash=REPEAT('0',64)"); err != nil {
		t.Fatal(err)
	}
	b.request("POST", root+"/publish", map[string]any{}, 503)
}
