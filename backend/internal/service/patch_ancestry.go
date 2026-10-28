package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"slices"
	"sort"
)

// Patch roots survive manual editing just as generation ancestry does. They
// carry no review verdict: each new manifest needs its own independent report.
func (s *IP) patchParentsTx(ctx context.Context, tx *sql.Tx, project string, m domain.Manifest) ([]string, error) {
	roots := map[string]bool{}
	seen := map[string]bool{}
	read := func(id string) error {
		if id == "" || id == m.ReleaseID || seen[id] {
			return nil
		}
		seen[id] = true
		parent, err := releaseTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if parent.Manifest.BranchID != m.BranchID {
			return domain.ErrForbidden
		}
		if parent.Manifest.PatchGroup != nil || len(parent.Manifest.CompositionParents) > 0 || len(parent.Manifest.PeerReleases) > 0 {
			roots[parent.ID] = true
		}
		for _, root := range parent.Manifest.PatchParents {
			if root != m.ReleaseID {
				roots[root] = true
			}
		}
		return nil
	}
	for _, ref := range m.Artifacts {
		if ref.Origin == nil {
			return nil, domain.ErrValidation
		}
		if err := read(ref.Origin.ParentReleaseID); err != nil {
			return nil, err
		}
		if err := read(ref.ExpectedHead); err != nil {
			return nil, err
		}
		// A private draft may reopen without the old UI source selection. Once a
		// manifest is saved, newer drafts cannot retroactively become its parents.
		var pending string
		err := tx.QueryRowContext(ctx, `SELECT r.id FROM artifact_releases r
 JOIN JSON_TABLE(r.manifest,'$.artifacts[*]' COLUMNS (
 resource_id VARCHAR(32) PATH '$.resource_id',base_head VARCHAR(32) PATH '$.expected_head'
 )) a ON a.resource_id=? AND a.base_head=?
 WHERE r.project_id=? AND r.branch_id=? AND r.created_by=? AND r.id<>?
 AND r.status IN ('PREPARED','READY')
 AND r.created_at <= COALESCE((SELECT saved.created_at FROM artifact_releases saved WHERE saved.id=? AND saved.project_id=?),CURRENT_TIMESTAMP(6))
 AND (JSON_EXTRACT(r.manifest,'$.patch_group') IS NOT NULL OR JSON_LENGTH(JSON_EXTRACT(r.manifest,'$.patch_parents'))>0 OR JSON_LENGTH(JSON_EXTRACT(r.manifest,'$.composition_parents'))>0 OR JSON_LENGTH(JSON_EXTRACT(r.manifest,'$.peer_releases'))>0)
 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, ref.ResourceID, ref.ExpectedHead, project, m.BranchID, ref.Origin.ActorID, m.ReleaseID, m.ReleaseID, project).Scan(&pending)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			if err = read(pending); err != nil {
				return nil, err
			}
		}
	}
	if len(roots) > 32 {
		return nil, domain.ErrValidation
	}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *IP) validatePatchParentsTx(ctx context.Context, tx *sql.Tx, project string, m domain.Manifest) error {
	parents, err := s.patchParentsTx(ctx, tx, project, m)
	if err != nil {
		return err
	}
	if !slices.Equal(parents, m.PatchParents) {
		return domain.ErrConflict
	}
	members := make(map[string]bool, len(m.Artifacts))
	for _, ref := range m.Artifacts {
		members[ref.ResourceID] = true
	}
	for _, id := range parents {
		root, err := releaseTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if (root.Manifest.PatchGroup == nil && len(root.Manifest.CompositionParents) == 0 && len(root.Manifest.PeerReleases) == 0) || root.Manifest.BranchID != m.BranchID {
			return domain.ErrNotReady
		}
		// Once the root or a complete descendant publishes, later edits may
		// revise one member, while retaining the independent review requirement.
		var published int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_releases WHERE project_id=? AND branch_id=? AND status='PUBLISHED'
 AND (id=? OR JSON_CONTAINS(JSON_EXTRACT(manifest,'$.patch_parents'),JSON_QUOTE(?)))`, project, m.BranchID, id, id).Scan(&published)
		if err != nil {
			return err
		}
		if published > 0 {
			continue
		}
		// Preserve the entire first joint publication, including new resources
		// and policy-only edits which need no text patch of their own.
		for _, artifact := range root.Manifest.Artifacts {
			if !members[artifact.ResourceID] {
				return domain.ErrConflict
			}
		}
	}
	return nil
}
