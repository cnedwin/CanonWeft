package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

type PatchInput struct {
	BranchID       string                `json:"branch_id"`
	ExpectedIPHead string                `json:"expected_ip_head"`
	OperationID    string                `json:"operation_id,omitempty"`
	Group          domain.TextPatchGroup `json:"group"`
}

type PatchPreview struct {
	Items []domain.PatchedResource `json:"items"`
}

// patchTargetsTx reads original published artifacts by immutable revision. The
// preview additionally requires current heads; preparation's authority check is
// in PrepareRelease so a lost acknowledgement can still replay after publication.
func (s *IP) patchTargetsTx(ctx context.Context, tx *sql.Tx, project, branch string, group domain.TextPatchGroup, current bool) ([]domain.PatchTarget, error) {
	if len(group.Members) == 0 || len(group.Members) > 32 || len(group.Patches) == 0 || len(group.Patches) > domain.MaxTextPatches {
		return nil, domain.ErrValidation
	}
	patches := make(map[string]domain.TextPatch, len(group.Members))
	for _, p := range group.Patches {
		if prior, ok := patches[p.ResourceID]; ok && (prior.BaseRevision != p.BaseRevision || prior.BaseHash != p.BaseHash) {
			return nil, domain.ErrConflict
		}
		patches[p.ResourceID] = p
	}
	targets := make([]domain.PatchTarget, 0, len(group.Members))
	for _, id := range group.Members {
		r, err := resourceTx(ctx, tx, project, id)
		if err != nil {
			return nil, err
		}
		if r.BranchID != branch {
			return nil, domain.ErrForbidden
		}
		p, ok := patches[id]
		if !ok {
			return nil, domain.ErrValidation
		}
		if current && (r.Head != p.BaseRevision || r.Hash != p.BaseHash) {
			return nil, domain.ErrConflict
		}
		previous, err := releaseTx(ctx, tx, project, p.BaseRevision)
		if err != nil {
			return nil, err
		}
		if previous.Status != "PUBLISHED" || previous.Manifest.BranchID != branch {
			return nil, domain.ErrConflict
		}
		hash := ""
		for _, ref := range previous.Manifest.Artifacts {
			if ref.ResourceID == id {
				hash = ref.Hash
				break
			}
		}
		if hash == "" || hash != p.BaseHash {
			return nil, domain.ErrConflict
		}
		body, err := s.Blobs.ReadObject(ctx, project, hash)
		if err != nil || digest(string(body)) != hash {
			return nil, domain.ErrNotReady
		}
		targets = append(targets, domain.PatchTarget{ResourceID: id, Revision: p.BaseRevision, Hash: hash, Text: string(body)})
	}
	return targets, nil
}

func (s *IP) previewPatches(ctx context.Context, a domain.Actor, project string, input PatchInput, current bool) (PatchPreview, error) {
	var preview PatchPreview
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return preview, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return preview, err
	}
	if current && b.Head != input.ExpectedIPHead {
		return preview, domain.ErrConflict
	}
	if err = s.validateCompensationTx(ctx, tx, project, b.ID, input.Group); err != nil {
		return preview, err
	}
	targets, err := s.patchTargetsTx(ctx, tx, project, b.ID, input.Group, current)
	if err != nil {
		return preview, err
	}
	preview.Items, err = domain.ApplyTextPatchGroup(targets, input.Group)
	return preview, err
}

// PreviewPatches performs no writes and never replaces the author's draft.
func (s *IP) PreviewPatches(ctx context.Context, a domain.Actor, project string, input PatchInput) (PatchPreview, error) {
	return s.previewPatches(ctx, a, project, input, true)
}

func (s *IP) PreparePatchRelease(ctx context.Context, a domain.Actor, project string, input PatchInput) (domain.Release, error) {
	if !validName(input.OperationID, 120) {
		return domain.Release{}, domain.ErrValidation
	}
	preview, err := s.previewPatches(ctx, a, project, input, false)
	if err != nil {
		return domain.Release{}, err
	}
	release := domain.ReleaseInput{BranchID: input.BranchID, ExpectedIPHead: input.ExpectedIPHead, OperationID: input.OperationID, PatchGroup: &input.Group}
	for _, item := range preview.Items {
		release.Updates = append(release.Updates, domain.ResourceUpdate{ResourceID: item.Target.ResourceID, ExpectedHead: item.Target.Revision, Text: item.Result.Text, FrozenChangeReason: item.Result.Applied[0].Reason})
	}
	return s.PrepareRelease(ctx, a, project, release)
}

func (s *IP) validatePatchManifestTx(ctx context.Context, tx *sql.Tx, project string, m domain.Manifest) error {
	if m.PatchGroup == nil {
		return nil
	}
	if err := s.validateCompensationTx(ctx, tx, project, m.BranchID, *m.PatchGroup); err != nil {
		return err
	}
	targets, err := s.patchTargetsTx(ctx, tx, project, m.BranchID, *m.PatchGroup, true)
	if err != nil {
		return err
	}
	results, err := domain.ApplyTextPatchGroup(targets, *m.PatchGroup)
	if err != nil {
		return err
	}
	// A joint candidate can additionally preserve newly created resources and
	// policy-only edits. Every declared patch member is still required below;
	// the enclosing manifest validates every additional artifact independently.
	if len(results) > len(m.Artifacts) {
		return domain.ErrValidation
	}
	refs := make(map[string]domain.ArtifactRef, len(m.Artifacts))
	for _, ref := range m.Artifacts {
		if _, exists := refs[ref.ResourceID]; exists {
			return domain.ErrValidation
		}
		refs[ref.ResourceID] = ref
	}
	for _, result := range results {
		ref, ok := refs[result.Target.ResourceID]
		if !ok || ref.ExpectedHead != result.Target.Revision || ref.Hash != result.Result.Hash || ref.Size != int64(len(result.Result.Text)) {
			return domain.ErrConflict
		}
	}
	return nil
}
