package qualification_test

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func jointManualCompanion(t *testing.T, f *fixture) domain.Release {
	t.Helper()
	ctx := context.Background()
	ip := f.svc.IP
	branch, err := ip.CreateBranch(ctx, f.actor, f.project, "改编发布分支", "", "", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	resource, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: branch.ID, Name: "人工改编资料", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	release, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: branch.ID, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: resource.ID, Text: "明确由作者确认的独立改编内容。"}}})
	if err != nil {
		t.Fatal(err)
	}
	release, err = ip.AdoptRelease(ctx, f.actor, f.project, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func jointInput(a, b domain.Release) service.JointPublicationInput {
	return service.JointPublicationInput{OperationID: service.NewID(), Releases: []service.JointReleasePin{{ReleaseID: a.ID, ManifestHash: a.Hash}, {ReleaseID: b.ID, ManifestHash: b.Hash}}}
}
