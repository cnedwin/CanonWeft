package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

func TestChangeInspectionQualifiedBatchPreservesScopeAndRejectsFalseCoverage(t *testing.T) {
	f, _, binding, protocol, price := semanticReviewFixture(t, "change-impact-review")
	ctx := context.Background()
	ip, svc := f.svc.IP, f.svc.Generations
	ids := []string{}
	updates := []domain.ResourceUpdate{}
	for _, text := range []string{"他仍然使用银针治病。", "本批未选的独立商队故事。"} {
		r, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "待查场景", Kind: "note", OperationID: service.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		updates = append(updates, domain.ResourceUpdate{ResourceID: r.ID, Text: text})
	}
	released, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{OperationID: service.NewID(), BranchID: f.branch, Updates: updates})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, released.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, released.ID); err != nil {
		t.Fatal(err)
	}
	candidate, err := ip.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林舟", Description: "已失去医术"}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ip.CreateChangePlan(ctx, f.actor, f.project, service.ChangePlanInput{BranchID: f.branch, CandidateID: candidate.ID, OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	scopes := []domain.ChangeReviewScope{}
	for _, entry := range plan.GraphSnapshot.Entries {
		if entry.Ref.Kind == "resource" {
			scopes = append(scopes, domain.ChangeReviewScope{Node: entry.Ref, Reason: "核查潜在影响，不依赖已有依赖边"})
		}
	}
	plan, err = ip.ExtendChangeScope(ctx, f.actor, f.project, plan.ID, service.ChangeScopeInput{OperationID: service.NewID(), Targets: scopes})
	if err != nil {
		t.Fatal(err)
	}
	installQualificationSourceFixture(t, f)
	semanticSources(t, f)
	before := protocol.calls.Load()
	input := service.ChangeInspectionInput{BranchID: f.branch, ResourceIDs: ids[:1], BindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024, OperationID: service.NewID()}
	invalid := input
	invalid.BindingID = ""
	if _, err = svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, invalid); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("unqualified reviewer accepted", err)
	}
	invalid = input
	invalid.ResourceIDs = []string{service.NewID()}
	if _, err = svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, invalid); err == nil {
		t.Fatal("unknown scope accepted")
	}
	g, err := svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, input)
	if err != nil || replay.ID != g.ID || protocol.calls.Load() != before {
		t.Fatal("prepare charged or duplicated", err)
	}
	coverage, err := svc.ChangeInspectionCoverage(ctx, f.actor, f.project, plan.ID, f.branch)
	if err != nil || coverage.Checked != 0 || coverage.Pending != 1 || len(coverage.NextBatch) != 1 || coverage.NextBatch[0] != ids[1] || protocol.calls.Load() != before {
		t.Fatal("prepared batch repeated or charged by coverage", coverage, err)
	}
	invalid = input
	invalid.ResourceIDs = ids[1:]
	if _, err = svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, invalid); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("replay retargeted", err)
	}
	raw, err := ip.Blobs.ReadObject(ctx, f.project, g.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var request models.Request
	if json.Unmarshal(raw, &request) != nil {
		t.Fatal("bad frozen request")
	}
	source, doc, err := domain.ParseChangeInspectionInput(request.Prompt)
	if err != nil || len(source.Targets) != 1 || source.Targets[0].Ref.ID != ids[0] || len(doc.Artifacts) != 1 || strings.Contains(request.Prompt, updates[1].Text) {
		t.Fatal("unselected body entered batch", err)
	}
	run := func(input service.ChangeInspectionInput) service.ChangeInspection {
		t.Helper()
		task, e := svc.PrepareChangeInspection(ctx, f.actor, f.project, plan.ID, input)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Start(ctx, f.actor, f.project, task.ID); e != nil {
			t.Fatal(e)
		}
		if state, e := svc.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
			t.Fatal(state, e)
		}
		result, e := svc.ReadChangeInspection(ctx, f.actor, f.project, task.ID)
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	result := run(input)
	if !result.Current || !result.Counted || len(result.Items) != 1 || result.Items[0].State != "CHECKED_NO_ISSUE_REPORTED" || protocol.calls.Load() != before+1 {
		t.Fatal("missing actual qualified report", result)
	}
	protocol.transform = func(report *domain.SemanticReviewReport) { report.Coverage = report.Coverage[1:] }
	invalid = input
	invalid.OperationID, invalid.ResourceIDs = service.NewID(), ids[1:]
	malformed := run(invalid)
	if malformed.Counted || malformed.Report != nil {
		t.Fatal("partial coverage accepted", malformed)
	}
	protocol.transform = func(report *domain.SemanticReviewReport) {
		report.Verdict = "UNKNOWN"
		report.Issues = []domain.SemanticReviewIssue{{Category: "unsupported", Severity: "unknown", TargetKey: "resource:" + ids[1], AnchorKeys: []string{"change", "resource:" + ids[1]}, Explanation: "缺少这段故事的时间依据"}}
	}
	invalid.OperationID = service.NewID()
	uncertain := run(invalid)
	if !uncertain.Counted || uncertain.Items[0].State != "UNCERTAIN" {
		t.Fatal("uncertainty hidden", uncertain)
	}
	coverage, err = svc.ChangeInspectionCoverage(ctx, f.actor, f.project, plan.ID, f.branch)
	if err != nil || coverage.Checked != 2 || coverage.Uncertain != 1 || !coverage.AllListedChecked || len(coverage.NextBatch) != 0 || len(coverage.Items) != 2 {
		t.Fatal("cross-batch coverage lost uncertainty", coverage, err)
	}
	page, err := svc.ChangeInspections(ctx, f.actor, f.project, plan.ID, f.branch, "")
	if err != nil || len(page.Items) != 3 || page.Next != "" {
		t.Fatal("inspection list lost runs", page, err)
	}
	older, err := svc.ChangeInspections(ctx, f.actor, f.project, plan.ID, f.branch, page.Items[0].ID)
	if err != nil || len(older.Items) != 2 || older.Items[0].ID == page.Items[0].ID {
		t.Fatal("cursor repeated inspected run", older, err)
	}
	if _, err = svc.ChangeInspections(ctx, f.actor, f.project, plan.ID, f.branch, "bad-cursor"); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("bad cursor accepted", err)
	}
	semanticSources(t, f)
	branch, err := ip.ReadBranch(ctx, f.actor, f.project, f.branch)
	if err != nil || branch.Head != "" {
		t.Fatal("inspection published candidate", err)
	}
	if err = f.svc.DisableBinding(ctx, f.actor, f.project, binding.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := svc.ReadChangeInspection(ctx, f.actor, f.project, g.ID)
	if err != nil || stale.Current || stale.Counted {
		t.Fatal("revoked qualification counted", stale, err)
	}
	coverage, err = svc.ChangeInspectionCoverage(ctx, f.actor, f.project, plan.ID, f.branch)
	if err != nil || coverage.Checked != 0 || coverage.NeedsAttention != 2 || coverage.AllListedChecked || len(coverage.NextBatch) != 0 {
		t.Fatal("revoked attempts counted or automatically retried", coverage, err)
	}
	if protocol.calls.Load() != before+3 {
		t.Fatal("read/revoke made extra model call")
	}
	// A long history must be scanned beyond the 100-row database chunk. These
	// cancelled test rows reuse frozen evidence; they are never executable calls.
	var lastID string
	for range 101 {
		lastID = service.NewID()
		_, err = f.db.ExecContext(ctx, `INSERT INTO generation_tasks
		(id,workspace_id,project_id,branch_id,actor_id,operation_id,request_hash,input_hash,frozen,credential_ref,state,cancel_requested)
		SELECT ?,workspace_id,project_id,branch_id,actor_id,?,request_hash,input_hash,frozen,credential_ref,'CANCELLED',true FROM generation_tasks WHERE id=?`, lastID, service.NewID(), g.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	coverage, err = svc.ChangeInspectionCoverage(ctx, f.actor, f.project, plan.ID, f.branch)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	for _, item := range coverage.Items {
		attempts += len(item.Runs)
	}
	if attempts != 104 || coverage.Checked != 0 {
		t.Fatal("coverage truncated historical attempts", attempts, coverage)
	}
	_, err = f.db.ExecContext(ctx, "UPDATE generation_tasks SET frozen=JSON_SET(frozen,'$.input_hash',?) WHERE id=?", strings.Repeat("0", 64), lastID)
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "GET", "/changes/"+plan.ID+"/inspection-coverage?branch="+f.branch, nil, 200)
	if response["unreadable_runs"] != float64(1) || response["automatic_ready"] != false || response["all_listed_checked"] != false {
		t.Fatal("HTTP hid unavailable scope", response)
	}
}
