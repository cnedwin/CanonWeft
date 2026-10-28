package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"novelstudio/internal/domain"
)

type CompensationInput struct {
	BranchID       string `json:"branch_id"`
	ReleaseID      string `json:"release_id"`
	ExpectedIPHead string `json:"expected_ip_head"`
	Reason         string `json:"reason"`
}

type CompensationConflict struct {
	ResourceID string `json:"resource_id"`
	Reason     string `json:"reason"`
}

type CompensationPreview struct {
	Group     *domain.TextPatchGroup   `json:"group"`
	Items     []domain.PatchedResource `json:"items"`
	Conflicts []CompensationConflict   `json:"conflicts"`
}

// PreviewCompensation only reverses the selected publication's text changes.
// IP, policy and dependency changes remain explicit independent decisions.
func (s *IP) PreviewCompensation(ctx context.Context, a domain.Actor, project string, input CompensationInput) (CompensationPreview, error) {
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return CompensationPreview{}, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return CompensationPreview{}, err
	}
	if b.Head != input.ExpectedIPHead {
		return CompensationPreview{}, domain.ErrConflict
	}
	return s.compensationTx(ctx, tx, project, b.ID, input.ReleaseID, input.Reason, NewID(), nil)
}

func releaseArtifact(release domain.Release, resource string) (domain.ArtifactRef, error) {
	var result domain.ArtifactRef
	found := false
	for _, ref := range release.Manifest.Artifacts {
		if ref.ResourceID != resource {
			continue
		}
		if found {
			return result, domain.ErrNotReady
		}
		result, found = ref, true
	}
	if !found {
		return result, domain.ErrNotReady
	}
	return result, nil
}

func (s *IP) compensationText(ctx context.Context, project string, ref domain.ArtifactRef) (string, error) {
	if ref.Hash == "" {
		return "", nil
	}
	body, err := s.Blobs.ReadObject(ctx, project, ref.Hash)
	if err != nil || digest(string(body)) != ref.Hash || int64(len(body)) != ref.Size {
		return "", domain.ErrNotReady
	}
	return string(body), nil
}

// compensationTx follows the actual published ExpectedHead chain, not a list
// supplied by a model/client. The closed bound prevents unbounded transaction IO.
func (s *IP) compensationTx(ctx context.Context, tx *sql.Tx, project, branch, sourceID, reason, groupID string, targets map[string]string) (CompensationPreview, error) {
	result := CompensationPreview{Items: []domain.PatchedResource{}, Conflicts: []CompensationConflict{}}
	if !validName(reason, 2000) {
		return result, domain.ErrValidation
	}
	source, err := releaseTx(ctx, tx, project, sourceID)
	if err != nil {
		return result, err
	}
	if source.Status != "PUBLISHED" {
		return result, domain.ErrConflict
	}
	if source.Manifest.BranchID != branch {
		return result, domain.ErrForbidden
	}
	group := domain.TextPatchGroup{ID: groupID, CompensatesReleaseID: sourceID, Members: []string{}, Patches: []domain.TextPatch{}}
	patchTargets := []domain.PatchTarget{}
	reads := 0
	for _, published := range source.Manifest.Artifacts {
		old := domain.ArtifactRef{}
		if published.ExpectedHead != "" {
			prior, err := releaseTx(ctx, tx, project, published.ExpectedHead)
			if err != nil {
				return result, err
			}
			if prior.Status != "PUBLISHED" || prior.Manifest.BranchID != branch {
				return result, domain.ErrNotReady
			}
			old, err = releaseArtifact(prior, published.ResourceID)
			if err != nil {
				return result, err
			}
		}
		if old.Hash == published.Hash {
			continue
		}
		original, err := s.compensationText(ctx, project, old)
		if err != nil {
			return result, err
		}
		body, err := s.compensationText(ctx, project, published)
		if err != nil {
			return result, err
		}
		if original == body {
			continue
		}
		edit, err := domain.PlanTextCompensation(original, body)
		if err != nil {
			return result, err
		}
		r, err := resourceTx(ctx, tx, project, published.ResourceID)
		if err != nil {
			return result, err
		}
		if r.BranchID != branch {
			return result, domain.ErrNotReady
		}
		targetID := r.Head
		if targets != nil {
			var found bool
			targetID, found = targets[r.ID]
			if !found {
				return result, domain.ErrConflict
			}
		}
		cursor := targetID
		history := []string{}
		seen := map[string]bool{}
		conflict := false
		for cursor != source.ID {
			if cursor == "" || seen[cursor] {
				conflict = true
				break
			}
			seen[cursor] = true
			reads++
			if reads > 256 {
				return result, domain.ErrNotReady
			}
			next, err := releaseTx(ctx, tx, project, cursor)
			if err != nil {
				return result, err
			}
			if next.Status != "PUBLISHED" || next.Manifest.BranchID != branch {
				return result, domain.ErrNotReady
			}
			ref, err := releaseArtifact(next, r.ID)
			if err != nil {
				return result, err
			}
			text, err := s.compensationText(ctx, project, ref)
			if err != nil {
				return result, err
			}
			history = append(history, text)
			cursor = ref.ExpectedHead
		}
		current := body
		for i := len(history) - 1; i >= 0 && !conflict; i-- {
			edit, err = domain.RebaseTextCompensation(edit, current, history[i])
			if errors.Is(err, domain.ErrConflict) {
				conflict = true
				break
			}
			if err != nil {
				return result, err
			}
			current = history[i]
		}
		if conflict {
			result.Conflicts = append(result.Conflicts, CompensationConflict{ResourceID: r.ID, Reason: "后续版本修改了同一范围或版本链不再包含该发布，请对照历史手工修订。"})
			continue
		}
		target := domain.PatchTarget{ResourceID: r.ID, Revision: targetID, Hash: digest(current), Text: current}
		patchTargets = append(patchTargets, target)
		group.Members = append(group.Members, r.ID)
		group.Patches = append(group.Patches, domain.TextPatch{ID: digest("compensation-patch:" + groupID + ":" + r.ID)[:32], GroupID: groupID, ResourceID: r.ID, BaseRevision: targetID, BaseHash: target.Hash, StartRune: edit.StartRune, EndRune: edit.EndRune, ExpectedSliceHash: digest(edit.Expected), Replacement: edit.Replacement, Reason: reason})
	}
	if len(result.Conflicts) > 0 {
		return result, nil
	}
	if len(group.Members) == 0 {
		return result, domain.ErrValidation
	}
	slices.Sort(group.Members)
	result.Items, err = domain.ApplyTextPatchGroup(patchTargets, group)
	if err != nil {
		return result, err
	}
	result.Group = &group
	return result, nil
}

func (s *IP) validateCompensationTx(ctx context.Context, tx *sql.Tx, project, branch string, group domain.TextPatchGroup) error {
	if group.CompensatesReleaseID == "" {
		return nil
	}
	if len(group.Patches) == 0 {
		return domain.ErrValidation
	}
	targets := map[string]string{}
	for _, p := range group.Patches {
		if _, ok := targets[p.ResourceID]; ok {
			return domain.ErrValidation
		}
		targets[p.ResourceID] = p.BaseRevision
	}
	expected, err := s.compensationTx(ctx, tx, project, branch, group.CompensatesReleaseID, group.Patches[0].Reason, group.ID, targets)
	if err != nil {
		return err
	}
	if expected.Group == nil || len(expected.Group.Patches) != len(group.Patches) || len(expected.Group.Members) != len(group.Members) {
		return domain.ErrConflict
	}
	for _, want := range expected.Group.Patches {
		found := false
		for _, actual := range group.Patches {
			if actual.ResourceID == want.ResourceID {
				if actual != want {
					return domain.ErrConflict
				}
				found = true
			}
		}
		if !found {
			return domain.ErrConflict
		}
	}
	return nil
}
