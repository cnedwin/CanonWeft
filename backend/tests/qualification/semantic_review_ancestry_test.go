package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestSemanticReviewRejectsChangedMergeAndPromotionSourcesBeforeQuote(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	ip := f.svc.IP
	publish := func(branch, base, name string) service.Candidate {
		t.Helper()
		c, err := ip.Propose(ctx, f.actor, f.project, branch, base, service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: name}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ip.Adopt(ctx, f.actor, f.project, branch, c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = ip.Publish(ctx, f.actor, f.project, branch, c.ID); err != nil {
			t.Fatal(err)
		}
		return c
	}
	base := publish(f.branch, "", "初始")
	child, err := ip.CreateBranch(ctx, f.actor, f.project, "adaptation", f.branch, base.ID, service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	upstream := publish(f.branch, base.ID, "上游新名")
	merge := service.MergeInput{BaseHead: base.ID, UpstreamHead: upstream.ID, OperationID: service.NewID(), ChangeReasons: map[string]string{"entity:hero:/name": "采用明确上游变更"}}
	candidate, err := ip.ProposeMerge(ctx, f.actor, f.project, child.ID, merge)
	if err != nil {
		t.Fatal(err)
	}
	latest := publish(f.branch, upstream.ID, "上游再次修改")
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: ip}}
	if _, err = svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: candidate.ID, TargetHash: candidate.Hash, BranchID: child.ID, MaxOutputTokens: 1024}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale merge source quoted", err)
	}
	local := publish(child.ID, base.ID, "改编新名")
	promotion, err := ip.ProposePromotion(ctx, f.actor, f.project, f.branch, service.PromotionInput{SourceBranch: child.ID, SourceHead: local.ID, BaseHead: latest.ID, OperationID: service.NewID(), Reason: "提升经过选择的人物变更", Selection: domain.PromotionSelection{Entities: []string{"hero"}}, ChangeReasons: map[string]string{"entity:hero:/name": "采用改编中确认的人物名"}})
	if err != nil {
		t.Fatal(err)
	}
	publish(child.ID, local.ID, "改编再次修改")
	if _, err = svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: promotion.ID, TargetHash: promotion.Hash, BranchID: f.branch, MaxOutputTokens: 1024}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale promotion source quoted", err)
	}
	var calls int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&calls); err != nil || calls != 0 {
		t.Fatal("stale ancestry charged", calls, err)
	}
}
