package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"testing"
)

func TestMemoryAdoptionRequiresContextBeforeAnyModelDispatch(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, prepared := generationFixture(t, transport)
	bid := prepared.BranchID
	source := resource(b, pid, bid, "主题承诺")
	release := prepareRelease(b, pid, bid, "", "", "memory-dispatch-source", []map[string]any{{"resource_id": source, "text": "主人公的行动必须服务于守护亲人的主题。"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+source, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	session := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "theme-session", Document: service.MemorySessionDocument{Title: "主题"}}, 200)
	in := service.MemoryInput{OperationID: "memory-dispatch-save", Item: memory.Item{Session: session["id"].(string), Kind: "theme", Visibility: "author", Control: true, Pinned: true, Text: "守护亲人", Sources: []memory.Source{{Resource: source, Revision: release, Hash: src["hash"].(string), Start: 0, End: 5}}}}
	saved := decodeMemory(t, b.request("POST", root+"/items", in, 200))
	b.request("POST", root+"/items/"+saved.ID+"/adopt", service.MemoryAction{ExpectedRevision: saved.Revision, OperationID: "memory-dispatch-adopt"}, 200)
	ctx := context.Background()
	if _, err := svc.Start(ctx, a, pid, prepared.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("prepared task ignored newly adopted memory", err)
	}
	_, err := svc.Prepare(ctx, a, pid, service.GenerationInput{BranchID: bid, Role: "writer", PriceID: prepared.Frozen.Price.ID, OperationID: "skip-memory", Request: models.Request{Prompt: "绕过记忆", MaxOutputTokens: 100}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("new writer bypassed required context", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("gate dispatched model")
	}
}

func decodeMemory(t *testing.T, result map[string]any) memory.Item {
	t.Helper()
	raw, _ := json.Marshal(result)
	var item memory.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestMemoryCandidateAdoptionContextInvalidationAndPrivacy(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "记忆闭环", "memory-project")
	bid := branch(b, pid, "正史")
	ip := propose(b, pid, bid, "", "ip", originalSnapshot())
	publish(b, pid, bid, ip)
	source := resource(b, pid, bid, "主线与人物依据")
	text := "🌙林舟追查雾城失踪案，守护亲人是他的核心动机。关键时刻放下复仇，救出同伴。"
	release := prepareRelease(b, pid, bid, ip, "", "memory-source", []map[string]any{{"resource_id": source, "text": text, "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+source, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	session := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "planning-session", Document: service.MemorySessionDocument{Title: "第一卷规划", Goal: "保护核心动机", FocusIDs: []string{}}}, 200)
	sid := session["id"].(string)
	knowledge := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	legacyContext := b.request("POST", knowledge+"/search", map[string]any{"query": "失踪案", "purpose": "writer", "perspective": "lin-1", "limit": 3}, 200)
	if legacyContext["memory"] != nil {
		t.Fatal("fixture should predate memory")
	}
	in := service.MemoryInput{OperationID: "memory-save", Item: memory.Item{Session: sid, Kind: "mainline", Visibility: "author", Control: true, Pinned: true, Text: "主线是追查失踪案，人物最终选择守护而非复仇。", Sources: []memory.Source{{Resource: source, Revision: release, Hash: src["hash"].(string), Start: 0, End: len([]rune(text))}}}}
	in.Item.Timeline = "main"
	saved := decodeMemory(t, b.request("POST", root+"/items", in, 200))
	if saved.State != "candidate" || saved.Actor == "" {
		t.Fatal("save published candidate")
	}
	if retry := decodeMemory(t, b.request("POST", root+"/items", in, 200)); retry.ID != saved.ID || retry.Revision != saved.Revision {
		t.Fatal("retry duplicated memory")
	}
	writer := service.MemoryCompileInput{Purpose: "writer", Perspective: "lin-1", Timeline: "main", Query: "主线", Budget: 65536}
	preview := b.request("POST", root+"/compile", writer, 200)
	if len(preview["items"].([]any)) != 0 {
		t.Fatal("candidate leaked into writer")
	}
	adopted := decodeMemory(t, b.request("POST", root+"/items/"+saved.ID+"/adopt", service.MemoryAction{ExpectedRevision: saved.Revision, OperationID: "memory-adopt"}, 200))
	if adopted.State != "adopted_plan" || adopted.Actor != "" || adopted.Session != "" {
		t.Fatal("plan not separated from canon", adopted)
	}
	preview = b.request("POST", root+"/compile", writer, 200)
	selected := preview["items"].([]any)
	if len(selected) != 1 || selected[0].(map[string]any)["channel"] != "control" {
		t.Fatal("mainline lost", preview)
	}
	context := b.request("POST", knowledge+"/search", map[string]any{"query": "失踪案", "purpose": "writer", "perspective": "lin-1", "limit": 3}, 200)
	if context["memory"] == nil {
		t.Fatal("RAG omitted mandatory memory")
	}
	b.request("GET", knowledge+"/contexts/"+context["id"].(string), nil, 200)
	// A different project member cannot read or overwrite this private checkpoint.
	member := b.request("POST", "/api/members", map[string]any{"username": "memory-editor", "password": "memory-editor-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "memory-editor", "password": "memory-editor-password-123"}, 200)
	listed := other.request("GET", root, nil, 200)
	if len(listed["sessions"].([]any)) != 0 {
		t.Fatal("private checkpoint leaked")
	}
	other.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "other-session-edit", ID: sid, ExpectedRevision: 1, Document: service.MemorySessionDocument{Title: "篡改"}}, 403)
	// Changing the adopted plan invalidates already frozen generation input.
	in.Item = adopted
	in.Item.Session = sid
	in.Item.Text = "主线修改候选"
	in.ExpectedRevision = adopted.Revision
	in.OperationID = "memory-edit"
	edited := decodeMemory(t, b.request("POST", root+"/items", in, 200))
	b.request("GET", knowledge+"/contexts/"+context["id"].(string), nil, 409)
	b.request("GET", knowledge+"/contexts/"+legacyContext["id"].(string), nil, 409)
	other.request("POST", root+"/compile", writer, 409)
	writer.Timeline = "another-timeline"
	other.request("POST", root+"/compile", writer, 200)
	writer.Timeline = "main"
	adopted = decodeMemory(t, b.request("POST", root+"/items/"+edited.ID+"/adopt", service.MemoryAction{ExpectedRevision: edited.Revision, OperationID: "memory-readopt"}, 200))
	// Current source replacement makes the required plan stale, never silently absent.
	next := prepareRelease(b, pid, bid, ip, "", "memory-source-next", []map[string]any{{"resource_id": source, "expected_head": release, "text": "主线和人物动机发生了变化。", "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	list := b.request("GET", root, nil, 200)
	if list["items"].([]any)[0].(map[string]any)["state"] != "stale" {
		t.Fatal("stale source not surfaced")
	}
	b.request("POST", root+"/compile", writer, 409)
	writer.Timeline = "another-timeline"
	b.request("POST", root+"/compile", writer, 200)
	var revisions int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE item_id=?", adopted.ID).Scan(&revisions); err != nil || revisions != 4 {
		t.Fatal("immutable revisions missing", revisions, err)
	}
}

func TestMemoryCannotDeclassifySourcesAndSessionsUseCAS(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "保密记忆", "memory-private")
	bid := branch(b, pid, "正史")
	id := resource(b, pid, bid, "秘密")
	release := prepareRelease(b, pid, bid, "", "", "secret-release", []map[string]any{{"resource_id": id, "text": "秘密身份只能作者知道。"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+id, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	input := service.MemorySessionInput{OperationID: "create-session", Document: service.MemorySessionDocument{Title: "会话", FocusIDs: []string{}}}
	session := b.request("POST", root+"/sessions", input, 200)
	input.ID = session["id"].(string)
	input.ExpectedRevision = 1
	input.Document.Notes = "最新草稿"
	input.OperationID = "edit-session"
	b.request("POST", root+"/sessions", input, 200)
	input.OperationID = "another-window-session"
	b.request("POST", root+"/sessions", input, 409)
	in := service.MemoryInput{OperationID: "declassify", Item: memory.Item{Session: input.ID, Kind: "summary", Visibility: "public", Text: "透露秘密", Sources: []memory.Source{{Resource: id, Revision: release, Hash: src["hash"].(string), Start: 0, End: 5}}}}
	b.request("POST", root+"/items", in, 400)
	in.Item.Visibility = "author"
	in.OperationID = "invalid-range"
	in.Item.Sources[0].End = 999999
	b.request("POST", root+"/items", in, 400)
}
