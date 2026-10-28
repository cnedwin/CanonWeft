package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestPeerReleaseReviewIncludesUpstreamAndPublishesOnlyWithDependencies(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	upstream := jointManualCompanion(t, f)
	sources, err := ip.PeerReleaseSources(ctx, f.actor, f.project, upstream.ID)
	if err != nil || len(sources.Entries) != 1 {
		t.Fatal("peer catalog", err)
	}
	target, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "依赖新上游的改编", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ReleaseInput{BranchID: f.branch, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: target.ID, Text: "改编保留上游设定与主旨。", Dependencies: &domain.DependencyPolicy{Inputs: []domain.Dependency{{Source: sources.Entries[0].Ref, Kind: "semantic", Reason: "明确引用这份尚未发布的新版本"}}}}}}
	if _, err = ip.PrepareRelease(ctx, f.actor, f.project, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("undeclared unpublished reference accepted", err)
	}
	input.PeerReleases = []domain.ReleasePin{{ReleaseID: upstream.ID, ManifestHash: strings.Repeat("0", 64)}}
	if _, err = ip.PrepareRelease(ctx, f.actor, f.project, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong peer pin accepted", err)
	}
	input.PeerReleases[0].ManifestHash = upstream.Hash
	release, err := ip.PrepareRelease(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal("declared reference rejected", err)
	}
	if len(release.Manifest.PeerReleases) != 1 || release.Manifest.PeerReleases[0] != upstream.ID {
		t.Fatal("peer declaration lost")
	}
	if replay, err := ip.PrepareRelease(ctx, f.actor, f.project, input); err != nil || replay.ID != release.ID {
		t.Fatal("candidate replay", err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("peer candidate bypassed review", err)
	}
	review := completeSemanticReview(t, f, release, binding, price)
	raw, err := ip.Blobs.ReadObject(ctx, f.project, review.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var document domain.SemanticReviewDocument
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range document.Artifacts {
		if artifact.Target.ReleaseID == upstream.ID && artifact.Text == "明确由作者确认的独立改编内容。" {
			found = true
		}
	}
	if !found {
		t.Fatal("review omitted upstream actual text", string(raw))
	}
	for _, key := range document.RequiredCoverage {
		if !strings.HasPrefix(key, "peer:") {
			continue
		}
		incomplete := *review.Report
		incomplete.Coverage = []string{}
		for _, covered := range review.Report.Coverage {
			if covered != key {
				incomplete.Coverage = append(incomplete.Coverage, covered)
			}
		}
		if incomplete.Validate(document) == nil {
			t.Fatal("upstream omission still passes")
		}
		break
	}
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	if _, err = reviews.Select(ctx, f.actor, f.project, review.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("published without upstream", err)
	}
	wrongBranch, err := ip.CreateBranch(ctx, f.actor, f.project, "无关改编分支", "", "", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	wrongResource, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: wrongBranch.ID, Name: "其他内容", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: wrongBranch.ID, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: wrongResource.ID, Text: "这不是被引用的上游。"}}})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err = ip.AdoptRelease(ctx, f.actor, f.project, wrong.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishJoint(ctx, f.actor, f.project, jointInput(release, wrong)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong group accepted", err)
	}
	for _, member := range []domain.Release{release, upstream, wrong} {
		for _, artifact := range member.Manifest.Artifacts {
			resource, err := ip.ReadResource(ctx, f.actor, f.project, artifact.ResourceID)
			if err != nil || resource.Head != "" {
				t.Fatal("failed group partially published", err)
			}
		}
	}
	publication := jointInput(release, upstream)
	group, err := ip.PublishJoint(ctx, f.actor, f.project, publication)
	if err != nil || len(group.Releases) != 2 {
		t.Fatal("valid peer group", err)
	}
	if replay, err := ip.PublishJoint(ctx, f.actor, f.project, publication); err != nil || replay.ID != group.ID {
		t.Fatal("publication replay", err)
	}
	if replay, err := ip.PrepareRelease(ctx, f.actor, f.project, input); err != nil || replay.ID != release.ID {
		t.Fatal("saved input replay after publication", err)
	}
	for _, member := range group.Releases {
		for _, artifact := range member.Manifest.Artifacts {
			resource, err := ip.ReadResource(ctx, f.actor, f.project, artifact.ResourceID)
			if err != nil || resource.Head != member.ID {
				t.Fatal("peer version not published", err)
			}
		}
	}
}
