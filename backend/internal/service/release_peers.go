package service

import (
	"context"
	"database/sql"
	"fmt"
	"novelstudio/internal/domain"
	"slices"
	"sort"
)

func peerOwnsReference(peer domain.Release, ref domain.DependencyRef) bool {
	if peer.Manifest.BranchID != ref.BranchID {
		return false
	}
	if ref.Kind == "resource" {
		if peer.ID != ref.Revision {
			return false
		}
		for _, artifact := range peer.Manifest.Artifacts {
			if artifact.ResourceID == ref.ID && resourceNodeHash(artifact.Hash, artifact.Policy, artifact.Dependencies) == ref.Hash {
				return true
			}
		}
		return false
	}
	return ref.Kind != "skill" && peer.Manifest.IPCandidateID != "" && peer.Manifest.IPCandidateID == ref.Revision
}

func inheritPeerDeclarationsTx(ctx context.Context, tx *sql.Tx, project string, manifest *domain.Manifest) error {
	parents := append(slices.Clone(manifest.PatchParents), manifest.CompositionParents...)
	for _, artifact := range manifest.Artifacts {
		if artifact.Origin != nil && artifact.Origin.ParentReleaseID != "" {
			parents = append(parents, artifact.Origin.ParentReleaseID)
		}
	}
	seen := map[string]bool{}
	for _, id := range parents {
		if seen[id] {
			continue
		}
		seen[id] = true
		parent, err := releaseTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		for _, peerID := range parent.Manifest.PeerReleases {
			if slices.Contains(manifest.PeerReleases, peerID) {
				continue
			}
			peer, err := releaseTx(ctx, tx, project, peerID)
			if err != nil {
				return err
			}
			used := false
			for _, artifact := range manifest.Artifacts {
				if artifact.Dependencies == nil {
					continue
				}
				for _, dependency := range artifact.Dependencies.Inputs {
					used = used || peerOwnsReference(peer, dependency.Source)
				}
			}
			if used {
				manifest.PeerReleases = append(manifest.PeerReleases, peerID)
			}
		}
	}
	slices.Sort(manifest.PeerReleases)
	return nil
}

// Peer IDs reference saved immutable candidates, never promises of future work.
// A bounded DAG lets review and publication see every unpublished prerequisite.
func releasePeersTx(ctx context.Context, tx *sql.Tx, project string, root domain.Manifest) ([]domain.Release, error) {
	result := []domain.Release{}
	seen, visiting := map[string]bool{}, map[string]bool{root.ReleaseID: true}
	var walk func(domain.Manifest, int) error
	walk = func(parent domain.Manifest, depth int) error {
		if depth > 16 || len(parent.PeerReleases) > 15 {
			return domain.ErrValidation
		}
		local := map[string]bool{}
		for _, id := range parent.PeerReleases {
			if id == "" || local[id] || visiting[id] {
				return domain.ErrValidation
			}
			local[id] = true
			peer, err := releaseTx(ctx, tx, project, id)
			if err != nil {
				return err
			}
			if peer.Manifest.BranchID == parent.BranchID || !slices.Contains([]string{"PREPARED", "READY", "PUBLISHED"}, peer.Status) {
				return domain.ErrConflict
			}
			used := false
			for _, artifact := range parent.Artifacts {
				if artifact.Dependencies == nil {
					continue
				}
				for _, dependency := range artifact.Dependencies.Inputs {
					used = used || peerOwnsReference(peer, dependency.Source)
				}
			}
			if !used {
				return fmt.Errorf("%w：上游候选必须有明确的内容引用", domain.ErrValidation)
			}
			if !seen[id] {
				if len(seen) >= 128 {
					return domain.ErrValidation
				}
				visiting[id] = true
				if err = walk(peer.Manifest, depth+1); err != nil {
					return err
				}
				delete(visiting, id)
				seen[id] = true
				result = append(result, peer)
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return result, nil
}

func validatePeerDependencyTx(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest, dependency domain.Dependency) (bool, error) {
	for _, id := range manifest.PeerReleases {
		peer, err := releaseTx(ctx, tx, project, id)
		if err != nil {
			return false, err
		}
		if !peerOwnsReference(peer, dependency.Source) {
			continue
		}
		if peer.Status == "PUBLISHED" {
			return true, validateDependencySource(ctx, tx, project, dependency)
		}
		branch, err := branchTx(ctx, tx, project, peer.Manifest.BranchID)
		if err != nil {
			return true, err
		}
		if branch.Head != peer.Manifest.ExpectedIPHead {
			return true, domain.ErrConflict
		}
		if dependency.Source.Kind == "resource" {
			resource, err := resourceTx(ctx, tx, project, dependency.Source.ID)
			if err != nil {
				return true, err
			}
			for _, artifact := range peer.Manifest.Artifacts {
				if artifact.ResourceID == resource.ID && resource.BranchID == branch.ID && resource.Head == artifact.ExpectedHead {
					return true, nil
				}
			}
			return true, domain.ErrConflict
		}
		candidate, err := candidateTx(ctx, tx, branch.ID, peer.Manifest.IPCandidateID)
		if err != nil {
			return true, err
		}
		if candidate.BaseHead != branch.Head || candidate.Status == "PUBLISHED" {
			return true, domain.ErrConflict
		}
		for _, entry := range snapshotEntries(branch.ID, candidate.ID, branch.Name, candidate.Snapshot) {
			if entry.Ref == dependency.Source {
				return true, nil
			}
		}
		return true, domain.ErrConflict
	}
	return false, nil
}

type PeerReleaseSources struct {
	Release  domain.Release    `json:"release"`
	Entries  []DependencyEntry `json:"entries"`
	Snapshot domain.Snapshot   `json:"snapshot"`
}

func (s *IP) PeerReleaseSources(ctx context.Context, a domain.Actor, project, id string) (PeerReleaseSources, error) {
	result := PeerReleaseSources{Entries: []DependencyEntry{}}
	release, err := s.ReadRelease(ctx, a, project, id)
	if err != nil {
		return result, err
	}
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	branch, err := branchTx(ctx, tx, project, release.Manifest.BranchID)
	if err != nil {
		return result, err
	}
	result.Release = release
	result.Snapshot, err = snapshotTx(ctx, tx, project, release.Manifest.ExpectedIPHead)
	if err != nil {
		return result, err
	}
	for _, artifact := range release.Manifest.Artifacts {
		resource, err := resourceTx(ctx, tx, project, artifact.ResourceID)
		if err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, DependencyEntry{Name: resource.Name, BranchName: branch.Name, Ref: domain.DependencyRef{Kind: "resource", BranchID: branch.ID, ID: resource.ID, Revision: release.ID, Hash: resourceNodeHash(artifact.Hash, artifact.Policy, artifact.Dependencies)}})
	}
	if release.Manifest.IPCandidateID != "" {
		candidate, err := candidateTx(ctx, tx, branch.ID, release.Manifest.IPCandidateID)
		if err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, snapshotEntries(branch.ID, candidate.ID, branch.Name, candidate.Snapshot)...)
		result.Snapshot = candidate.Snapshot
	}
	return result, nil
}

// Append upstream material to the same bounded review document. Prefix keys
// distinguish the same entity/resource appearing in different branch versions.
func (s *SemanticReviews) appendPeerReviewTx(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest, doc *domain.SemanticReviewDocument, sources *[]string) error {
	peers, err := releasePeersTx(ctx, tx, project, manifest)
	if err != nil {
		return err
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	bytesUsed := int64(0)
	for _, artifact := range doc.Artifacts {
		bytesUsed += int64(len(artifact.Text))
	}
	for _, peer := range peers {
		for _, artifact := range peer.Manifest.Artifacts {
			if artifact.Size < 0 || bytesUsed+artifact.Size > 1024*1024 {
				return fmt.Errorf("%w：协同复核的完整上游内容超过单次审查上限，请拆分候选范围", domain.ErrContextBudget)
			}
			bytesUsed += artifact.Size
			data, err := s.Generations.IP.Blobs.ReadObject(ctx, project, artifact.Hash)
			if err != nil || digest(string(data)) != artifact.Hash {
				return domain.ErrNotReady
			}
			key := "peer:" + peer.ID + ":resource:" + artifact.ResourceID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(data)})
			target := domain.ArtifactReviewTarget{ReleaseID: peer.ID, ManifestHash: peer.Hash, ResourceID: artifact.ResourceID, BranchID: peer.Manifest.BranchID, ExpectedHead: artifact.ExpectedHead, IPHead: manifestIPHead(peer.Manifest), ContentHash: artifact.Hash}
			if artifact.Origin != nil && artifact.Origin.Generation != nil {
				target.SourceGenerationID = artifact.Origin.Generation.GenerationID
				*sources = append(*sources, target.SourceGenerationID)
			}
			doc.Artifacts = append(doc.Artifacts, domain.SemanticReviewArtifact{Target: target, Text: string(data)})
		}
		peerSnapshot, err := snapshotTx(ctx, tx, project, peer.Manifest.ExpectedIPHead)
		if err != nil {
			return err
		}
		if peer.Manifest.IPCandidateID != "" {
			candidate, err := candidateTx(ctx, tx, peer.Manifest.BranchID, peer.Manifest.IPCandidateID)
			if err != nil {
				return err
			}
			peerSnapshot = candidate.Snapshot
		}
		peerDoc, err := domain.CompleteSemanticReviewSnapshot(domain.SemanticReviewDocument{Snapshot: peerSnapshot})
		if err != nil {
			return err
		}
		prefix := "peer:" + peer.ID + ":"
		for _, key := range peerDoc.RequiredCoverage {
			doc.RequiredCoverage = append(doc.RequiredCoverage, prefix+key)
		}
		for _, anchor := range peerDoc.Anchors {
			bytesUsed += int64(len(anchor.Text))
			if bytesUsed > 1024*1024 {
				return fmt.Errorf("%w：协同复核设定超过单次审查上限，请拆分候选范围", domain.ErrContextBudget)
			}
			anchor.Key, anchor.TargetKey = prefix+anchor.Key, prefix+anchor.TargetKey
			doc.Anchors = append(doc.Anchors, anchor)
		}
		for _, finding := range peerDoc.HardFindings {
			doc.HardFindings = append(doc.HardFindings, prefix+finding)
		}
	}
	return nil
}
