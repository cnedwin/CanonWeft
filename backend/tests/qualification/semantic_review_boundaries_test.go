package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestSemanticReviewCancellationUnknownInvalidOutputAndRevocation(t *testing.T) {
	f, source, binding, protocol, price := semanticReviewFixture(t)
	ctx := context.Background()
	svc := &service.SemanticReviews{Generations: f.svc.Generations}
	prepare := func(text string) (domain.Release, service.SemanticReview) {
		release := prepareGenerationRelease(t, f, source, text)
		r, err := svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "artifact_release", TargetID: release.ID, TargetHash: release.Hash, BranchID: f.branch, ReviewerBindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024})
		if err != nil || r.State != "PREPARED" {
			t.Fatal("prepare", r.State, r.Reason, err)
		}
		if _, err = svc.Start(ctx, f.actor, f.project, r.ID); err != nil {
			t.Fatal(err)
		}
		if state, e := svc.Advance(ctx, r.ID); e != nil || state != "RUNNING" {
			t.Fatal("start", state, e)
		}
		return release, r
	}
	t.Run("pause-before-dispatch-preserves-reservation", func(t *testing.T) {
		_, r := prepare("暂停并继续同一次独立审查")
		if _, err := svc.Control(ctx, f.actor, f.project, r.ID, "pause"); err != nil {
			t.Fatal(err)
		}
		calls := protocol.calls.Load()
		if state, err := f.svc.Generations.Execute(ctx, r.GenerationID); err != nil || state != "PAUSED" {
			t.Fatal("paused child dispatched or failed", state, err)
		}
		if protocol.calls.Load() != calls {
			t.Fatal("paused review sent a request")
		}
		var callState string
		if err := f.db.QueryRow("SELECT c.state FROM model_calls c JOIN generation_tasks g ON g.call_id=c.id WHERE g.id=?", r.GenerationID).Scan(&callState); err != nil || callState != "RESERVED" {
			t.Fatal("pause changed reservation", callState, err)
		}
		if _, err := svc.Control(ctx, f.actor, f.project, r.ID, "resume"); err != nil {
			t.Fatal(err)
		}
		if state, err := f.svc.Generations.Execute(ctx, r.GenerationID); err != nil || state != "SUCCEEDED" {
			t.Fatal("resume", state, err)
		}
		if state, err := svc.Advance(ctx, r.ID); err != nil || state != "COMPLETED" {
			t.Fatal("resume report", state, err)
		}
	})
	t.Run("cancel-late-output", func(t *testing.T) {
		release, r := prepare("取消后不得提交的修改")
		protocol.after = func() {
			protocol.after = nil
			if _, err := svc.Control(ctx, f.actor, f.project, r.ID, "cancel"); err != nil {
				t.Fatal(err)
			}
		}
		if state, err := f.svc.Generations.Execute(ctx, r.GenerationID); err != nil || state != "CANCELLED" {
			t.Fatal("late result not cancelled", state, err)
		}
		if state, err := svc.Advance(ctx, r.ID); err != nil || state != "CANCELLED" {
			t.Fatal(state, err)
		}
		read, err := svc.Read(ctx, f.actor, f.project, r.ID)
		if err != nil || read.ReportHash != "" || read.Verdict != "PENDING" {
			t.Fatal("late report authority", read, err)
		}
		if _, err = svc.Select(ctx, f.actor, f.project, r.ID, 0); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("cancelled selection", err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("cancelled source adopted", err)
		}
	})
	t.Run("unknown-never-resends", func(t *testing.T) {
		_, r := prepare("未知请求不重发")
		protocol.lost = true
		if state, err := f.svc.Generations.Execute(ctx, r.GenerationID); err != nil || state != "UNKNOWN" {
			t.Fatal(state, err)
		}
		protocol.lost = false
		calls := protocol.calls.Load()
		for i := 0; i < 3; i++ {
			if state, err := svc.Advance(ctx, r.ID); err != nil || state != "UNKNOWN" {
				t.Fatal(state, err)
			}
		}
		if protocol.calls.Load() != calls {
			t.Fatal("unknown request resent")
		}
		if _, err := svc.Start(ctx, f.actor, f.project, r.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("unknown request restarted", err)
		}
		var reserved int64
		if err := f.db.QueryRow("SELECT c.reserved_micros FROM model_calls c JOIN generation_tasks g ON g.call_id=c.id WHERE g.id=?", r.GenerationID).Scan(&reserved); err != nil || reserved <= 0 {
			t.Fatal("unknown hold erased", reserved, err)
		}
	})
	t.Run("fabricated-coverage", func(t *testing.T) {
		release, r := prepare("报告不能省略固定对象")
		protocol.transform = func(report *domain.SemanticReviewReport) { report.Coverage = []string{} }
		if _, err := f.svc.Generations.Execute(ctx, r.GenerationID); err != nil {
			t.Fatal(err)
		}
		protocol.transform = nil
		if state, err := svc.Advance(ctx, r.ID); err != nil || state != "FAILED" {
			t.Fatal("incomplete report accepted", state, err)
		}
		if _, err := svc.Start(ctx, f.actor, f.project, r.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("terminal failed review restarted", err)
		}
		if read, err := svc.Read(ctx, f.actor, f.project, r.ID); err != nil || read.State != "FAILED" {
			t.Fatal("failed review state changed", read.State, err)
		}
		if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("incomplete report adopted", err)
		}
	})
	t.Run("optional-manual-review-cannot-ignore-known-unknown", func(t *testing.T) {
		candidate, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "待确认的角色"}}})
		if err != nil {
			t.Fatal(err)
		}
		input := service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: candidate.ID, TargetHash: candidate.Hash, BranchID: f.branch, ReviewerBindingID: binding.ID, PriceID: price.ID, MaxOutputTokens: 1024}
		r, err := svc.Prepare(ctx, f.actor, f.project, input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Start(ctx, f.actor, f.project, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Advance(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		protocol.transform = func(report *domain.SemanticReviewReport) {
			report.Verdict = "UNKNOWN"
			report.Issues = []domain.SemanticReviewIssue{{Category: "unsupported", Severity: "unknown", TargetKey: "entity:hero", AnchorKeys: []string{"entity:hero"}, Explanation: "角色来源仍不明确"}}
		}
		if _, err = f.svc.Generations.Execute(ctx, r.GenerationID); err != nil {
			t.Fatal(err)
		}
		protocol.transform = nil
		if state, e := svc.Advance(ctx, r.ID); e != nil || state != "COMPLETED" {
			t.Fatal(state, e)
		}
		if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, candidate.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("known UNKNOWN bypassed by manual satisfaction", err)
		}
		// A new content revision is independent and remains manually editable.
		newContent, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "来源已补全的新版本"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, newContent.ID); err != nil {
			t.Fatal("old unknown locked new content", err)
		}
		pass := completeTargetReview(t, f, input, binding, price)
		if _, err = svc.Select(ctx, f.actor, f.project, pass.ID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, candidate.ID); err != nil {
			t.Fatal("new independent pass did not resolve unknown", err)
		}
	})
	t.Run("revoked-after-adoption", func(t *testing.T) {
		release := prepareGenerationRelease(t, f, source, "已审但发布前资格被停用")
		r := completeSemanticReview(t, f, release, binding, price)
		if _, err := svc.Select(ctx, f.actor, f.project, r.ID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DisableBinding(ctx, f.actor, f.project, binding.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.IP.PublishRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("revoked review published", err)
		}
		read, err := svc.Read(ctx, f.actor, f.project, r.ID)
		if err != nil || read.Report == nil || read.SelectionReady {
			t.Fatal("revoked report history or authority wrong", read, err)
		}
		var head string
		if err = f.db.QueryRow("SELECT head_revision FROM resources WHERE id=?", release.Manifest.Artifacts[0].ResourceID).Scan(&head); err != nil || head != "" {
			t.Fatal("blocked publish changed resource", head, err)
		}
	})
}
