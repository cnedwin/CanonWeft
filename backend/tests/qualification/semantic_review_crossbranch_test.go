package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestSemanticReviewMergePromotionAndJointPublicationHaveActualExit(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	manual := func(branch, base, name string) service.Candidate {
		t.Helper()
		c, err := ip.ProposeChanges(ctx, f.actor, f.project, branch, base, service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: name}}}, func() map[string]string {
			if base == "" {
				return map[string]string{"entity:hero:": "建立人物"}
			}
			return map[string]string{"entity:hero:/name": "明确当前分支的人物名"}
		}())
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
	approve := func(branch string, c service.Candidate) {
		t.Helper()
		if _, err := ip.Adopt(ctx, f.actor, f.project, branch, c.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("cross-branch bypassed review", err)
		}
		r := completeTargetReview(t, f, service.SemanticReviewInput{TargetKind: "ip_candidate", TargetID: c.ID, TargetHash: c.Hash, BranchID: branch}, binding, price)
		if _, err := reviews.Select(ctx, f.actor, f.project, r.ID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := ip.Adopt(ctx, f.actor, f.project, branch, c.ID); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := ip.Publish(ctx, f.actor, f.project, branch, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	base := manual(f.branch, "", "原名")
	child, err := ip.CreateBranch(ctx, f.actor, f.project, "改编", f.branch, base.ID, service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	upstream := manual(f.branch, base.ID, "上游新名")
	merged, err := ip.ProposeMerge(ctx, f.actor, f.project, child.ID, service.MergeInput{BaseHead: base.ID, UpstreamHead: upstream.ID, OperationID: service.NewID(), ChangeReasons: map[string]string{"entity:hero:/name": "同步上游确认的人物名"}})
	if err != nil {
		t.Fatal(err)
	}
	approve(child.ID, merged)
	state, err := ip.ReadBranch(ctx, f.actor, f.project, child.ID)
	if err != nil || state.BaseRevision != upstream.ID {
		t.Fatal("merge baseline not advanced", state, err)
	}
	local := manual(child.ID, merged.ID, "改编新名")
	promoted, err := ip.ProposePromotion(ctx, f.actor, f.project, f.branch, service.PromotionInput{BaseHead: upstream.ID, SourceBranch: child.ID, SourceHead: local.ID, OperationID: service.NewID(), Reason: "提升明确设定", Selection: domain.PromotionSelection{Entities: []string{"hero"}}, ChangeReasons: map[string]string{"entity:hero:/name": "采用改编中确认的名称"}})
	if err != nil {
		t.Fatal(err)
	}
	approve(f.branch, promoted)
	candidate, err := ip.ProposeChanges(ctx, f.actor, f.project, f.branch, promoted.ID, service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "联合新名"}}}, map[string]string{"entity:hero:/name": "设定与场景共同确认人物名"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.Adopt(ctx, f.actor, f.project, f.branch, candidate.ID); err != nil {
		t.Fatal(err)
	}
	resource, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Kind: "note", Name: "对应场景", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Kind: "note", Name: "场景后续", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	dependency := func(kind, id, version string) *domain.DependencyPolicy {
		return &domain.DependencyPolicy{Inputs: []domain.Dependency{{Kind: "evidence", Reason: "明确依赖同批已审查内容", Source: domain.DependencyRef{Kind: kind, BranchID: f.branch, ID: id, Revision: version}}}}
	}
	input := domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: promoted.ID, IPCandidateID: candidate.ID, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{
		{ResourceID: downstream.ID, Text: "后续记录沿用同批场景。", Dependencies: dependency("resource", resource.ID, "$release")},
		{ResourceID: resource.ID, Text: "联合新名走进城内。", Dependencies: dependency("entity", "hero", "$candidate")},
	}}
	release, err := ip.PrepareRelease(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range release.Manifest.Artifacts {
		source := artifact.Dependencies.Inputs[0].Source
		want := candidate.ID
		if artifact.ResourceID == downstream.ID {
			want = release.ID
		}
		if source.Revision != want || len(source.Hash) != 64 {
			t.Fatal("prospective reference not frozen", source)
		}
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("joint release bypassed review", err)
	}
	report := completeSemanticReview(t, f, release, binding, price)
	if _, err = reviews.Select(ctx, f.actor, f.project, report.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = ip.PublishRelease(ctx, f.actor, f.project, release.ID); err != nil {
			t.Fatal(err)
		}
	}
	if retry, err := ip.PrepareRelease(ctx, f.actor, f.project, input); err != nil || retry.ID != release.ID {
		t.Fatal("published prospective request retry was not idempotent", retry.ID, err)
	}
	state, err = ip.ReadBranch(ctx, f.actor, f.project, f.branch)
	if err != nil || state.Head != candidate.ID {
		t.Fatal("joint IP not published", err)
	}
	current, err := ip.ReadResource(ctx, f.actor, f.project, resource.ID)
	if err != nil || current.Head != release.ID {
		t.Fatal("joint text not published", err)
	}
	currentDownstream, err := ip.ReadResource(ctx, f.actor, f.project, downstream.ID)
	if err != nil || currentDownstream.Head != release.ID {
		t.Fatal("joint downstream not published", err)
	}
	cycle := domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: candidate.ID, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{
		{ResourceID: resource.ID, ExpectedHead: release.ID, Text: "甲", Dependencies: dependency("resource", downstream.ID, "$release")},
		{ResourceID: downstream.ID, ExpectedHead: release.ID, Text: "乙", Dependencies: dependency("resource", resource.ID, "$release")},
	}}
	if _, err = ip.PrepareRelease(ctx, f.actor, f.project, cycle); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("cyclic prospective versions accepted", err)
	}
	var events int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='release.published' AND resource_id=?", release.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("joint retry duplicated event", events, err)
	}
}
