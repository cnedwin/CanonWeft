package integration

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestMemorySessionRetryPreservesLatestAndIsolation(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "会话重试", "session-retry")
	bid := branch(b, pid, "正史")
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	in := service.MemorySessionInput{OperationID: "session-create", Document: service.MemorySessionDocument{Title: "私人会话", Notes: "初稿"}}
	first := b.request("POST", root+"/sessions", in, 200)
	retry := b.request("POST", root+"/sessions", in, 200)
	if first["id"] != retry["id"] {
		t.Fatal("receipt loss duplicated session")
	}
	missing := in
	missing.OperationID = ""
	b.request("POST", root+"/sessions", missing, 400)
	changed := in
	changed.Document.Notes = "不能复用原请求修改内容"
	b.request("POST", root+"/sessions", changed, 409)
	edit := in
	edit.ID = first["id"].(string)
	edit.ExpectedRevision = 1
	edit.OperationID = "session-edit"
	edit.Document.Notes = "后续修改"
	b.request("POST", root+"/sessions", edit, 200)
	b.request("POST", root+"/sessions", edit, 200)
	latest := b.request("POST", root+"/sessions", in, 200)
	if latest["revision"] != float64(2) || latest["document"].(map[string]any)["notes"] != "后续修改" {
		t.Fatal("old retry clobbered newer checkpoint", latest)
	}
	edit.OperationID = "session-stale-edit"
	b.request("POST", root+"/sessions", edit, 409)
	b.request("GET", root+"/sessions/"+edit.ID, nil, 200)
	otherBranch := branch(b, pid, "改编")
	b.request("POST", "/api/projects/"+pid+"/branches/"+otherBranch+"/memory/sessions", in, 409)
	member := b.request("POST", "/api/members", map[string]any{"username": "session-editor", "password": "session-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "session-editor", "password": "session-password-123"}, 200)
	other.request("GET", root+"/sessions/"+edit.ID, nil, 403)
	other.request("POST", root+"/sessions", edit, 403)
	other.request("POST", root+"/sessions", in, 200)
	b.request("DELETE", "/api/projects/"+pid+"/members/"+member["id"].(string), nil, 200)
	other.request("POST", root+"/sessions", in, 403)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_sessions WHERE project_id=?", pid).Scan(&count); err != nil || count != 2 {
		t.Fatal("unexpected session count", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM operations WHERE kind='memory.session'").Scan(&count); err != nil || count != 3 {
		t.Fatal("unexpected receipt count", count, err)
	}
}

func TestMemorySessionConcurrentCreateUsesOneReceipt(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "并发会话", "session-concurrent")
	bid := branch(b, pid, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner'").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	svc := &service.Memory{IP: &service.IP{Workspace: &service.Workspace{DB: db}}}
	in := service.MemorySessionInput{OperationID: "same-request", Document: service.MemorySessionDocument{Title: "同一会话"}}
	type outcome struct {
		session service.MemorySession
		err     error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { s, e := svc.SaveSession(context.Background(), actor, pid, bid, in); results <- outcome{s, e} }()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.session.ID != second.session.ID {
		t.Fatal("concurrent retry forked checkpoint", first, second)
	}
}
