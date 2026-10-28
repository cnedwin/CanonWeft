package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

// Only the provider transport is doubled. Real adapter parsing, credential vault,
// reservations, receipts, evaluation scoring and activation transactions run.
type protocolFixture struct {
	calls       atomic.Int32
	lost        bool
	invalid     bool
	payloads    []string
	after       func()
	inputTokens int64
}

func (p *protocolFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls.Add(1)
	raw, _ := io.ReadAll(r.Body)
	p.payloads = append(p.payloads, string(raw))
	if p.lost {
		return nil, errors.New("tests-only lost provider receipt")
	}
	var request struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("bad protocol fixture request")
	}
	prompt := request.Messages[len(request.Messages)-1].Content
	index := strings.Index(prompt, "Input JSON:\n")
	var input struct {
		Source string `json:"source"`
	}
	if index >= 0 {
		if json.NewDecoder(strings.NewReader(prompt[index+len("Input JSON:\n"):])).Decode(&input) != nil {
			return nil, errors.New("bad task input")
		}
	} else {
		index = strings.Index(prompt, "<fixed-method-data>\n")
		if index < 0 {
			return nil, errors.New("task input missing")
		}
		var payload struct {
			Input struct {
				Source string `json:"source"`
			} `json:"input"`
		}
		if json.NewDecoder(strings.NewReader(prompt[index+len("<fixed-method-data>\n"):])).Decode(&payload) != nil {
			return nil, errors.New("bad method input")
		}
		input.Source = payload.Input.Source
	}
	rejected := strings.Contains(prompt, "fixed-method-data") && (strings.Contains(input.Source, "密钥") || strings.Contains(input.Source, "收费") || strings.Contains(input.Source, "正史"))
	summary := input.Source
	if rejected {
		summary = ""
	}
	output, _ := json.Marshal(map[string]any{"summary": summary, "rejected": rejected})
	if p.invalid {
		output = []byte("invalid JSON candidate")
	}
	tokens := p.inputTokens
	if tokens == 0 {
		tokens = 20
	}
	body, _ := json.Marshal(map[string]any{"id": "fixture-receipt", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": tokens, "completion_tokens": 10, "prompt_cache_hit_tokens": 0}})
	if p.after != nil {
		p.after()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
}

func modelFixture(t *testing.T) (*fixture, *protocolFixture, service.QualificationInput) {
	f := isolated(t)
	vault := t.TempDir()
	t.Setenv("STUDIO_VAULT_MASTER", filepath.Join(vault, "master", "key.dpapi"))
	t.Setenv("STUDIO_VAULT_ROOT", filepath.Join(vault, "cipher"))
	ctx := context.Background()
	connections := &service.Models{Workspace: f.svc.IP.Workspace}
	config, err := connections.Save(ctx, f.actor.ID, "", service.ModelInput{Provider: "deepseek", Name: "local protocol fixture", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", Enabled: true, APIKey: "test-only-not-a-real-provider-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connections.Bind(ctx, f.actor.ID, domain.ModelBinding{Role: "researcher", ConnectionID: config.ID, ConnectionRevision: 1}); err != nil {
		t.Fatal(err)
	}
	p := &protocolFixture{}
	gen := &service.Generations{IP: f.svc.IP, Adapter: models.New(p)}
	f.svc.Generations = gen
	price, err := gen.SavePrice(ctx, f.actor.ID, service.PriceCard{ConnectionID: config.ID, ConnectionRevision: 1, Currency: "USD", Input: 1000000, Output: 1000000, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&service.Budgets{Workspace: f.svc.IP.Workspace}).Configure(ctx, f.actor.ID, service.Budget{Currency: "USD", Limit: 10000000, PerCall: 1000000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	presets, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "agent", Role: "researcher"})
	if err != nil {
		t.Fatal(err)
	}
	return f, p, service.QualificationInput{OperationID: service.NewID(), VersionID: presets.BaselineID, BaselineID: presets.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", Role: "researcher", PriceID: price.ID, MaxOutputTokens: 128}
}

func drive(t *testing.T, f *fixture, id string) service.QualificationRun {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 150; i++ {
		state, err := f.svc.Advance(ctx, id)
		if err != nil {
			t.Fatal("evaluation advance", err)
		}
		r, err := f.svc.Read(ctx, f.actor, f.project, id)
		if err != nil {
			t.Fatal(err)
		}
		if state != "RUNNING" && state != "QUEUED" {
			return r
		}
		for _, step := range r.Steps {
			if step.State == "RUNNING" && step.ChildID != "" {
				if step.ChildKind == "generation" {
					if _, err = f.svc.Generations.Execute(ctx, step.ChildID); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err = f.svc.ToolTrials.Execute(ctx, step.ChildID); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	t.Fatal("evaluation failed to reach terminal state")
	return service.QualificationRun{}
}

func TestActualThreeArmProtocolEvaluationAndExactActivation(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || !q.Ready || q.Calls != 18 || q.Frozen.Estimate <= 0 {
		t.Fatal(q, err)
	}
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil || again.ID != r.ID {
		t.Fatal("duplicate evaluation", err)
	}
	result := drive(t, f, r.ID)
	if result.State != "PASSED" || result.QualificationID == "" || len(result.Steps) != 18 || p.calls.Load() != 18 {
		t.Fatal("incomplete real comparison", result.State, len(result.Steps), p.calls.Load())
	}
	noMethodFailures := 0
	for _, step := range result.Steps {
		if step.Arm == "no_method" && !step.Metrics.Score.Passed {
			noMethodFailures++
		}
		if !step.Metrics.UsageKnown || step.Metrics.Cost <= 0 || step.ChildID == "" {
			t.Fatal("missing execution/cost receipt", step)
		}
	}
	if noMethodFailures != 2 {
		t.Fatal("no-method negative comparison missing", noMethodFailures)
	}
	for _, payload := range p.payloads {
		if strings.Contains(payload, `"gold"`) || strings.Contains(payload, `"expected"`) || strings.Contains(payload, "ScoreThreshold") {
			t.Fatal("heldout answers leaked to provider")
		}
	}
	wrong := service.BindingInput{OperationID: service.NewID(), QualificationID: result.QualificationID, TaskKind: "professional.novel"}
	if _, err = f.svc.Activate(ctx, f.actor, f.project, wrong); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("task scope expanded", err)
	}
	wrong.TaskKind = "shared.contract"
	wrong.OperationID = service.NewID()
	binding, err := f.svc.Activate(ctx, f.actor, f.project, wrong)
	if err != nil {
		t.Fatal(err)
	}
	production := service.GenerationInput{BindingID: binding.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"生产用途候选"}`), Request: models.Request{Prompt: "propose contract candidate", MaxOutputTokens: 127}}
	if _, err = f.svc.Generations.Prepare(ctx, f.actor, f.project, production); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("token profile mismatch accepted", err)
	}
	production.Request.MaxOutputTokens = 128
	job, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, production)
	if err != nil {
		t.Fatal(err)
	}
	if job.Frozen.Capability.Mode != "QUALIFIED" || job.Frozen.Capability.QualificationID != result.QualificationID {
		t.Fatal("qualified references not frozen")
	}
	if err = (&service.Capabilities{IP: f.svc.IP}).Revoke(ctx, f.actor, f.project, input.VersionID, "revoke entire tested version"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, job.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked qualified task dispatched", err)
	}
	wrong.OperationID = service.NewID()
	wrong.ExpectedBinding = binding.ID
	wrong.Rollback = true
	if _, err = f.svc.Activate(ctx, f.actor, f.project, wrong); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked qualification rollback accepted", err)
	}
}

func TestUnknownEvaluationReceiptNeverResendsAndBudgetStaysReserved(t *testing.T) {
	f, p, input := modelFixture(t)
	p.lost = true
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	result := drive(t, f, r.ID)
	if result.State != "BLOCKED" || !strings.Contains(result.Reason, "未知") || p.calls.Load() != 1 {
		t.Fatal(result.State, result.Reason, p.calls.Load())
	}
	if result.Steps[0].State != "UNKNOWN" || result.Steps[0].Metrics.UsageKnown {
		t.Fatal("unknown child still claims running/known measurement", result.Steps[0].State)
	}
	for i := 0; i < 3; i++ {
		if _, err = f.svc.Advance(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls.Load() != 1 {
		t.Fatal("unknown provider request resent")
	}
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || budget.Reserved <= 0 || budget.Spent != 0 {
		t.Fatal("unknown charge hold released", budget, err)
	}
}

func TestConfigurationRotationStopsEvaluationBeforeDispatch(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&service.Models{Workspace: f.svc.IP.Workspace}).Save(ctx, f.actor.ID, r.Frozen.Config.ID, service.ModelInput{Provider: "deepseek", Name: "rotated", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", Enabled: true, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	state, err := f.svc.Advance(ctx, r.ID)
	if err != nil || state != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("rotated evaluation dispatched", state, p.calls.Load(), err)
	}
}

func TestQualifiedBindingRollbackCASAndInflightFreeze(t *testing.T) {
	f, _, input := modelFixture(t)
	ctx := context.Background()
	first, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	first = drive(t, f, first.ID)
	b1, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: first.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, service.GenerationInput{BindingID: b1.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"frozen older binding"}`), Request: models.Request{Prompt: "candidate", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	caps := &service.Capabilities{IP: f.svc.IP}
	old, err := caps.Read(ctx, f.actor, f.project, input.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	definition := old.Definition
	definition.Instructions += "\nPreserve original sources with no additional authority."
	newVersion, err := caps.Save(ctx, f.actor, f.project, service.CapabilitySave{OperationID: service.NewID(), ExpectedHead: old.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	input.OperationID = service.NewID()
	input.VersionID = newVersion.ID
	input.ExpectedBinding = b1.ID
	second, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	second = drive(t, f, second.ID)
	b2, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: second.QualificationID, TaskKind: "shared.contract", ExpectedBinding: b1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, job.ID); err != nil {
		t.Fatal("switch invalidated frozen older binding", err)
	}
	if state, err := f.svc.Generations.Execute(ctx, job.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	completed, err := f.svc.Generations.Read(ctx, f.actor, f.project, job.ID)
	if err != nil || completed.Frozen.Capability.BindingID != b1.ID || completed.Output == nil {
		t.Fatal("inflight binding changed", err)
	}
	rollback := service.BindingInput{OperationID: service.NewID(), QualificationID: first.QualificationID, TaskKind: "shared.contract", ExpectedBinding: b1.ID, Rollback: true}
	if _, err = f.svc.Activate(ctx, f.actor, f.project, rollback); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale CAS rollback accepted", err)
	}
	rollback.OperationID = service.NewID()
	rollback.ExpectedBinding = b2.ID
	b3, err := f.svc.Activate(ctx, f.actor, f.project, rollback)
	if err != nil || b3.QualificationID != first.QualificationID {
		t.Fatal("still-qualified rollback rejected", err)
	}
	if _, err = f.db.Exec("UPDATE capability_qualifications SET expires_at=UTC_TIMESTAMP()-INTERVAL 1 SECOND WHERE id=?", first.QualificationID); err != nil {
		t.Fatal(err)
	}
	rollback.OperationID = service.NewID()
	rollback.ExpectedBinding = b3.ID
	if _, err = f.svc.Activate(ctx, f.actor, f.project, rollback); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("expired qualification activated", err)
	}
}

func TestRevokedAncestorAfterProviderOutputCannotBeAdopted(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	caps := &service.Capabilities{IP: f.svc.IP}
	old, err := caps.Read(ctx, f.actor, f.project, input.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	definition := old.Definition
	definition.Instructions += "\nSecond immutable candidate."
	child, err := caps.Save(ctx, f.actor, f.project, service.CapabilitySave{OperationID: service.NewID(), ExpectedHead: old.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	input.VersionID = child.ID
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	b, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, service.GenerationInput{BindingID: b.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"known output still cannot publish"}`), Request: models.Request{Prompt: "candidate", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	p.after = func() {
		if err := caps.Revoke(ctx, f.actor, f.project, old.ID, "ancestor revoked after dispatch"); err != nil {
			t.Error(err)
		}
	}
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Read(ctx, f.actor, f.project, job.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked ancestor output reusable", err)
	}
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || budget.Spent <= 0 {
		t.Fatal("late receipt fees lost", err)
	}
}

func TestPauseAfterChildPreparedStopsBeforeProviderDispatch(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := r.Steps[0].ChildID
	if child == "" {
		t.Fatal("child not prepared")
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.Generations.Execute(ctx, child); err != nil || state != "PAUSED" || p.calls.Load() != 0 {
		t.Fatal("paused parent dispatched provider child", state, p.calls.Load(), err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	result := drive(t, f, r.ID)
	if result.State != "PASSED" || p.calls.Load() != 18 {
		t.Fatal("pause/resume lost or duplicated child", result.State, p.calls.Load())
	}
}

func TestPauseThenCancelReleasesOnlyUndispatchedChildReservation(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.Advance(ctx, r.ID); err != nil || state != "CANCELLED" {
		t.Fatal("paused cancel did not finalize", state, err)
	}
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 0 || p.calls.Load() != 0 {
		t.Fatal("undispatched cancel leaked reservation or called provider", budget, p.calls.Load(), err)
	}
}

func TestReaderAndOtherProjectCannotRunOrReadEvaluation(t *testing.T) {
	f, _, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	w := f.svc.IP.Workspace
	reader, err := w.CreateMember(ctx, f.actor.ID, "evaluation-reader", "reader-password-123456", "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err = w.SetMember(ctx, f.actor.ID, f.project, reader.ID, "reader", false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Create(ctx, reader, f.project, input); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader ran evaluation", err)
	}
	if _, err = f.svc.Read(ctx, reader, f.project, r.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader accessed another actor evidence", err)
	}
	other, err := w.CreateProject(ctx, f.actor.ID, "other isolated project", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Read(ctx, f.actor, other.ID, r.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("cross-project evaluation leaked", err)
	}
}
