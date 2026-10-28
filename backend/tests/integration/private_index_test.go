package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"strings"
	"testing"
)

func TestPrivateWorkingSemanticIndexPinsAndInvalidates(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "私有语义索引", "private-index")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "同名资料")
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	draft := "/api/projects/" + pid + "/resources/" + rid + "/draft"
	text := "少女为治好母亲的顽疾，独自踏上采集灵药的旅途。🌙"
	b.request("PUT", draft, map[string]any{"revision": 0, "base_head": "", "text": text}, 200)
	b.request("POST", path+"/index", map[string]any{}, 200)
	job := b.request("POST", path+"/jobs", map[string]any{"operation_id": "private-build", "kind": "working"}, 202)
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &service.IndexJobs{Knowledge: &service.Knowledge{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Embedder: embedder}}
	if state, err := jobs.Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	query := map[string]any{"query": "女儿寻求救治家人的办法", "purpose": "author", "limit": 6, "include_working": true, "include_private_working": true}
	result := b.request("POST", path+"/search", query, 200)
	hits, ok := result["private_hits"].([]any)
	if !ok || len(hits) == 0 {
		t.Fatal("semantic-only private evidence missing", result)
	}
	hit := hits[0].(map[string]any)
	if hit["text"] != text || hit["lexical_score"].(float64) != 0 || hit["semantic_score"].(float64) < 0.45 {
		t.Fatal("not an actual semantic-only match", hit)
	}
	if len(result["evidence"].([]any)) != 0 || len(result["facts"].([]any)) != 0 {
		t.Fatal("private material entered canon", result)
	}
	anchor := path + "/contexts/" + result["id"].(string) + "/private-sources/" + hit["id"].(string)
	if part := b.request("GET", anchor, nil, 200); part["text"] != text {
		t.Fatal("unicode anchor differs", part)
	}
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 200)
	query["working_refs"] = []any{workingChoice(t, b, path, "resource_draft", rid)}
	full := b.request("POST", path+"/search", query, 200)
	if full["unconfirmed_inputs"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != text {
		t.Fatal("explicit input truncated")
	}
	b.request("PUT", draft, map[string]any{"revision": 1, "base_head": "", "text": "她放弃旅行，留在故乡。"}, 200)
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 409)
	b.request("GET", anchor, nil, 409)
	delete(query, "working_refs")
	b.request("POST", path+"/search", query, 503)
	query["purpose"] = "writer"
	b.request("POST", path+"/search", query, 400)
}

func privateJobs(t *testing.T, db *sql.DB) *service.IndexJobs {
	t.Helper()
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	return &service.IndexJobs{Knowledge: &service.Knowledge{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Embedder: embedder}}
}

func TestPrivateWorkingActorProjectIsolationAndRevocation(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	jobs := privateJobs(t, db)
	pid := createProject(b, "作品甲", "private-isolation-a")
	bid := branch(b, pid, "正史")
	other := createProject(b, "作品乙", "private-isolation-b")
	otherBranch := branch(b, other, "另一作品正史")
	root := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	secondRoot := "/api/projects/" + other + "/branches/" + otherBranch + "/knowledge"
	member := b.request("POST", "/api/members", map[string]any{"username": "private-editor", "password": "private-editor-password", "role": "editor"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member, map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "private-editor", "password": "private-editor-password"}, 200)
	r := resource(b, pid, bid, "同名资料")
	r2 := b.request("POST", "/api/projects/"+other+"/resources", map[string]any{"branch_id": otherBranch, "name": "同名资料", "kind": "note", "operation_id": "other-private-resource"}, 201)["id"].(string)
	for _, v := range []struct {
		client            *browser
		project, id, text string
	}{{b, pid, r, "主人私人计划：红色钥匙打开宝库。"}, {editor, pid, r, "编辑私人计划：蓝色钥匙打开宝库。"}, {b, other, r2, "另一作品私人计划：绿色钥匙打开宝库。"}} {
		v.client.request("PUT", "/api/projects/"+v.project+"/resources/"+v.id+"/draft", map[string]any{"revision": 0, "base_head": "", "text": v.text}, 200)
	}
	b.request("POST", root+"/index", map[string]any{}, 200)
	job := editor.request("POST", root+"/jobs", map[string]any{"kind": "working", "operation_id": "editor-private"}, 202)
	id := job["id"].(string)
	if state, err := jobs.Execute(ctx, id); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	b.request("GET", root+"/jobs/"+id, nil, 403)
	b.request("POST", root+"/jobs/"+id+"/cancel", map[string]any{}, 403)
	if len(b.request("GET", root+"/jobs", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("owner can list another actor private job")
	}
	editor.request("GET", secondRoot+"/private-index", nil, 403)
	q := map[string]any{"query": "钥匙打开宝库", "purpose": "author", "limit": 10, "include_working": true, "include_private_working": true}
	saved := editor.request("POST", root+"/search", q, 200)
	for _, raw := range saved["private_hits"].([]any) {
		if raw.(map[string]any)["text"] != "编辑私人计划：蓝色钥匙打开宝库。" {
			t.Fatal("actor/project leak", raw)
		}
	}
	b.request("POST", root+"/search", q, 503)
	b.request("DELETE", "/api/projects/"+pid+"/members/"+member, nil, 200)
	editor.request("GET", root+"/contexts/"+saved["id"].(string), nil, 403)
	editor.request("GET", root+"/jobs/"+id, nil, 403)
	editor.request("GET", root+"/private-index", nil, 403)
}

func TestPrivateWorkingCandidateWatermarksOmissionsAndRecovery(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	jobs := privateJobs(t, db)
	pid := createProject(b, "候选工作区", "private-candidates")
	bid := branch(b, pid, "正史")
	root := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	branchRoot := "/api/projects/" + pid + "/branches/" + bid
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	candidate := propose(b, pid, bid, "", "private-ip", originalSnapshot())
	r := resource(b, pid, bid, "未确认材料")
	release := prepareRelease(b, pid, bid, "", "", "private-release", []map[string]any{{"resource_id": r, "text": "少女为治好母亲的顽疾，独自踏上采集灵药的旅途。"}})
	b.request("POST", root+"/index", map[string]any{}, 200)
	create := func(op string) service.IndexJob {
		t.Helper()
		j, e := jobs.Create(ctx, actor, pid, bid, service.IndexJobInput{OperationID: op, Kind: "working"})
		if e != nil {
			t.Fatal(e)
		}
		return j
	}
	j := create("private-recovery")
	run, cancel := context.WithCancel(ctx)
	jobs.Heartbeat = func(context.Context) { cancel() }
	if state, err := jobs.Execute(run, j.ID); state != "RUNNING" || !errors.Is(err, context.Canceled) {
		t.Fatal("interruption not recoverable", state, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM working_indexes WHERE id=?", j.IndexID).Scan(&count); err != nil || count != 0 {
		t.Fatal("interrupted index visible", count, err)
	}
	jobs.Heartbeat = nil
	if state, err := jobs.Execute(ctx, j.ID); err != nil || state != "READY" {
		t.Fatal("retry failed", state, err)
	}
	q := map[string]any{"query": "女儿寻求救治家人的办法", "purpose": "critic", "limit": 6, "include_working": true, "include_private_working": true}
	saved := b.request("POST", root+"/search", q, 200)
	b.request("POST", branchRoot+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	b.request("GET", root+"/contexts/"+saved["id"].(string), nil, 409)
	j = create("private-ready-state")
	if state, err := jobs.Execute(ctx, j.ID); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	saved = b.request("POST", root+"/search", q, 200)
	releaseAdopt(b, pid, release)
	b.request("GET", root+"/contexts/"+saved["id"].(string), nil, 409)
	// Cancellation is checked between inference batches and before atomic visibility.
	j = create("private-cancel")
	jobs.Heartbeat = func(context.Context) {
		if _, err := jobs.Cancel(ctx, actor, pid, bid, j.ID); err != nil {
			t.Error(err)
		}
	}
	if state, err := jobs.Execute(ctx, j.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	jobs.Heartbeat = nil
	if err := db.QueryRow("SELECT COUNT(*) FROM working_indexes WHERE id=?", j.IndexID).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled index visible", count, err)
	}
	// Publishing advances resource bases. The stale draft is explicitly omitted.
	b.request("PUT", "/api/projects/"+pid+"/resources/"+r+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "旧基础上的私人草稿"}, 200)
	releasePublish(b, pid, release)
	status := b.request("GET", root+"/private-index", nil, 200)
	omissions := status["manifest"].(map[string]any)["omissions"].([]any)
	if len(omissions) != 1 || omissions[0].(map[string]any)["ref"].(map[string]any)["id"] != r {
		t.Fatal("stale input not explicitly omitted", status)
	}
}

func TestPrivateWorkingSearchScansBeyondFirstChunkPage(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	jobs := privateJobs(t, db)
	pid := createProject(b, "完整私有语料分页", "private-pages")
	bid := branch(b, pid, "正史")
	r := resource(b, pid, bid, "长篇草稿")
	var source strings.Builder
	for n := 0; n < 140; n++ {
		unit := []rune(strings.Repeat(fmt.Sprintf("编号%04d的档案记载山谷居民建造水渠和保存谷物的经过。", n), 30))
		source.WriteString(string(unit[:480]))
	}
	b.request("PUT", "/api/projects/"+pid+"/resources/"+r+"/draft", map[string]any{"revision": 0, "base_head": "", "text": source.String()}, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", root+"/index", map[string]any{}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	job, err := jobs.Create(ctx, actor, pid, bid, service.IndexJobInput{Kind: "working", OperationID: "private-page-build"})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Execute(ctx, job.ID); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	var target, text string
	if err = db.QueryRow("SELECT id,content FROM working_chunks WHERE index_id=? ORDER BY id LIMIT 1 OFFSET 130", job.IndexID).Scan(&target, &text); err != nil {
		t.Fatal("fixture needs more than one page", err)
	}
	result := b.request("POST", root+"/search", map[string]any{"query": text, "purpose": "research", "limit": 20, "token_budget": 262144, "include_working": true, "include_private_working": true}, 200)
	found := false
	for _, raw := range result["private_hits"].([]any) {
		if raw.(map[string]any)["id"] == target {
			found = true
		}
	}
	if !found {
		t.Fatal("second-page exact semantic input was never ranked")
	}
}

func TestPrivateWorkingChangesDuringInferenceCannotCommit(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	jobs := privateJobs(t, db)
	pid := createProject(b, "检索期间修改", "private-race")
	bid := branch(b, pid, "正史")
	r := resource(b, pid, bid, "动态草稿")
	draft := "/api/projects/" + pid + "/resources/" + r + "/draft"
	b.request("PUT", draft, map[string]any{"revision": 0, "base_head": "", "text": "少女为母亲采集灵药。"}, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", root+"/index", map[string]any{}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	job, err := jobs.Create(ctx, actor, pid, bid, service.IndexJobInput{Kind: "working", OperationID: "private-build-race"})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	jobs.Heartbeat = func(context.Context) {
		if !changed {
			changed = true
			b.request("PUT", draft, map[string]any{"revision": 1, "base_head": "", "text": "少女留在故乡照料母亲。"}, 200)
		}
	}
	if state, err := jobs.Execute(ctx, job.ID); err != nil || state != "BLOCKED" {
		t.Fatal("changed snapshot committed", state, err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM working_indexes WHERE id=?", job.IndexID).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale build visible", count, err)
	}
	jobs.Heartbeat = nil
	job, err = jobs.Create(ctx, actor, pid, bid, service.IndexJobInput{Kind: "working", OperationID: "private-query-race"})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Execute(ctx, job.ID); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	// Forward to actual local BGE, then mutate the draft while the caller has no
	// authority transaction. A post-embedding watermark check must reject it.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstream, err := http.NewRequestWithContext(req.Context(), "POST", os.Getenv("STUDIO_EMBEDDING_URL")+"/embed", req.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		upstream.Header = req.Header.Clone()
		upstream.ContentLength = req.ContentLength
		response, err := http.DefaultClient.Do(upstream)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer response.Body.Close()
		if _, err = db.ExecContext(req.Context(), "UPDATE resource_drafts SET revision=revision+1,body=? WHERE resource_id=? AND actor_id=?", "修改发生在向量返回前", r, actor.ID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	jobs.Knowledge.Embedder, err = retrieval.NewEmbedder(proxy.URL, os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = jobs.Knowledge.Search(ctx, actor, pid, bid, service.SearchInput{Query: "寻找救治家人的办法", Purpose: "author", Limit: 6, IncludeWorking: true, IncludePrivateWorking: true})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed query snapshot accepted", err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM context_packages WHERE project_id=?", pid).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale context persisted", count, err)
	}
}
