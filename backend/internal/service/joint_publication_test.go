package service

import (
	"errors"
	"novelstudio/internal/domain"
	"testing"
)

func TestJointPublicationRejectsImplicitCrossMemberRetargeting(t *testing.T) {
	releases := []domain.Release{
		{ID: "novel", Manifest: domain.Manifest{BranchID: "novel", IPCandidateID: "next", Artifacts: []domain.ArtifactRef{{ResourceID: "source"}}}},
		{ID: "comic", Manifest: domain.Manifest{BranchID: "comic", Artifacts: []domain.ArtifactRef{{ResourceID: "target", Dependencies: &domain.DependencyPolicy{Inputs: []domain.Dependency{{Source: domain.DependencyRef{Kind: "resource", ID: "source", BranchID: "novel"}}}}}}}},
	}
	if err := validateJointReferences(releases); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old following reference accepted", err)
	}
	releases[1].Manifest.Artifacts[0].Dependencies.Inputs[0].Pinned = true
	if err := validateJointReferences(releases); err != nil {
		t.Fatal("explicit historical reference rejected", err)
	}
	releases[1].Manifest.Artifacts[0].Dependencies.Inputs[0] = domain.Dependency{Source: domain.DependencyRef{Kind: "entity", ID: "hero", BranchID: "novel"}}
	if err := validateJointReferences(releases); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old IP reference accepted", err)
	}
}
