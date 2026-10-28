package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type generationTransport struct {
	calls    atomic.Int32
	response string
	lost     bool
	model    string
	payload  string
	started  chan struct{}
	release  chan struct{}
}

func (t *generationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	var payload struct {
		Model string `json:"model"`
	}
	raw, _ := io.ReadAll(request.Body)
	t.payload = string(raw)
	_ = json.Unmarshal(raw, &payload)
	t.model = payload.Model
	if t.started != nil {
		close(t.started)
		<-t.release
	}
	if t.lost {
		return nil, errors.New("test lost response")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(t.response)), Header: http.Header{}}, nil
}

func generationFixture(t *testing.T, transport *generationTransport) (*browser, *service.Generations, domain.Actor, string, service.Generation) {
	t.Helper()
	modelEnvironment(t)
	b, db := setup(t)
	pid := createProject(b, "生成恢复", "one")
	bid := branch(b, pid, "正史")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "写作", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	svc := &service.Generations{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Adapter: models.New(transport)}
	price, err := svc.SavePrice(context.Background(), actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1000000, Output: 2000000, CacheRead: 100000, CacheWrite: 1000000, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := svc.Prepare(context.Background(), actor, pid, service.GenerationInput{BranchID: bid, Role: "writer", PriceID: price.ID, OperationID: "gen", Request: models.Request{Prompt: "测试输入", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&service.Budgets{Workspace: svc.IP.Workspace}).Configure(context.Background(), actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return b, svc, actor, pid, task
}

const generationSuccess = `{"id":"test-receipt","choices":[{"finish_reason":"stop","message":{"content":"测试候选"}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"prompt_cache_hit_tokens":10}}`

func TestGenerationContextPurposeAndStaleEvidenceCannotDispatch(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, old := generationFixture(t, transport)
	ctx := context.Background()
	bid := old.BranchID
	snap := originalSnapshot()
	snap.Entities[0].Aliases = []string{"青衣客"}
	cid := propose(b, pid, bid, "", "canon", snap)
	publish(b, pid, bid, cid)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	author := b.request("POST", path+"/search", map[string]any{"query": "青衣客的宝剑", "purpose": "author", "limit": 2}, 200)
	if len(author["matched_entities"].([]any)) != 1 || !strings.Contains(author["expanded_query"].(string), snap.Entities[0].Name) {
		t.Fatal("aliases missing from actual retrieval")
	}
	input := service.GenerationInput{BranchID: bid, ExpectedIPHead: cid, Role: "writer", ContextID: author["id"].(string), PriceID: old.Frozen.Price.ID, OperationID: "with-context", Request: models.Request{Prompt: "使用证据包提出候选", MaxOutputTokens: 100}}
	if _, err := svc.Prepare(ctx, a, pid, input); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("author evidence accepted as expression context", err)
	}
	writer := b.request("POST", path+"/search", map[string]any{"query": "宝剑", "purpose": "writer", "perspective": "lin-1", "limit": 2}, 200)
	input.ContextID = writer["id"].(string)
	task, err := svc.Prepare(ctx, a, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if task.Frozen.ContextID != input.ContextID {
		t.Fatal("context not frozen")
	}
	rid := resource(b, pid, bid, "新资料")
	release := prepareRelease(b, pid, bid, cid, "", "new-source", []map[string]any{{"resource_id": rid, "text": "新的资料令旧索引失效"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	if _, err = svc.Start(ctx, a, pid, task.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("stale context dispatched", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("model called before evidence validated")
	}
	b.request("POST", path+"/index", map[string]any{}, 200)
	writer = b.request("POST", path+"/search", map[string]any{"query": "宝剑", "purpose": "writer", "perspective": "lin-1", "limit": 2}, 200)
	input.ContextID = writer["id"].(string)
	input.OperationID = "fresh-context"
	task, err = svc.Prepare(ctx, a, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, a, pid, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, task.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "reference-data") || !strings.Contains(transport.payload, input.ContextID) || transport.calls.Load() != 1 {
		t.Fatal("frozen evidence absent from actual adapter request")
	}
}

func TestModelSwitchKeepsFrozenGenerationAndSuccessReuse(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, task := generationFixture(t, transport)
	ctx := context.Background()
	b.request("PUT", "/api/models/"+task.Frozen.Config.ID, map[string]any{"provider": "deepseek", "name": "更新配置", "model": "deepseek-v4-pro", "base_url": "https://api.deepseek.com", "enabled": true, "revision": 1}, 200)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": task.Frozen.Config.ID, "connection_revision": 2, "revision": 1}, 200)
	if _, err := svc.Start(ctx, a, pid, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Control(ctx, a, pid, task.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, task.ID); err != nil || state != "PAUSED" || transport.calls.Load() != 0 {
		t.Fatal("paused call sent", state, err)
	}
	if _, err := svc.Control(ctx, a, pid, task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if state, err := svc.Execute(ctx, task.ID); err != nil || state != "SUCCEEDED" {
			t.Fatal(state, err)
		}
	}
	if transport.calls.Load() != 1 || transport.model != "deepseek-flash" {
		t.Fatal("config switched or completed call resent")
	}
	result, err := svc.Read(ctx, a, pid, task.ID)
	if err != nil || result.Output == nil || result.Output.Text != "测试候选" || result.Call.Charged != 31 {
		t.Fatal("receipt/output not preserved", result.Call, err)
	}
}

func TestGenerationCancellationDropsLateOutputButRecordsUsage(t *testing.T) {
	transport := &generationTransport{response: generationSuccess, started: make(chan struct{}), release: make(chan struct{})}
	_, svc, a, pid, task := generationFixture(t, transport)
	installSourceRegistrationFixture(t, svc.IP.Workspace.DB)
	ctx := context.Background()
	if _, err := svc.Start(ctx, a, pid, task.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Execute(ctx, task.ID); done <- err }()
	select {
	case <-transport.started:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}
	if _, err := svc.Control(ctx, a, pid, task.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	close(transport.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := svc.Read(ctx, a, pid, task.ID)
	if err != nil || result.State != "CANCELLED" || result.Output != nil || result.Call.OutputHash != "" || result.Call.Charged != 31 {
		t.Fatal("late cancellation contract failed", result, err)
	}
}

func TestFrozenGenerationDefaultClosedAndLostReplyNeverResent(t *testing.T) {
	modelEnvironment(t)
	b, db := setup(t)
	project := createProject(b, "生成任务", "one")
	bid := branch(b, project, "正史")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	connection := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "写作", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": connection, "connection_revision": 1, "revision": 0}, 200)
	transport := &generationTransport{lost: true}
	svc := &service.Generations{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Adapter: models.New(transport)}
	ctx := context.Background()
	price, err := svc.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: connection, ConnectionRevision: 1, Currency: "USD", Input: 1000000, Output: 2000000, CacheRead: 100000, CacheWrite: 1000000, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	input := service.GenerationInput{BranchID: bid, Role: "writer", PriceID: price.ID, OperationID: "write-one", Request: models.Request{Prompt: "测试候选，不是生产正文", MaxOutputTokens: 100}}
	task, err := svc.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, actor, project, task.ID); !errors.Is(err, service.ErrBudgetClosed) {
		t.Fatal("paid gate not closed", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("request before budget approval")
	}
	budget := &service.Budgets{Workspace: svc.IP.Workspace}
	if _, err = budget.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task, err = svc.Start(ctx, actor, project, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Execute(ctx, task.ID)
	if err != nil || state != "UNKNOWN" {
		t.Fatal(state, err)
	}
	state, err = svc.Execute(ctx, task.ID)
	if err != nil || state != "UNKNOWN" || transport.calls.Load() != 1 {
		t.Fatal("lost response resent", state, err)
	}
	held, err := budget.Read(ctx, actor.ID)
	if err != nil || held.Reserved <= 0 || held.Spent != 0 {
		t.Fatal("unknown hold released", held, err)
	}
}
