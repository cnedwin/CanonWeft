package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"sync/atomic"
	"testing"
)

// Only tests provide this semantic answer transport. It exercises the actual
// independent evaluator, binding, request compiler, budget and report gates.
type semanticProtocolFixture struct {
	calls     atomic.Int32
	malformed bool
	after     func()
	transform func(*domain.SemanticReviewReport)
	lost      bool
	byTarget  map[string]domain.SemanticReviewReport
}

func (p *semanticProtocolFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls.Add(1)
	if p.lost {
		return nil, errors.New("tests-only unknown semantic receipt")
	}
	raw, _ := io.ReadAll(r.Body)
	var request struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil || len(request.Messages) == 0 {
		return nil, errors.New("test request missing")
	}
	prompt := request.Messages[len(request.Messages)-1].Content
	var input domain.SemanticReviewDocument
	method := strings.Contains(prompt, "<fixed-method-data>\n")
	if method {
		start := strings.Index(prompt, "<fixed-method-data>\n")
		var payload struct {
			Input domain.SemanticReviewDocument `json:"input"`
		}
		if json.NewDecoder(strings.NewReader(prompt[start+len("<fixed-method-data>\n"):])).Decode(&payload) != nil {
			return nil, errors.New("bad method fixture")
		}
		input = payload.Input
	} else {
		start := strings.Index(prompt, "Input JSON:\n")
		if start < 0 || json.NewDecoder(strings.NewReader(prompt[start+len("Input JSON:\n"):])).Decode(&input) != nil {
			return nil, errors.New("bad no-method fixture")
		}
	}
	report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "已检查固定内容"}
	if method {
		category := map[string]string{"belief": "fact_belief_claim", "temporal": "temporal", "secret": "knowledge_visibility", "world": "world_rule", "ambiguous": "unsupported", "injection": "fact_belief_claim"}[input.Target.ID]
		if input.Target.ID == "impact-removal" {
			category = "world_rule"
		}
		if input.Target.ID == "impact-time-unknown" {
			category = "unsupported"
		}
		if category != "" {
			report.Verdict = "FAIL"
			severity := "blocker"
			if category == "unsupported" {
				report.Verdict = "UNKNOWN"
				severity = "unknown"
			}
			report.Issues = []domain.SemanticReviewIssue{{Category: category, Severity: severity, TargetKey: "resource:scene", AnchorKeys: []string{"source"}, Explanation: "具体反例与固定设定不一致或无法判断"}}
		}
		if fixed, exists := p.byTarget[input.Target.Hash]; exists {
			report = fixed
		}
	}
	if p.transform != nil {
		p.transform(&report)
	}
	output, _ := json.Marshal(report)
	if p.malformed {
		output = []byte(`{"schema":1,"verdict":"PASS"}`)
	}
	body, _ := json.Marshal(map[string]any{"id": "semantic-fixture", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 40, "prompt_cache_hit_tokens": 0}})
	if p.after != nil {
		p.after()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
}

func semanticReviewFixture(t *testing.T, method ...string) (*fixture, service.Generation, service.QualifiedBinding, *semanticProtocolFixture, service.PriceCard) {
	t.Helper()
	f, source, _ := releaseGenerationFixture(t)
	ctx := context.Background()
	prices, err := f.svc.Generations.Prices(ctx, f.actor.ID)
	if err != nil || len(prices) == 0 {
		t.Fatal("missing fixture price", err)
	}
	price := prices[0]
	if _, err = (&service.Models{Workspace: f.svc.IP.Workspace}).Bind(ctx, f.actor.ID, domain.ModelBinding{Role: "critic", ConnectionID: price.ConnectionID, ConnectionRevision: price.ConnectionRevision}); err != nil {
		t.Fatal(err)
	}
	protocol := &semanticProtocolFixture{}
	f.svc.Generations.Adapter = models.New(protocol)
	suiteID, taskKind := "shared-semantic-review-v1", "shared.semantic-review"
	if len(method) > 0 {
		suiteID, taskKind = "shared-change-impact-v1", "shared.change-impact"
	}
	installed, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: suiteID, Kind: "agent", Role: "critic"})
	if err != nil {
		t.Fatal(err)
	}
	version := installed.BaselineID
	if len(method) > 0 {
		preset, installErr := (&service.Capabilities{IP: f.svc.IP}).InstallNarrative(ctx, f.actor, f.project, method[0])
		if installErr != nil {
			t.Fatal(installErr)
		}
		for _, ref := range preset.Versions {
			if ref.Kind == "agent" {
				version = ref.ID
			}
		}
	}
	q, err := f.svc.Create(ctx, f.actor, f.project, service.QualificationInput{OperationID: service.NewID(), VersionID: version, BaselineID: installed.BaselineID, BranchID: f.branch, SuiteID: suiteID, Role: "critic", PriceID: price.ID, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	q = drive(t, f, q.ID)
	if q.State != "PASSED" {
		t.Fatal("test semantic qualification failed", q.State, q.Reason)
	}
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: q.QualificationID, TaskKind: taskKind})
	if err != nil {
		t.Fatal(err)
	}
	return f, source, binding, protocol, price
}

func completeSemanticReview(t *testing.T, f *fixture, release domain.Release, binding service.QualifiedBinding, price service.PriceCard) service.SemanticReview {
	return completeTargetReview(t, f, service.SemanticReviewInput{TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: release.Manifest.BranchID}, binding, price)
}

func completeTargetReview(t *testing.T, f *fixture, input service.SemanticReviewInput, binding service.QualifiedBinding, price service.PriceCard) service.SemanticReview {
	return completeTargetReviewVerdict(t, f, input, binding, price, "PASS")
}

func completeTargetReviewVerdict(t *testing.T, f *fixture, input service.SemanticReviewInput, binding service.QualifiedBinding, price service.PriceCard, verdict string) service.SemanticReview {
	t.Helper()
	ctx := context.Background()
	svc := &service.SemanticReviews{Generations: f.svc.Generations}
	input.OperationID, input.ReviewerBindingID, input.PriceID, input.MaxOutputTokens = service.NewID(), binding.ID, price.ID, 1024
	r, err := svc.Prepare(ctx, f.actor, f.project, input)
	if err != nil || r.State != "PREPARED" || r.GenerationID == "" || r.EstimateMicros <= 0 || r.EstimateCurrency != price.Currency {
		t.Fatal("review not prepared", r, err)
	}
	if _, err = svc.Start(ctx, f.actor, f.project, r.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Advance(ctx, r.ID); e != nil || state != "RUNNING" {
		t.Fatal("review not running", state, e)
	}
	if _, err = f.svc.Generations.Execute(ctx, r.GenerationID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Advance(ctx, r.ID); e != nil || state != "COMPLETED" {
		t.Fatal("review not completed", state, e)
	}
	r, err = svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || r.Report == nil || r.Verdict != verdict {
		t.Fatal("report missing", r, err)
	}
	return r
}

func TestSemanticReviewDerivedContentHasIndependentAdoptPublishExit(t *testing.T) {
	f, source, binding, protocol, price := semanticReviewFixture(t)
	ctx := context.Background()
	release := prepareGenerationRelease(t, f, source, "人工修改后的新正文，与原生成文字不同。")
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("derived adopted before independent review", err)
	}
	before := protocol.calls.Load()
	review := completeSemanticReview(t, f, release, binding, price)
	if protocol.calls.Load() != before+1 || review.GenerationID == source.ID || len(review.Frozen.SourceGenerationIDs) != 1 || review.Frozen.SourceGenerationIDs[0] != source.ID {
		t.Fatal("not a single independent execution", review)
	}
	svc := &service.SemanticReviews{Generations: f.svc.Generations}
	selected, err := svc.Select(ctx, f.actor, f.project, review.ID, 0)
	if err != nil {
		t.Fatal("selection failed", err)
	}
	again, err := svc.Select(ctx, f.actor, f.project, review.ID, 0)
	if err != nil || selected.ID != again.ID {
		t.Fatal("selection replay duplicated", err)
	}
	budgets := &service.Budgets{Workspace: f.svc.IP.Workspace}
	currentBudget, err := budgets.Read(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentBudget.Enabled = false
	if _, err = budgets.Configure(ctx, f.actor.ID, currentBudget); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal("independent report did not release derived adoption", err)
	}
	companion := jointManualCompanion(t, f)
	group, err := f.svc.IP.PublishJoint(ctx, f.actor, f.project, jointInput(release, companion))
	if err != nil || len(group.Releases) != 2 {
		t.Fatal("reviewed derived content cannot publish", err)
	}
	var result domain.Release
	for _, r := range group.Releases {
		if r.ID == release.ID {
			result = r
		}
	}
	if result.Status != "PUBLISHED" {
		t.Fatal("derived content missing from group")
	}
	if !result.Manifest.Artifacts[0].Origin.ReviewRequired || result.Manifest.Artifacts[0].Origin.Generation.GenerationID != source.ID {
		t.Fatal("review erased immutable lineage")
	}
}
