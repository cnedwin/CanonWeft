package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNovelSetupAssistanceExistingIPReuse(t *testing.T) {
	b, _, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	project := createProject(b, "保留原IP名称", "assisted-existing-ip")
	parent := branch(b, project, "正史")
	head := propose(b, project, parent, "", "assisted-existing-head", originalSnapshot())
	publish(b, project, parent, head)
	in := service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()}
	in.Setup.IPMode, in.Setup.ProjectID, in.Setup.BranchID, in.Setup.BaseHead = "existing", project, parent, head
	draft, err := entry.SaveDraft(ctx, actor.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	ip := &service.IP{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	svc := &service.NovelSetupAssistance{Generations: &service.Generations{IP: ip}}
	w, err := svc.Workspace(ctx, actor.ID, draft.ID, draft.Revision)
	if err != nil || w.Project.ID != project || w.Branch.ID == parent || w.Branch.Head != head {
		t.Fatal("prepared existing IP branch", err)
	}
	book, err := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "confirm-assisted-existing", AcceptSettings: true})
	if err != nil || book.ProjectID != project || book.BranchID != w.Branch.ID {
		t.Fatal("existing IP workspace not reused", err)
	}
	original, err := ip.ReadBranch(ctx, actor, project, parent)
	if err != nil || original.Head != head {
		t.Fatal("original canon changed", err)
	}
	if got := b.request("GET", "/api/projects/"+project, nil, 200); got["name"] != "保留原IP名称" {
		t.Fatal("assistant renamed original IP")
	}
}

func TestNovelSetupAssistanceUsesRealWorkspaceAndPreservesChoices(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	input := service.NovelSetupDraft{ID: service.NewID(), Step: 2, Setup: validNovelSetup()}
	input.Setup.Title = "建书助手测试"
	input.Setup.Idea = "修钟匠寻找失踪旧友"
	draft, err := entry.SaveDraft(ctx, actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	ip := &service.IP{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	transport := &generationTransport{}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelSetupAssistance{Generations: gen, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	w, err := svc.Workspace(ctx, actor.ID, draft.ID, draft.Revision)
	if err != nil {
		t.Fatal("prepare workspace", err)
	}
	again, err := svc.Workspace(ctx, actor.ID, draft.ID, draft.Revision)
	if err != nil || again.Project.ID != w.Project.ID || again.Branch.ID != w.Branch.ID {
		t.Fatal("duplicate preparation workspace", err)
	}
	var books int
	if err = db.QueryRow("SELECT COUNT(*) FROM novels WHERE project_id=?", w.Project.ID).Scan(&books); err != nil || books != 0 {
		t.Fatal("assistant confirmed book", err)
	}
	rid := resource(b, w.Project.ID, w.Branch.ID, "修钟题材资料")
	sourceText := "资料首句：钟摆的周期取决于摆长。\n故事地点是雨夜钟楼。\n资料尾句：校时必须保留操作记录。"
	release := prepareRelease(b, w.Project.ID, w.Branch.ID, "", "", "setup-source", []map[string]any{{"resource_id": rid, "text": sourceText}})
	releaseAdopt(b, w.Project.ID, release)
	releasePublish(b, w.Project.ID, release)
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "本地建书替身", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/architect", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	proposal := map[string]any{"fields": []service.SetupFieldCandidate{{Field: "title", Value: json.RawMessage(`"停在十一点的钟"`), Basis: "proposal", SourceIDs: []string{}, Reason: "以修钟职业连接失踪之谜"}, {Field: "world", Value: json.RawMessage(`"雨夜钟楼与旧城区"`), Basis: "source", SourceIDs: []string{rid}, Reason: "依据题材资料中的故事地点"}}, "warnings": []string{"此书名尚未检索重名"}}
	content, _ := json.Marshal(proposal)
	reply, _ := json.Marshal(map[string]any{"id": "setup-receipt", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 80}})
	transport.response = string(reply)
	request := service.SetupAssistInput{SetupID: draft.ID, Revision: draft.Revision, SourceIDs: []string{rid}, Notes: "根据资料完善书名和世界，但不要更换主角或改变我的模型选择", PriceID: price.ID, MaxOutputTokens: 2000, OperationID: "setup-assist"}
	task, err := svc.Prepare(ctx, actor.ID, draft.ID, request)
	if err != nil {
		t.Fatal("prepare", err)
	}
	replayed, err := svc.Prepare(ctx, actor.ID, draft.ID, request)
	if err != nil || replayed.ID != task.ID {
		t.Fatal("lost prepare response created a new task", err)
	}
	if _, err = gen.Start(ctx, actor, w.Project.ID, task.ID); err == nil {
		t.Fatal("setup bypassed budget")
	}
	if _, err = (&service.Budgets{Workspace: entry.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 10000, PerCall: 1000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, w.Project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, task.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "novel-setup-assistant") || !strings.Contains(transport.payload, "资料首句") || !strings.Contains(transport.payload, "资料尾句") || !strings.Contains(transport.payload, "修钟匠寻找失踪旧友") {
		t.Fatal("setup method or complete selected sources absent")
	}
	suggestion, err := svc.Suggestion(ctx, actor.ID, draft.ID, task.ID)
	if err != nil || len(suggestion.Fields) != 2 {
		t.Fatal("suggestion", err)
	}
	// Neither generation nor reading the suggestion changes the original fields.
	current, err := entry.ReadDraft(ctx, actor.ID, draft.ID)
	if err != nil || current.Setup.Title != draft.Setup.Title {
		t.Fatal("automatic setup adoption", err)
	}
	if _, err = svc.Adopt(ctx, actor.ID, draft.ID, service.SetupAssistAdoption{GenerationID: task.ID, Revision: draft.Revision, Fields: []service.SetupFieldCandidate{{Field: "model_choice", Value: json.RawMessage(`"configured"`)}}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("model settings changed by assistant", err)
	}
	suggestion.Fields[0].Value = json.RawMessage(`"停钟旧事（作者修订）"`)
	if os.Getenv("STUDIO_SETUP_ASSIST_BROWSER_NODE") != "" {
		assertSetupAssistanceBrowser(t, ip, task.ID)
		current, err = entry.ReadDraft(ctx, actor.ID, draft.ID)
	} else {
		current, err = svc.Adopt(ctx, actor.ID, draft.ID, service.SetupAssistAdoption{GenerationID: task.ID, Revision: draft.Revision, Fields: suggestion.Fields[:1]})
	}
	if err != nil {
		t.Fatal("adopt selected", err)
	}
	if current.Setup.Title != "停钟旧事（作者修订）" || current.Setup.World != draft.Setup.World || current.Setup.ModelChoice != draft.Setup.ModelChoice || len(current.Setup.Characters) != len(draft.Setup.Characters) {
		t.Fatal("selection lost unselected/protected fields")
	}
	if _, err = svc.Suggestion(ctx, actor.ID, draft.ID, task.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old proposal accepted after manual choice", err)
	}
	// A changed input blocks a queued task before any second provider request.
	request.Revision = current.Revision
	request.OperationID = "setup-stale"
	stale, err := svc.Prepare(ctx, actor.ID, draft.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	current.Setup.Style = "作者最新手改：克制而冷静"
	current, err = entry.SaveDraft(ctx, actor.ID, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, w.Project.ID, stale.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale setup sent", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected model call")
	}
	archive := filepath.Join(t.TempDir(), "before-confirm.nsbak")
	if _, err = backup.Create(ctx, db, ip.Blobs, archive); err != nil {
		t.Fatal("preconfirmation backup", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() { db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`") })
	blobRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("setup restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var payload, restoredPayload, confirmed string
	if err = db.QueryRow("SELECT payload FROM novel_setup_drafts WHERE id=?", draft.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT payload,confirmed_novel_id FROM novel_setup_drafts WHERE id=?", draft.ID).Scan(&restoredPayload, &confirmed); err != nil || payload != restoredPayload || confirmed != "" {
		t.Fatal("private setup lost in restore", err)
	}
	inputBytes, err := (storage.LocalBlobs{Root: blobRoot}).ReadObject(ctx, w.Project.ID, task.Frozen.InputHash)
	if err != nil || !strings.Contains(string(inputBytes), "资料首句") || !strings.Contains(string(inputBytes), "资料尾句") || !strings.Contains(string(inputBytes), draft.ID) {
		t.Fatal("restored setup source/request", err)
	}
	var enabled int
	if err = restored.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=TRUE").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("restored setup execution enabled", err)
	}
	book, err := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: current.Revision, OperationID: "confirm-assisted", AcceptSettings: true})
	if err != nil {
		t.Fatal("confirm prepared workspace", err)
	}
	if book.ProjectID != w.Project.ID || book.BranchID != w.Branch.ID || book.Setup.Title != current.Setup.Title {
		t.Fatal("book discarded preparation workspace")
	}
	if _, err = ip.ReadResource(ctx, actor, book.ProjectID, rid); err != nil {
		t.Fatal("materials lost at confirmation", err)
	}
	t.Log("Setup assistant: preset + full selected sources, paid gate, explicit edited field adoption, stale rejection, same project/branch confirmed; 1 local model call")
}
