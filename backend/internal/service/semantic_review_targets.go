package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"slices"
)

const semanticReviewScope = domain.SemanticReviewScope

func (s *SemanticReviews) targetDocumentTx(ctx context.Context, tx *sql.Tx, project string, input SemanticReviewInput) (domain.SemanticReviewDocument, []string, error) {
	doc := domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: input.TargetKind, ID: input.TargetID, Hash: input.TargetHash, BranchID: input.BranchID}, Artifacts: []domain.SemanticReviewArtifact{}, HardFindings: []string{}, RequiredCoverage: []string{}, Anchors: []domain.SemanticReviewAnchor{}, Scope: semanticReviewScope}
	sources := []string{}
	ip := s.Generations.IP
	b, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return doc, sources, err
	}
	doc.Target.ExpectedIPHead = b.Head
	switch input.TargetKind {
	case "ip_candidate":
		c, e := candidateTx(ctx, tx, input.BranchID, input.TargetID)
		if e != nil {
			return doc, sources, e
		}
		if c.Hash != input.TargetHash || c.BaseHead != b.Head || c.Status == "PUBLISHED" {
			return doc, sources, domain.ErrConflict
		}
		if _, e = validateCandidateAncestryTx(ctx, tx, project, b, c); e != nil {
			return doc, sources, e
		}
		raw, e := ip.Blobs.ReadObject(ctx, project, c.Hash)
		if e != nil || digest(string(raw)) != c.Hash {
			return doc, sources, domain.ErrNotReady
		}
		if e = json.Unmarshal(raw, &doc.Snapshot); e != nil {
			return doc, sources, domain.ErrNotReady
		}
		var payload []byte
		e = tx.QueryRowContext(ctx, "SELECT payload FROM ip_branch_deltas WHERE candidate_id=? AND project_id=?", c.ID, project).Scan(&payload)
		if e != nil && e != sql.ErrNoRows {
			return doc, sources, e
		}
		if e == nil {
			var delta domain.BranchDelta
			if json.Unmarshal(payload, &delta) != nil {
				return doc, sources, domain.ErrNotReady
			}
			doc.Delta = &delta
		}
	case "artifact_release":
		release, e := releaseTx(ctx, tx, project, input.TargetID)
		if e != nil {
			return doc, sources, e
		}
		if release.Hash != input.TargetHash || release.Manifest.BranchID != input.BranchID || release.Status == "PUBLISHED" {
			return doc, sources, domain.ErrConflict
		}
		if e = ip.validateManifest(ctx, tx, project, release.Manifest); e != nil {
			return doc, sources, e
		}
		doc.Snapshot, e = snapshotTx(ctx, tx, project, b.Head)
		if e != nil {
			return doc, sources, e
		}
		if release.Manifest.IPCandidateID != "" {
			c, ce := candidateTx(ctx, tx, input.BranchID, release.Manifest.IPCandidateID)
			if ce != nil {
				return doc, sources, ce
			}
			doc.Snapshot = c.Snapshot
		}
		for _, ref := range release.Manifest.Artifacts {
			raw, e := ip.Blobs.ReadObject(ctx, project, ref.Hash)
			if e != nil || digest(string(raw)) != ref.Hash {
				return doc, sources, domain.ErrNotReady
			}
			source := ""
			if ref.Origin != nil && ref.Origin.Generation != nil {
				source = ref.Origin.Generation.GenerationID
				sources = append(sources, source)
			}
			target := domain.ArtifactReviewTarget{ReleaseID: release.ID, ManifestHash: release.Hash, ResourceID: ref.ResourceID, BranchID: input.BranchID, ExpectedHead: ref.ExpectedHead, IPHead: b.Head, ContentHash: ref.Hash, SourceGenerationID: source}
			doc.Artifacts = append(doc.Artifacts, domain.SemanticReviewArtifact{Target: target, Text: string(raw)})
			key := "resource:" + ref.ResourceID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(raw)})
		}
		if e = ip.appendChapterReviewPlansTx(ctx, tx, project, release.Manifest, &doc); e != nil {
			return doc, sources, e
		}
		if e = s.appendPeerReviewTx(ctx, tx, project, release.Manifest, &doc, &sources); e != nil {
			return doc, sources, e
		}
	default:
		return doc, sources, domain.ErrValidation
	}
	doc, err = domain.CompleteSemanticReviewSnapshot(doc)
	if err != nil {
		return doc, sources, err
	}
	slices.Sort(sources)
	return doc, slices.Compact(sources), nil
}

func (s *SemanticReviews) validateTargetTx(ctx context.Context, tx *sql.Tx, r SemanticReview) error {
	if r.RestoredDisabled || r.CancelRequested {
		return domain.ErrConflict
	}
	doc, _, err := s.targetDocumentTx(ctx, tx, r.ProjectID, SemanticReviewInput{TargetKind: r.TargetKind, TargetID: r.TargetID, TargetHash: r.TargetHash, BranchID: r.BranchID})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil || digest(string(raw)) != r.Frozen.InputHash {
		return domain.ErrConflict
	}
	if r.Frozen.ContentScopeHash != "" {
		scope, e := domain.SemanticContentScopeHash(doc)
		if e != nil || scope != r.Frozen.ContentScopeHash {
			return domain.ErrConflict
		}
	}
	return nil
}
