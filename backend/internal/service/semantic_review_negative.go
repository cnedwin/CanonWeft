package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

func candidateSemanticScope(c Candidate) (string, error) {
	return domain.SemanticContentScopeHash(domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: "ip_candidate", BranchID: c.BranchID, ExpectedIPHead: c.BaseHead}, Snapshot: c.Snapshot, Scope: semanticReviewScope})
}

// This read-only indication uses frozen historical material, not current source
// permission, so a revoked source does not hide a user's saved draft. Adoption
// separately validates the complete manifest and selected review authority.
func (s *IP) releaseSemanticRequirementTx(ctx context.Context, tx *sql.Tx, project string, r domain.Release) (bool, error) {
	if r.Manifest.IPCandidateID != "" || r.Manifest.PatchGroup != nil || len(r.Manifest.PatchParents) > 0 || len(r.Manifest.CompositionParents) > 0 || len(r.Manifest.PeerReleases) > 0 {
		return true, nil
	}
	for _, ref := range r.Manifest.Artifacts {
		if ref.Origin != nil && ref.Origin.ReviewRequired {
			return true, nil
		}
	}
	snapshot, err := snapshotTx(ctx, tx, project, r.Manifest.ExpectedIPHead)
	if err != nil {
		return false, err
	}
	doc := domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: "artifact_release", BranchID: r.Manifest.BranchID, ExpectedIPHead: r.Manifest.ExpectedIPHead}, Snapshot: snapshot, Scope: semanticReviewScope}
	for _, ref := range r.Manifest.Artifacts {
		raw, e := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if e != nil || digest(string(raw)) != ref.Hash {
			return false, domain.ErrNotReady
		}
		doc.Artifacts = append(doc.Artifacts, domain.SemanticReviewArtifact{Target: domain.ArtifactReviewTarget{ResourceID: ref.ResourceID, ExpectedHead: ref.ExpectedHead, IPHead: r.Manifest.ExpectedIPHead, ContentHash: ref.Hash}, Text: string(raw)})
	}
	scope, err := domain.SemanticContentScopeHash(doc)
	if err != nil {
		return false, err
	}
	return s.knownSemanticNegativeTx(ctx, tx, r.Manifest.BranchID, "artifact_release", scope, "")
}

// Negative equivalence is separate from positive authorization. Old live
// records are compared from verified frozen input, never silently skipped.
func (s *IP) knownSemanticNegativeTx(ctx context.Context, tx *sql.Tx, branch, kind, scope, afterReview string) (bool, error) {
	query := "SELECT " + semanticReviewColumns + ` FROM semantic_review_runs WHERE branch_id=? AND target_kind=?
 AND state='COMPLETED' AND verdict IN ('FAIL','UNKNOWN') AND report_hash<>''
 AND restored_disabled=FALSE AND cancel_requested=FALSE
 AND (JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.content_scope_hash'))=? OR JSON_EXTRACT(frozen,'$.content_scope_hash') IS NULL)`
	args := []any{branch, kind, scope}
	if afterReview != "" {
		query += " AND updated_at > (SELECT updated_at FROM (SELECT updated_at FROM semantic_review_runs WHERE id=?) selected_report)"
		args = append(args, afterReview)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanSemanticReview(rows)
		if e != nil {
			return false, e
		}
		actual := r.Frozen.ContentScopeHash
		if actual == "" {
			doc, e := (&SemanticReviews{Generations: &Generations{IP: s}}).document(ctx, r)
			if e != nil {
				return false, e
			}
			actual, e = domain.SemanticContentScopeHash(doc)
			if e != nil {
				return false, e
			}
		}
		if actual == scope {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *SemanticReviews) noNewerSemanticNegativeTx(ctx context.Context, tx *sql.Tx, r SemanticReview) error {
	scope := r.Frozen.ContentScopeHash
	if scope == "" {
		doc, err := s.document(ctx, r)
		if err != nil {
			return err
		}
		scope, err = domain.SemanticContentScopeHash(doc)
		if err != nil {
			return err
		}
	}
	negative, err := s.Generations.IP.knownSemanticNegativeTx(ctx, tx, r.BranchID, r.TargetKind, scope, r.ID)
	if err != nil {
		return err
	}
	if negative {
		return domain.ErrConflict
	}
	return nil
}
