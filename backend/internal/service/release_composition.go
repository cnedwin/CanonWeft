package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

type ReleaseCompositionInput struct {
	OperationID     string            `json:"operation_id"`
	Releases        []JointReleasePin `json:"releases"`
	RebindFollowing bool              `json:"rebind_following"`
}
type CompositionConflict struct {
	ResourceID string   `json:"resource_id"`
	Releases   []string `json:"releases"`
	Reason     string   `json:"reason"`
}
type CompositionRebinding struct {
	ResourceID string               `json:"resource_id"`
	Before     domain.DependencyRef `json:"before"`
	Target     string               `json:"target"`
}
type ReleaseCompositionPreview struct {
	BranchID       string                  `json:"branch_id"`
	ExpectedIPHead string                  `json:"expected_ip_head"`
	IPCandidateID  string                  `json:"ip_candidate_id"`
	Updates        []domain.ResourceUpdate `json:"updates"`
	Conflicts      []CompositionConflict   `json:"conflicts"`
	Rebindings     []CompositionRebinding  `json:"rebindings"`
}

func normalizeComposition(input ReleaseCompositionInput) (ReleaseCompositionInput, error) {
	if !validName(input.OperationID, 120) || len(input.Releases) < 2 || len(input.Releases) > 16 {
		return input, domain.ErrValidation
	}
	input.Releases = slices.Clone(input.Releases)
	slices.SortFunc(input.Releases, func(a, b JointReleasePin) int { return strings.Compare(a.ReleaseID, b.ReleaseID) })
	for i, p := range input.Releases {
		if p.ReleaseID == "" || p.ManifestHash == "" || (i > 0 && p.ReleaseID == input.Releases[i-1].ReleaseID) {
			return input, domain.ErrValidation
		}
	}
	return input, nil
}

func (s *IP) PreviewReleaseComposition(ctx context.Context, a domain.Actor, project string, input ReleaseCompositionInput) (ReleaseCompositionPreview, error) {
	input, err := normalizeComposition(input)
	if err != nil {
		return ReleaseCompositionPreview{}, err
	}
	return s.compileReleaseComposition(ctx, a, project, input, true)
}

func (s *IP) PrepareReleaseComposition(ctx context.Context, a domain.Actor, project string, input ReleaseCompositionInput) (domain.Release, error) {
	input, err := normalizeComposition(input)
	if err != nil {
		return domain.Release{}, err
	}
	return retryReleaseSourceAuthority(ctx, func() (domain.Release, error) {
		// Frozen parents can be reconstructed after publication, allowing the core
		// receipt lookup to replay a lost acknowledgement before checking live heads.
		preview, err := s.compileReleaseComposition(ctx, a, project, input, false)
		if err != nil {
			return domain.Release{}, err
		}
		if len(preview.Conflicts) > 0 {
			return domain.Release{}, domain.ErrConflict
		}
		release := domain.ReleaseInput{BranchID: preview.BranchID, ExpectedIPHead: preview.ExpectedIPHead, IPCandidateID: preview.IPCandidateID, OperationID: input.OperationID, Updates: preview.Updates}
		return s.prepareReleaseWithComposition(ctx, a, project, release, &input)
	})
}

func (s *IP) compileReleaseComposition(ctx context.Context, a domain.Actor, project string, input ReleaseCompositionInput, current bool) (ReleaseCompositionPreview, error) {
	result := ReleaseCompositionPreview{Updates: []domain.ResourceUpdate{}, Conflicts: []CompositionConflict{}, Rebindings: []CompositionRebinding{}}
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	refs := map[string]domain.ArtifactRef{}
	owners := map[string]string{}
	parentIDs := map[string]bool{}
	for i, pin := range input.Releases {
		parent, err := releaseTx(ctx, tx, project, pin.ReleaseID)
		if err != nil {
			return result, err
		}
		if parent.Hash != pin.ManifestHash {
			return result, domain.ErrConflict
		}
		if current && parent.Status == "PUBLISHED" {
			return result, domain.ErrConflict
		}
		parentIDs[parent.ID] = true
		m := parent.Manifest
		if i == 0 {
			result.BranchID, result.ExpectedIPHead = m.BranchID, m.ExpectedIPHead
		}
		if m.BranchID != result.BranchID || m.ExpectedIPHead != result.ExpectedIPHead {
			return result, domain.ErrConflict
		}
		if m.IPCandidateID != "" {
			if result.IPCandidateID != "" && result.IPCandidateID != m.IPCandidateID {
				return result, domain.ErrConflict
			}
			result.IPCandidateID = m.IPCandidateID
		}
		for _, ref := range m.Artifacts {
			if old, exists := refs[ref.ResourceID]; exists {
				if valueHash(old) != valueHash(ref) {
					result.Conflicts = append(result.Conflicts, CompositionConflict{ResourceID: ref.ResourceID, Releases: []string{owners[ref.ResourceID], parent.ID}, Reason: "同一份资料存在不同正文、依赖或来源，请先处理冲突后再组合。"})
				}
				continue
			}
			refs[ref.ResourceID], owners[ref.ResourceID] = ref, parent.ID
		}
	}
	if len(refs) > 32 {
		return result, domain.ErrValidation
	}
	if current {
		branch, err := branchTx(ctx, tx, project, result.BranchID)
		if err != nil {
			return result, err
		}
		if branch.Head != result.ExpectedIPHead {
			return result, domain.ErrConflict
		}
	}
	ids := []string{}
	for id := range refs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		ref := refs[id]
		if current {
			resource, err := resourceTx(ctx, tx, project, id)
			if err != nil {
				return result, err
			}
			if resource.Head != ref.ExpectedHead {
				return result, domain.ErrConflict
			}
		}
		raw, err := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if err != nil || digest(string(raw)) != ref.Hash {
			return result, domain.ErrNotReady
		}
		update := domain.ResourceUpdate{ResourceID: id, ExpectedHead: ref.ExpectedHead, Text: string(raw), Policy: ref.Policy, Dependencies: ref.Dependencies, SourceImportID: ref.SourceImportID, PartialAcknowledged: ref.PartialAcknowledged, FrozenChangeReason: ref.FrozenChangeReason, Origin: &domain.ArtifactOriginInput{ParentReleaseID: owners[id]}}
		if update.Dependencies != nil {
			encoded, _ := json.Marshal(update.Dependencies)
			var cloned domain.DependencyPolicy
			if err = json.Unmarshal(encoded, &cloned); err != nil {
				return result, err
			}
			update.Dependencies = &cloned
			for j := range cloned.Inputs {
				dep := &cloned.Inputs[j]
				if dep.Source.Kind != "resource" || dep.Source.BranchID != result.BranchID {
					continue
				}
				if _, selected := refs[dep.Source.ID]; !selected {
					continue
				}
				// References internal to an original candidate must stay internal to the
				// composed candidate. Historical pins to a published version stay intact.
				if parentIDs[dep.Source.Revision] || (!dep.Pinned && input.RebindFollowing) {
					result.Rebindings = append(result.Rebindings, CompositionRebinding{ResourceID: id, Before: dep.Source, Target: dep.Source.ID})
					dep.Source.Revision, dep.Source.Hash = "$release", ""
				} else if !dep.Pinned {
					result.Conflicts = append(result.Conflicts, CompositionConflict{ResourceID: id, Releases: []string{owners[id]}, Reason: "依赖仍跟随本组即将替换的旧版本，请明确选择改绑组合版本。"})
				}
			}
		}
		result.Updates = append(result.Updates, update)
	}
	return result, nil
}

func validateCompositionParentsTx(ctx context.Context, tx *sql.Tx, project string, m domain.Manifest) error {
	if len(m.CompositionParents) == 0 {
		return nil
	}
	if len(m.CompositionParents) < 2 || len(m.CompositionParents) > 16 {
		return domain.ErrValidation
	}
	seen := map[string]bool{}
	members := map[string]domain.ArtifactRef{}
	for _, ref := range m.Artifacts {
		members[ref.ResourceID] = ref
	}
	for _, id := range m.CompositionParents {
		if id == m.ReleaseID || seen[id] {
			return domain.ErrValidation
		}
		seen[id] = true
		parent, err := releaseTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if parent.Manifest.BranchID != m.BranchID || parent.Manifest.ExpectedIPHead != m.ExpectedIPHead || (parent.Manifest.IPCandidateID != "" && parent.Manifest.IPCandidateID != m.IPCandidateID) {
			return domain.ErrConflict
		}
		for _, ref := range parent.Manifest.Artifacts {
			actual, exists := members[ref.ResourceID]
			if !exists || actual.ExpectedHead != ref.ExpectedHead || actual.Hash != ref.Hash {
				return domain.ErrConflict
			}
		}
	}
	return nil
}
