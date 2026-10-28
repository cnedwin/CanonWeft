package service

import (
	"errors"
	"novelstudio/internal/domain"
	"testing"
)

func TestPeerJointReferenceRequiresExactDeclarationAndNodeHash(t *testing.T) {
	upstream := domain.Release{ID: "novel-release", Manifest: domain.Manifest{BranchID: "novel", Artifacts: []domain.ArtifactRef{{ResourceID: "chapter", Hash: "body-hash"}}}}
	source := domain.DependencyRef{Kind: "resource", BranchID: "novel", ID: "chapter", Revision: upstream.ID, Hash: resourceNodeHash("body-hash", nil, nil)}
	downstream := domain.Release{ID: "comic-release", Manifest: domain.Manifest{BranchID: "comic", PeerReleases: []string{upstream.ID}, Artifacts: []domain.ArtifactRef{{ResourceID: "scene", Dependencies: &domain.DependencyPolicy{Inputs: []domain.Dependency{{Source: source}}}}}}}
	if err := validateJointReferences([]domain.Release{upstream, downstream}); err != nil {
		t.Fatal(err)
	}
	downstream.Manifest.Artifacts[0].Dependencies.Inputs[0].Source.Hash = "wrong"
	if err := validateJointReferences([]domain.Release{upstream, downstream}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong peer hash accepted", err)
	}
	downstream.Manifest.Artifacts[0].Dependencies.Inputs[0].Source = source
	downstream.Manifest.PeerReleases = nil
	if err := validateJointReferences([]domain.Release{upstream, downstream}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("implicit new reference accepted", err)
	}
}
