package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

func TestNovelPlanningAssistanceCandidateAndStaleDraft(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, d.ID, service.NovelConfirmation{Revision: d.Revision, OperationID: "assist-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	plans := &service.NovelPlanning{IP: ip}
	r, err := plans.Open(ctx, actor.ID, book.ID, "world", "")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := plans.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = plans.Save(ctx, actor.ID, book.ID, r.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	content := `{"explanation":"聚焦寻找妹妹的线索","questions":["失踪动机保留待定"],"text":{"mainline":"循药方中的错误剂量追查妹妹失踪的原因"}}`
	response, _ := json.Marshal(map[string]any{"id": "planning-test-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport := &generationTransport{response: string(response)}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	assist := &service.NovelPlanningAssistance{Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}, Generations: gen}
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "规划隔离测试", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/architect", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	in := service.NovelPlanningAssistInput{Revision: draft.Revision, Hash: draft.Hash, Notes: "保留人物身份，细化调查主线", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "assist-once"}
	installed, err := plans.InstallAgents(ctx, actor.ID, book.ID)
	if err != nil || len(installed.Versions) != 5 {
		t.Fatal("missing editable planning defaults", err)
	}
	again, err := plans.InstallAgents(ctx, actor.ID, book.ID)
	if err != nil || again.RootID != installed.RootID {
		t.Fatal("duplicate preset installation", err)
	}
	custom := in
	custom.OperationID = "assist-custom-method"
	custom.AgentVersionID = installed.Versions[1].ID
	customTask, err := assist.Prepare(ctx, actor.ID, book.ID, r.ID, custom)
	if err != nil || customTask.Frozen.Capability == nil || customTask.Frozen.Capability.Mode != "TRIAL" {
		t.Fatal("selected planning method not applied as explicit trial", err)
	}
	task, err := assist.Prepare(ctx, actor.ID, book.ID, r.ID, in)
	if err != nil {
		t.Fatal("prepare assistance", err)
	}
	replay, err := assist.Prepare(ctx, actor.ID, book.ID, r.ID, in)
	if err != nil || replay.ID != task.ID {
		t.Fatal("preparation replay changed task", err)
	}
	changed := in
	changed.Notes = "另一种建议"
	if _, err = assist.Prepare(ctx, actor.ID, book.ID, r.ID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same operation changed request", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("prepare called provider")
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err == nil {
		t.Fatal("disabled budget started generation")
	}
	// Only the test-only transport can run; this never makes a network request.
	if _, err = (&service.Budgets{Workspace: entry.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, task.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "小说规划协作方法") || !strings.Contains(transport.payload, "未确认的设定草稿") {
		t.Fatal("preset or initial characters missing from actual request")
	}
	suggestion, err := assist.Suggestion(ctx, actor.ID, book.ID, r.ID, task.ID)
	if err != nil || suggestion.Draft.Document.Mainline == draft.Document.Mainline {
		t.Fatal("missing suggestion", err)
	}
	unchanged, err := plans.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil || unchanged.Revision != draft.Revision || unchanged.Document.Mainline != draft.Document.Mainline || unchanged.Resource.Head != "" {
		t.Fatal("model auto-saved or published", err)
	}
	if node := os.Getenv("STUDIO_NOVEL_BROWSER_NODE"); node != "" {
		web, e := filepath.Abs("../../../web")
		if e != nil {
			t.Fatal(e)
		}
		server := httptest.NewUnstartedServer(nil)
		origin := "http://" + server.Listener.Addr().String()
		app := httpapi.New(db, origin)
		app.Static("/", filepath.Join(web, "dist"))
		server.Config.Handler = app
		server.Start()
		defer server.Close()
		browserContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, e := exec.CommandContext(browserContext, node, filepath.Join(web, "tests", "novel-planning-assistance-browser.cjs"), origin, task.ID, installed.Versions[1].ID).CombinedOutput()
		if e != nil {
			t.Fatalf("planning assistance browser: %v\n%s", e, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		current, e := plans.Read(ctx, actor.ID, book.ID, r.ID)
		if e != nil || current.Document.Mainline != suggestion.Draft.Document.Mainline || current.Resource.Head != "" {
			t.Fatal("browser did not save only the private proposal", e)
		}
	} else {
		if _, err = plans.Save(ctx, actor.ID, book.ID, r.ID, suggestion.Draft); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = assist.Suggestion(ctx, actor.ID, book.ID, r.ID, task.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old suggestion overwrote newer draft", err)
	}
	current, err := plans.Read(ctx, actor.ID, book.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = current.Revision
	in.Hash = current.Hash
	in.OperationID = "assist-before-settings-change"
	pending, err := assist.Prepare(ctx, actor.ID, book.ID, r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := entry.ReadSettingsDraft(ctx, actor.ID, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings.Setup.Theme = "自由与责任的矛盾"
	settings, err = entry.SaveSettingsDraft(ctx, actor.ID, book.ID, settings)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := entry.PreviewSettings(ctx, actor.ID, book.ID, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = entry.ConfirmSettings(ctx, actor.ID, book.ID, service.NovelSettingsConfirmation{DraftRevision: settings.Revision, ExpectedRevision: book.SettingsRevision, PreviewHash: preview.Hash, OperationID: "assist-settings-change", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, pending.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale novel settings reached provider start", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected repeated model execution")
	}
}
