package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

func manifestIPHead(m domain.Manifest) string {
	if m.IPCandidateID != "" {
		return m.IPCandidateID
	}
	return m.ExpectedIPHead
}

func originArtifact(r domain.Release, resource string) (domain.ArtifactRef, error) {
	for _, ref := range r.Manifest.Artifacts {
		if ref.ResourceID == resource {
			return ref, nil
		}
	}
	return domain.ArtifactRef{}, domain.ErrForbidden
}

// A source selection is itself a PREPARED release. If a private text draft is
// restored without its UI selection, known AI ancestry still cannot disappear.
// Other authors' unadopted selections do not claim this author's draft.
func pendingAIOriginTx(ctx context.Context, tx *sql.Tx, project, actor, resource, head string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT r.id FROM artifact_releases r
 JOIN JSON_TABLE(r.manifest, '$.artifacts[*]' COLUMNS (
 resource_id VARCHAR(32) PATH '$.resource_id', base_head VARCHAR(32) PATH '$.expected_head',
 generation_id VARCHAR(32) PATH '$.origin.generation.generation_id' NULL ON EMPTY,
 review_required INT PATH '$.origin.review_required' DEFAULT '0' ON EMPTY
 )) AS a ON a.resource_id=? AND a.base_head=?
 WHERE r.project_id=? AND r.created_by=? AND r.status IN ('PREPARED','READY') AND (a.generation_id IS NOT NULL OR a.review_required=1)
 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, resource, head, project, actor).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *IP) compileReleaseOrigins(ctx context.Context, a domain.Actor, project string, manifest *domain.Manifest, updates []domain.ResourceUpdate) error {
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range manifest.Artifacts {
		ref := &manifest.Artifacts[i]
		resource, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return err
		}
		if resource.BranchID != manifest.BranchID || resource.Head != ref.ExpectedHead {
			return domain.ErrConflict
		}
		selection := domain.ArtifactOriginInput{}
		if updates[i].Origin != nil {
			selection = *updates[i].Origin
		}
		origin := domain.ArtifactOrigin{Kind: "manual", ActorID: a.ID, ResourceID: resource.ID, BranchID: resource.BranchID, BaseHead: resource.Head, IPHead: manifestIPHead(*manifest), ContentHash: ref.Hash}
		if err = s.inheritReleaseOrigin(ctx, tx, a, project, resource, selection, &origin); err != nil {
			return err
		}
		if ref.SourceImportID != "" {
			if selection.GenerationID != "" || origin.Generation != nil {
				return fmt.Errorf("导入来源不能清除已有生成血缘: %w", domain.ErrConflict)
			}
			origin.ImportID, origin.ImportHash = ref.SourceImportID, ref.OriginalHash
			origin.Kind = "import"
		}
		if selection.GenerationID != "" {
			historical, err := s.chapterWriterHistoricalOriginTx(ctx, tx, project, resource, a.ID, selection.GenerationID)
			if err != nil {
				return err
			}
			if !historical {
				historical, err = s.researchHistoricalOriginTx(ctx, tx, project, resource, a.ID, selection.GenerationID)
				if err != nil {
					return err
				}
			}
			g, evidence, err := s.releaseGenerationEvidenceTx(ctx, tx, project, manifest.BranchID, selection.GenerationID, !historical)
			if err != nil {
				return err
			}
			if !canControlGeneration(a, g) {
				return domain.ErrForbidden
			}
			if g.Frozen.BaseHead != manifest.ExpectedIPHead {
				return domain.ErrConflict
			}
			origin.Generation = &evidence
			// A fresh call has its own live evidence. Historical recovery remains
			// reachable through ParentReleaseID rather than reclassifying this call.
			origin.Recovery = nil
		}
		if origin.Generation != nil {
			origin.Kind = "generation"
			if origin.Generation.TextHash != ref.Hash || origin.Generation.BaseIPHead != origin.IPHead {
				origin.Kind = "derived"
			}
		}
		origin.ReviewRequired = origin.NeedsIndependentReview()
		ref.Origin = &origin
		if err = origin.ValidateTarget(manifest.BranchID, *ref); err != nil {
			return err
		}
	}
	manifest.PatchParents, err = s.patchParentsTx(ctx, tx, project, *manifest)
	if err != nil {
		return err
	}
	return inheritPeerDeclarationsTx(ctx, tx, project, manifest)
}

func (s *IP) inheritReleaseOrigin(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, resource domain.Resource, selection domain.ArtifactOriginInput, origin *domain.ArtifactOrigin) error {
	parentID := selection.ParentReleaseID
	if parentID == "" {
		parentID = resource.Head
		pending, err := pendingAIOriginTx(ctx, tx, project, a.ID, resource.ID, resource.Head)
		if err != nil {
			return err
		}
		if pending != "" {
			parentID = pending
		}
	}
	if parentID == "" {
		return nil
	}
	parent, err := releaseTx(ctx, tx, project, parentID)
	if err != nil {
		return err
	}
	ref, err := originArtifact(parent, resource.ID)
	if err != nil {
		return err
	}
	if parent.Manifest.BranchID != resource.BranchID || (parent.ID != resource.Head && ref.ExpectedHead != resource.Head) {
		return domain.ErrConflict
	}
	origin.ParentReleaseID, origin.ParentHash = parent.ID, ref.Hash
	origin.Kind = "derived"
	if ref.Origin != nil {
		if err = ref.Origin.ValidateTarget(resource.BranchID, ref); err != nil {
			return err
		}
		origin.Generation = ref.Origin.Generation
		origin.Recovery = append([]domain.ArtifactRecoveryEvidence(nil), ref.Origin.Recovery...)
		origin.ImportID, origin.ImportHash = ref.Origin.ImportID, ref.Origin.ImportHash
	}
	// A caller may select an old manual candidate, but cannot use that selection
	// to remove an AI ancestor already present in the current published resource.
	if resource.Head != "" && parent.ID != resource.Head {
		current, err := releaseTx(ctx, tx, project, resource.Head)
		if err != nil {
			return err
		}
		currentRef, err := originArtifact(current, resource.ID)
		if err != nil {
			return err
		}
		if currentRef.Origin != nil && currentRef.Origin.HasGeneratedContent() && !origin.HasGeneratedContent() {
			return fmt.Errorf("不能移除已发布资料的生成来源: %w", domain.ErrConflict)
		}
	}
	return nil
}

func (s *IP) releaseGenerationEvidenceTx(ctx context.Context, tx *sql.Tx, project, branch, id string, current bool) (Generation, domain.ArtifactGenerationEvidence, error) {
	g, err := generationTx(ctx, tx, project, id)
	evidence := domain.ArtifactGenerationEvidence{}
	if err != nil {
		return g, evidence, err
	}
	if g.BranchID != branch {
		return g, evidence, domain.ErrForbidden
	}
	if g.State != "SUCCEEDED" || g.Cancelled || g.Paused || g.CallID == "" || (g.Frozen.Capability != nil && g.Frozen.Capability.Mode == "EVALUATION") {
		return g, evidence, domain.ErrConflict
	}
	actor, err := s.releaseSourceActorTx(ctx, tx, g.ActorID)
	if err != nil {
		return g, evidence, err
	}
	permission, err := projectTx(ctx, tx, actor, project)
	if err != nil || !domain.CanEdit(actor.Role) || !domain.CanEdit(permission.Role) {
		return g, evidence, domain.ErrForbidden
	}
	call, err := callTx(ctx, tx, g.WorkspaceID, project, g.CallID)
	if err != nil {
		return g, evidence, err
	}
	if call.State != "SUCCEEDED" || call.CancelRequested || call.OutputHash == "" {
		return g, evidence, domain.ErrConflict
	}
	expired, err := runtimeObjectRetentionTx(ctx, tx, project, "model_calls", call.ID, "output_hash", call.OutputHash)
	if err != nil {
		return g, evidence, err
	}
	if expired != nil {
		return g, evidence, domain.ErrMaterialExpired
	}
	var callInput, callActor, operation string
	if err = tx.QueryRowContext(ctx, "SELECT input_hash,actor_id,operation_id FROM model_calls WHERE id=?", call.ID).Scan(&callInput, &callActor, &operation); err != nil {
		return g, evidence, err
	}
	if callActor != g.ActorID || operation != "generation-"+g.ID {
		return g, evidence, domain.ErrConflict
	}
	// Reuse the budget service's pure canonical fingerprint without reserving
	// funds, changing budget state, or requiring paid calls to remain enabled.
	if callInput != modelCallFingerprint(project, g.Frozen.Estimate, g.Frozen.Price.Currency, g.Frozen.InputHash) {
		return g, evidence, domain.ErrConflict
	}
	input, err := s.Blobs.ReadObject(ctx, project, g.Frozen.InputHash)
	if err != nil || digest(string(input)) != g.Frozen.InputHash {
		return g, evidence, domain.ErrNotReady
	}
	raw, err := s.Blobs.ReadObject(ctx, project, call.OutputHash)
	if err != nil || digest(string(raw)) != call.OutputHash {
		return g, evidence, domain.ErrNotReady
	}
	var output models.Result
	if json.Unmarshal(raw, &output) != nil {
		return g, evidence, domain.ErrNotReady
	}
	if current {
		if err = s.lockReleaseQualificationActorTx(ctx, tx, g); err != nil {
			return g, evidence, err
		}
		if err = (&Generations{IP: s}).validateFrozen(ctx, tx, g); err != nil {
			return g, evidence, err
		}
	}
	evidence = domain.ArtifactGenerationEvidence{GenerationID: g.ID, CallID: g.CallID, InputHash: g.Frozen.InputHash, OutputHash: call.OutputHash, TextHash: digest(output.Text), BaseIPHead: g.Frozen.BaseHead, Role: g.Frozen.Role, ConfigID: g.Frozen.Config.ID, ConfigRevision: g.Frozen.Config.Revision, ConfigHash: capabilityDigest(g.Frozen.Config), Mode: "UNQUALIFIED"}
	if c := g.Frozen.Capability; c != nil {
		evidence.Mode = c.Mode
		evidence.AgentVersionID, evidence.ManifestHash = c.AgentVersionID, c.ManifestHash
		evidence.BindingID, evidence.QualificationID = c.BindingID, c.QualificationID
		evidence.TaskKind, evidence.SuiteHash = c.TaskKind, c.SuiteHash
	}
	return g, evidence, nil
}

func (s *IP) validateReleaseOriginTx(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest, ref domain.ArtifactRef) error {
	if ref.Origin == nil {
		return domain.ErrValidation
	}
	origin := ref.Origin
	if err := origin.ValidateTarget(manifest.BranchID, ref); err != nil {
		return err
	}
	if origin.IPHead != manifestIPHead(manifest) {
		return domain.ErrConflict
	}
	if !origin.HasGeneratedContent() {
		// Recheck in the final transaction as well as adoption/publication: a
		// prior manual candidate cannot erase an AI source selected afterwards.
		known, err := pendingAIOriginTx(ctx, tx, project, origin.ActorID, ref.ResourceID, ref.ExpectedHead)
		if err != nil {
			return err
		}
		if known != "" {
			return fmt.Errorf("此资料已有生成来源，请基于来源候选继续编辑: %w", domain.ErrConflict)
		}
		if ref.ExpectedHead != "" {
			parent, err := releaseTx(ctx, tx, project, ref.ExpectedHead)
			if err != nil {
				return err
			}
			prior, err := originArtifact(parent, ref.ResourceID)
			if err != nil {
				return err
			}
			if prior.Origin != nil && prior.Origin.HasGeneratedContent() {
				return domain.ErrConflict
			}
		}
	}
	if origin.ParentReleaseID != "" {
		parent, err := releaseTx(ctx, tx, project, origin.ParentReleaseID)
		if err != nil {
			return err
		}
		p, err := originArtifact(parent, ref.ResourceID)
		if err != nil || p.Hash != origin.ParentHash || parent.Manifest.BranchID != manifest.BranchID {
			return domain.ErrConflict
		}
	}
	if origin.ImportID != "" {
		var sourceHash string
		if err := tx.QueryRowContext(ctx, "SELECT source_hash FROM source_imports WHERE id=? AND project_id=? AND branch_id=?", origin.ImportID, project, manifest.BranchID).Scan(&sourceHash); err != nil || sourceHash != origin.ImportHash {
			return domain.ErrForbidden
		}
		if _, err := s.Blobs.ReadObject(ctx, project, sourceHash); err != nil {
			return domain.ErrNotReady
		}
	}
	if len(origin.Recovery) > 0 {
		latest := origin.Recovery[len(origin.Recovery)-1]
		if latest.ProjectID == project || latest.BranchID == manifest.BranchID {
			return domain.ErrConflict
		}
	}
	if origin.Generation != nil && len(origin.Recovery) == 0 {
		_, actual, err := s.releaseGenerationEvidenceTx(ctx, tx, project, manifest.BranchID, origin.Generation.GenerationID, !origin.ReviewRequired)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(*origin.Generation, actual) {
			return domain.ErrConflict
		}
	}
	return nil
}
