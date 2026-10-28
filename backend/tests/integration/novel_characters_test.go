package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestNovelCharacterDesignWorkflow(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, d.ID, service.NovelConfirmation{Revision: d.Revision, OperationID: "characters-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	transport := &generationTransport{}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelCharacters{Generations: gen, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	root := "/api/novels/" + book.ID + "/character-designs"
	create := service.CharacterDesignCreate{OperationID: "character-create", Notes: "两位同名不同命运的悬疑角色", Genre: "悬疑", Count: 2}
	draft, err := svc.Open(ctx, actor.ID, book.ID, create)
	if err != nil {
		t.Fatal("open", err)
	}
	replay, err := svc.Open(ctx, actor.ID, book.ID, create)
	if err != nil || replay.ResourceID != draft.ResourceID {
		t.Fatal("create replay", err)
	}
	changed := create
	changed.Notes = "不同要求"
	if _, err = svc.Open(ctx, actor.ID, book.ID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("create hash", err)
	}
	ids := draft.Document.IDs
	proposal := novel.CharacterProposal{Entities: []domain.Entity{{ID: ids[0], Kind: "character", Name: "周守时", Aliases: []string{}, Description: "沉默的修表匠"}, {ID: ids[1], Kind: "character", Name: "周守时", Aliases: []string{}, Description: "外向的档案员"}}, Assertions: []domain.Assertion{{ID: "friend", Subject: ids[0], Predicate: "旧友", Value: "知道档案员的旧名", ObjectID: ids[1], Kind: "fact", KnownBy: []string{}, Visibility: "author", Source: "创作建议"}}, Explanation: "同名不同职业，关系是待作者确认的设定", Warnings: []string{"保留不同身份"}}
	content, _ := json.Marshal(proposal)
	response, _ := json.Marshal(map[string]any{"id": "character-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport.response = string(response)
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "角色隔离测试", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/character_designer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := svc.Install(ctx, actor.ID, book.ID, "")
	if err != nil {
		t.Fatal("install", err)
	}
	again, err := svc.Install(ctx, actor.ID, book.ID, "")
	if err != nil || again.ID != agent.ID {
		t.Fatal("preset replay", err)
	}
	in := service.NovelPlanningAssistInput{Revision: draft.Revision, Hash: draft.Hash, Notes: create.Notes, PriceID: price.ID, MaxOutputTokens: 4096, OperationID: "character-generate"}
	task, err := svc.Prepare(ctx, actor.ID, book.ID, draft.ResourceID, in)
	if err != nil {
		t.Fatal("prepare", err)
	}
	if task.Frozen.Capability == nil || task.Frozen.Capability.AgentVersionID != agent.ID || transport.calls.Load() != 0 {
		t.Fatal("preset pin / no-call prepare")
	}
	if r, e := svc.Prepare(ctx, actor.ID, book.ID, draft.ResourceID, in); e != nil || r.ID != task.ID {
		t.Fatal("generation replay", e)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err == nil {
		t.Fatal("disabled budget allowed call")
	}
	budgets := &service.Budgets{Workspace: entry.Workspace}
	if _, err = budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal("start", err)
	}
	if state, e := gen.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute", state, e)
	}
	if !strings.Contains(transport.payload, "群像人物与一句话完善") || !strings.Contains(transport.payload, ids[0]) {
		t.Fatal("missing method or IDs")
	}
	suggestion, err := svc.Suggestion(ctx, actor.ID, book.ID, draft.ResourceID, task.ID)
	if err != nil {
		t.Fatal("suggestion", err)
	}
	if len(suggestion.Draft.Document.Candidate.Entities) != len(draft.Document.Base.Entities)+2 {
		t.Fatal("lost original entities")
	}
	draft, err = svc.Save(ctx, actor.ID, book.ID, draft.ResourceID, suggestion.Draft)
	if err != nil {
		t.Fatal("save proposal", err)
	}
	apply := service.CharacterDesignApply{Revision: draft.Revision, Hash: draft.Hash, IPRevision: draft.IPRevision, Selected: ids[:1], OperationID: "character-apply"}
	if _, err = svc.Apply(ctx, actor.ID, book.ID, draft.ResourceID, apply); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("dangling relation", err)
	}
	apply.Selected = ids
	result, err := svc.Apply(ctx, actor.ID, book.ID, draft.ResourceID, apply)
	if err != nil || len(result.Warnings) == 0 {
		t.Fatal("apply", err, result.Warnings)
	}
	if retry, e := svc.Apply(ctx, actor.ID, book.ID, draft.ResourceID, apply); e != nil || !retry.Replay || retry.Draft.IPRevision != result.Draft.IPRevision {
		t.Fatal("apply receipt", e)
	}
	current, err := ip.ReadDraft(ctx, actor, book.ProjectID, book.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Snapshot.Entities) != len(draft.Document.Base.Entities)+2 {
		t.Fatal("duplicates or lost base")
	}
	formal, err := ip.ReadBranch(ctx, actor, book.ProjectID, book.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	if formal.Head != draft.BaseHead {
		t.Fatal("private apply published")
	}
	// A later manual edit cannot be overwritten by a stale enrichment.
	enrich, err := svc.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "enrich", Notes: "补充口吻", Genre: "悬疑", EntityIDs: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	for i := range current.Snapshot.Entities {
		if current.Snapshot.Entities[i].ID == ids[0] {
			current.Snapshot.Entities[i].Description = "作者之后的手改"
		}
	}
	current, err = ip.SaveDraft(ctx, actor, book.ProjectID, book.BranchID, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Apply(ctx, actor.ID, book.ID, enrich.ResourceID, service.CharacterDesignApply{Revision: enrich.Revision, Hash: enrich.Hash, IPRevision: current.Revision, Selected: ids[:1], OperationID: "stale-enrich"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("concurrent hand edit overwritten", err)
	}
	other := b.request("POST", "/api/members", map[string]any{"username": "character-editor", "password": "character-editor-password", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+other["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "character-editor", "password": "character-editor-password"}, 200)
	editor.request("GET", root+"/"+draft.ResourceID, nil, 403)
	if len(editor.request("GET", root, nil, 200)["items"].([]any)) != 0 {
		t.Fatal("private designs exposed")
	}
	b.request("GET", root+"/"+draft.ResourceID, nil, 200)
	// Ordinary plaintext note drafts must not break JSON design listing.
	note, e := ip.CreateResource(ctx, actor, book.ProjectID, service.ResourceInput{BranchID: book.BranchID, Name: "普通资料", Kind: "note", OperationID: "plain-note"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ip.SaveResourceDraft(ctx, actor, book.ProjectID, note.ID, service.ResourceDraft{BaseHead: note.Head, Text: "普通非JSON资料"}); e != nil {
		t.Fatal(e)
	}
	b.request("GET", root, nil, 200)
	budget, e := budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = false
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
	assertCharacterDesignBrowser(t, ip, book.Setup.Title, draft.ResourceID, task.ID)
	// Book theme/settings are part of the same frozen context even before any
	// world plan has been published. A later author decision blocks old calls.
	pendingDraft, e := svc.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "settings-pin", Notes: "按本书主题设计一名人物", Genre: "悬疑", Count: 1})
	if e != nil {
		t.Fatal(e)
	}
	pending, e := svc.Prepare(ctx, actor.ID, book.ID, pendingDraft.ResourceID, service.NovelPlanningAssistInput{Revision: pendingDraft.Revision, Hash: pendingDraft.Hash, Notes: "保留主线和主题", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "settings-pin-call"})
	if e != nil {
		t.Fatal(e)
	}
	settings, e := entry.ReadSettingsDraft(ctx, actor.ID, book.ID)
	if e != nil {
		t.Fatal(e)
	}
	settings.Setup.Theme = "守住承诺而非独自复仇"
	settings, e = entry.SaveSettingsDraft(ctx, actor.ID, book.ID, settings)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := entry.PreviewSettings(ctx, actor.ID, book.ID, settings.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = entry.ConfirmSettings(ctx, actor.ID, book.ID, service.NovelSettingsConfirmation{DraftRevision: settings.Revision, ExpectedRevision: book.SettingsRevision, PreviewHash: preview.Hash, OperationID: "characters-new-theme", AcceptSettings: true}); e != nil {
		t.Fatal(e)
	}
	budget, e = budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = true
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
	if _, e = gen.Start(ctx, actor, book.ProjectID, pending.ID); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed theme did not block prepared call", e)
	}
	budget, e = budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = false
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+other["id"].(string), map[string]any{"role": "reader"}, 200)
	editor.request("GET", root, nil, 403)
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected model calls", transport.calls.Load())
	}
	namingID := assertNovelNaming(t, b, svc, transport, actor, book, price.ID)
	if _, e = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); e != nil {
		t.Fatal("inventory", e)
	}
	archive := filepath.Join(t.TempDir(), "characters.studio")
	if _, e = backup.Create(ctx, db, ip.Blobs, archive); e != nil {
		t.Fatal("backup", e)
	}
	cfg, e := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, e := db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); e != nil {
			t.Error(e)
		}
	})
	_, e = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(t.TempDir(), "restored"), Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}})
	if e != nil {
		t.Fatal("restore", e)
	}
	restored, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	var before, after string
	if e = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", draft.ResourceID, actor.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", draft.ResourceID, actor.ID).Scan(&after); e != nil || before != after {
		t.Fatal("candidate/provenance/receipt restore", e)
	}
	if e = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", namingID, actor.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", namingID, actor.ID).Scan(&after); e != nil || before != after || !strings.Contains(after, "顾闻钟") {
		t.Fatal("naming choices/locks/provenance restore", e)
	}
	var catalogID string
	if e = db.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(body,'$.design.naming.catalog.resource_id')) FROM resource_drafts WHERE resource_id=? AND actor_id=?", namingID, actor.ID).Scan(&catalogID); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", catalogID, actor.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", catalogID, actor.ID).Scan(&after); e != nil || before != after || !strings.Contains(after, "边城军职") {
		t.Fatal("editable name catalog restore", e)
	}
	var enabled int
	if e = restored.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=TRUE").Scan(&enabled); e != nil || enabled != 0 {
		t.Fatal("restored authority enabled", e)
	}
}
