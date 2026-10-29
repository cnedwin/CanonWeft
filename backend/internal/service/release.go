package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
	"unicode/utf8"
)

func releaseTx(ctx context.Context, tx *sql.Tx, project, id string) (domain.Release, error) {
	var release domain.Release
	var payload []byte
	err := tx.QueryRowContext(ctx, "SELECT id,content_hash,status,manifest FROM artifact_releases WHERE id=? AND project_id=? FOR UPDATE", id, project).Scan(&release.ID, &release.Hash, &release.Status, &payload)
	if err == sql.ErrNoRows {
		return release, domain.ErrForbidden
	}
	if err != nil {
		return release, err
	}
	if err = json.Unmarshal(payload, &release.Manifest); err != nil {
		return release, err
	}
	state, err := publicationRetentionTx(ctx, tx, project, "artifact_releases", id, release.Hash, payload)
	if err != nil {
		return release, err
	}
	if state != nil {
		return release, domain.ErrMaterialExpired
	}
	encoded, err := json.Marshal(release.Manifest)
	if err != nil {
		return release, err
	}
	if digest(string(encoded)) != release.Hash {
		return release, domain.ErrNotReady
	}
	if release.Manifest.ReleaseID != "" && release.Manifest.ReleaseID != release.ID {
		return release, domain.ErrNotReady
	}
	for _, ref := range release.Manifest.Artifacts {
		if ref.Origin != nil && ref.Origin.ReviewRequired {
			source := ""
			if ref.Origin.Generation != nil {
				source = ref.Origin.Generation.GenerationID
			}
			release.ReviewTargets = append(release.ReviewTargets, domain.ArtifactReviewTarget{ReleaseID: release.ID, ManifestHash: release.Hash, ResourceID: ref.ResourceID, BranchID: release.Manifest.BranchID, ExpectedHead: ref.ExpectedHead, IPHead: ref.Origin.IPHead, ContentHash: ref.Hash, SourceGenerationID: source})
		}
	}
	return release, nil
}

func (s *IP) PrepareRelease(ctx context.Context, a domain.Actor, project string, input domain.ReleaseInput) (domain.Release, error) {
	return retryReleaseSourceAuthority(ctx, func() (domain.Release, error) {
		return s.prepareRelease(ctx, a, project, input)
	})
}

func (s *IP) prepareRelease(ctx context.Context, a domain.Actor, project string, input domain.ReleaseInput, changePlans ...string) (domain.Release, error) {
	return s.prepareReleaseWithComposition(ctx, a, project, input, nil, changePlans...)
}

func (s *IP) prepareReleaseWithComposition(ctx context.Context, a domain.Actor, project string, input domain.ReleaseInput, composition *ReleaseCompositionInput, changePlans ...string) (domain.Release, error) {
	var release domain.Release
	if !validName(input.OperationID, 120) || len(input.Updates) == 0 || len(input.Updates) > 32 {
		return release, domain.ErrValidation
	}
	permission, err := s.Workspace.Project(ctx, a, project)
	if err != nil {
		return release, err
	}
	if !domain.CanEdit(permission.Role) || !domain.CanEdit(a.Role) {
		return release, domain.ErrForbidden
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return release, err
	}
	if len(raw) > 1024*1024 {
		return release, domain.ErrValidation
	}
	if composition != nil {
		raw, err = json.Marshal(struct {
			Input       domain.ReleaseInput
			Composition ReleaseCompositionInput
		}{input, *composition})
		if err != nil {
			return release, err
		}
	}
	requestHash := digest("release.prepare\x00" + project + "\x00" + string(raw))
	// An already saved operation must be replayable after its prospective inputs
	// have been published. Do not re-resolve the original placeholder request.
	priorTx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return release, err
	}
	var priorHash, priorID string
	priorHash, priorID, priorErr := operationReceiptTx(ctx, priorTx, a, input.OperationID)
	if priorErr == nil {
		if priorHash != requestHash {
			priorTx.Rollback()
			return release, domain.ErrConflict
		}
		prior, readErr := releaseTx(ctx, priorTx, project, priorID)
		priorTx.Rollback()
		return prior, readErr
	}
	priorTx.Rollback()
	if priorErr != sql.ErrNoRows {
		return release, priorErr
	}
	manifest := domain.Manifest{Schema: 2, BranchID: input.BranchID, ExpectedIPHead: input.ExpectedIPHead, IPCandidateID: input.IPCandidateID, Artifacts: []domain.ArtifactRef{}}
	manifest.PatchGroup = input.PatchGroup
	if len(input.PeerReleases) > 15 {
		return release, domain.ErrValidation
	}
	for _, peer := range input.PeerReleases {
		manifest.PeerReleases = append(manifest.PeerReleases, peer.ReleaseID)
	}
	if composition != nil {
		for _, pin := range composition.Releases {
			manifest.CompositionParents = append(manifest.CompositionParents, pin.ReleaseID)
		}
	}
	seen := map[string]bool{}
	// Compile immutable identities before the authority transaction. Physical
	// writes happen under the same locks as their saved manifest reference.
	for _, update := range input.Updates {
		if update.ResourceID == "" || seen[update.ResourceID] || !utf8.ValidString(update.Text) || len(update.Text) > 256*1024 {
			return release, domain.ErrValidation
		}
		seen[update.ResourceID] = true
		ref := domain.ArtifactRef{ResourceID: update.ResourceID, ExpectedHead: update.ExpectedHead, Hash: digest(update.Text), Size: int64(len(update.Text))}
		ref.Policy = update.Policy
		ref.Dependencies = update.Dependencies
		ref.FrozenChangeReason = update.FrozenChangeReason
		if ref.Dependencies == nil {
			current, readErr := s.ReadResource(ctx, a, project, update.ResourceID)
			if readErr != nil {
				return release, readErr
			}
			ref.Dependencies = current.Dependencies
		}
		if ref.Policy == nil {
			current, readErr := s.ReadResource(ctx, a, project, update.ResourceID)
			if readErr != nil {
				return release, readErr
			}
			ref.Policy = current.Policy
		}
		if ref.Policy != nil && ref.Policy.Validate() != nil {
			return release, domain.ErrValidation
		}
		if update.SourceImportID != "" {
			source, sourceErr := s.ReadImport(ctx, a, project, input.BranchID, update.SourceImportID)
			if sourceErr != nil {
				return release, sourceErr
			}
			ref.SourceImportID = source.ID
			ref.OriginalHash = source.Hash
			if !source.Report.Complete && !update.PartialAcknowledged {
				return release, domain.ErrValidation
			}
			ref.PartialAcknowledged = update.PartialAcknowledged
		}
		manifest.Artifacts = append(manifest.Artifacts, ref)
	}
	releaseID := NewID()
	manifest.ReleaseID = releaseID
	if err = s.resolveProspectiveDependencies(ctx, a, project, &manifest); err != nil {
		return release, err
	}
	if err = s.compileReleaseOrigins(ctx, a, project, &manifest, input.Updates); err != nil {
		return release, err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return release, err
	}
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return release, err
	}
	defer tx.Rollback()
	var stored, result string
	stored, result, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if stored != requestHash {
			return release, domain.ErrConflict
		}
		return releaseTx(ctx, tx, project, result)
	}
	if err != sql.ErrNoRows {
		return release, err
	}
	for _, planID := range changePlans {
		if _, err = currentChangePlanTx(ctx, tx, project, input.BranchID, planID); err != nil {
			return release, err
		}
	}
	for _, pin := range input.PeerReleases {
		peer, readErr := releaseTx(ctx, tx, project, pin.ReleaseID)
		if readErr != nil {
			return release, readErr
		}
		if peer.Hash != pin.ManifestHash {
			return release, domain.ErrConflict
		}
	}
	if composition != nil {
		for _, pin := range composition.Releases {
			parent, readErr := releaseTx(ctx, tx, project, pin.ReleaseID)
			if readErr != nil {
				return release, readErr
			}
			if parent.Hash != pin.ManifestHash {
				return release, domain.ErrConflict
			}
		}
	}
	for _, update := range input.Updates {
		if _, err := objectwriter.Write(ctx, tx, s.Blobs, project, []byte(update.Text)); err != nil {
			return release, err
		}
	}
	manifestObject, err := objectwriter.Write(ctx, tx, s.Blobs, project, encoded)
	if err != nil {
		return release, err
	}
	if err = s.validateManifest(ctx, tx, project, manifest); err != nil {
		return release, err
	}
	release = domain.Release{ID: releaseID, Hash: manifestObject.Hash, Status: "PREPARED", Manifest: manifest}
	_, err = tx.ExecContext(ctx, "INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,created_by) VALUES(?,?,?,?,?,?)", release.ID, project, manifest.BranchID, encoded, release.Hash, a.ID)
	if err != nil {
		return release, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'release.prepare',?,?)", a.ID, input.OperationID, requestHash, release.ID)
	if err != nil {
		return release, err
	}
	for _, ref := range manifest.Artifacts {
		if ref.SourceImportID != "" {
			if _, err = tx.ExecContext(ctx, "INSERT INTO source_import_candidates(import_id,resource_id,release_id) VALUES(?,?,?)", ref.SourceImportID, ref.ResourceID, release.ID); err != nil {
				return release, err
			}
		}
	}
	if err = audit(ctx, tx, a, "release.prepare", release.ID); err != nil {
		return release, err
	}
	if err = s.captureReleaseSourceTx(ctx, tx, project, release.ID); err != nil {
		return release, err
	}
	return release, tx.Commit()
}
func (s *IP) validateManifest(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest) error {
	if manifest.Schema != 2 {
		return fmt.Errorf("旧清单仅供查看，请保存包含来源的新候选: %w", domain.ErrConflict)
	}
	if len(manifest.Artifacts) == 0 {
		return domain.ErrValidation
	}
	branch, err := branchTx(ctx, tx, project, manifest.BranchID)
	if err != nil {
		return err
	}
	if branch.Head != manifest.ExpectedIPHead {
		return domain.ErrConflict
	}
	if err = s.validatePatchManifestTx(ctx, tx, project, manifest); err != nil {
		return err
	}
	if err = s.validatePatchParentsTx(ctx, tx, project, manifest); err != nil {
		return err
	}
	if err = validateCompositionParentsTx(ctx, tx, project, manifest); err != nil {
		return err
	}
	if _, err = releasePeersTx(ctx, tx, project, manifest); err != nil {
		return err
	}
	snapshot, err := snapshotTx(ctx, tx, project, branch.Head)
	if err != nil {
		return err
	}
	if manifest.IPCandidateID != "" {
		candidate, readErr := candidateTx(ctx, tx, manifest.BranchID, manifest.IPCandidateID)
		if readErr != nil {
			return readErr
		}
		if candidate.BaseHead != branch.Head || candidate.Status != "READY" {
			return domain.ErrConflict
		}
		if _, readErr = validateCandidateAncestryTx(ctx, tx, project, branch, candidate); readErr != nil {
			return readErr
		}
		snapshot = candidate.Snapshot
	}
	for _, ref := range manifest.Artifacts {
		if err := validPolicyReferences(ref.Policy, snapshot); err != nil {
			return err
		}
		if ref.SourceImportID != "" {
			var sourceHash string
			var sourceReport []byte
			if err := tx.QueryRowContext(ctx, "SELECT source_hash,report FROM source_imports WHERE id=? AND project_id=? AND branch_id=?", ref.SourceImportID, project, manifest.BranchID).Scan(&sourceHash, &sourceReport); err != nil || sourceHash != ref.OriginalHash {
				return domain.ErrForbidden
			}
			if err := s.validateGeneratedImageReportTx(ctx, tx, project, manifest.BranchID, ref.SourceImportID, sourceReport, snapshot); err != nil {
				return err
			}
			if _, err := s.Blobs.ReadObject(ctx, project, sourceHash); err != nil {
				return domain.ErrNotReady
			}
		}
		resource, readErr := resourceTx(ctx, tx, project, ref.ResourceID)
		if readErr != nil {
			return readErr
		}
		if resource.BranchID != manifest.BranchID {
			return domain.ErrForbidden
		}
		if resource.Kind == "novel_review_dispositions" {
			return fmt.Errorf("%w：作者审稿整改记录不能发布为作品或资料", domain.ErrValidation)
		}
		if ref.Policy != nil && ref.Policy.Withdrawn && resource.Kind != "note" && resource.Kind != "source" {
			return domain.ErrValidation
		}
		if resource.Head != ref.ExpectedHead {
			return domain.ErrConflict
		}
		if err := s.validateArtifactDependencies(ctx, tx, project, manifest, resource, ref); err != nil {
			return err
		}
		if err := s.validateReleaseOriginTx(ctx, tx, project, manifest, ref); err != nil {
			return err
		}
		data, readErr := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if readErr != nil || int64(len(data)) != ref.Size {
			return domain.ErrNotReady
		}
		if err := s.validateNovelPlanArtifact(ctx, tx, project, resource, ref, data); err != nil {
			return err
		}
	}
	return s.validateNovelChapterManifestTx(ctx, tx, project, manifest)
}
func (s *IP) AdoptRelease(ctx context.Context, a domain.Actor, project, id string) (domain.Release, error) {
	return retryReleaseSourceAuthority(ctx, func() (domain.Release, error) {
		return s.adoptRelease(ctx, a, project, id)
	})
}

func (s *IP) adoptRelease(ctx context.Context, a domain.Actor, project, id string) (domain.Release, error) {
	var release domain.Release
	tx, a, p, err := s.begin(ctx, a, project, false)
	if err != nil {
		return release, err
	}
	defer tx.Rollback()
	if a.Role == "reader" || p.Role == "reader" {
		return release, domain.ErrForbidden
	}
	release, err = releaseTx(ctx, tx, project, id)
	if err != nil {
		return release, err
	}
	if release.Status == "PUBLISHED" {
		return release, nil
	}
	if err = s.requireReleaseSemanticReviewTx(ctx, tx, project, release); err != nil {
		return release, err
	}
	if err = s.validateManifest(ctx, tx, project, release.Manifest); err != nil {
		return release, err
	}
	if _, err = s.Blobs.ReadObject(ctx, project, release.Hash); err != nil {
		return release, domain.ErrNotReady
	}
	if release.Status == "READY" {
		return release, nil
	}
	for _, ref := range release.Manifest.Artifacts {
		if ref.FrozenChangeReason != "" && (a.Role != "owner" || p.Role != "owner") {
			return release, domain.ErrForbidden
		}
	}
	if err = s.captureReleaseSourceTx(ctx, tx, project, id); err != nil {
		return release, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE artifact_releases SET status='READY',adopted_by=?,adopted_hash=? WHERE id=?", a.ID, release.Hash, id)
	if err != nil {
		return release, err
	}
	if err = audit(ctx, tx, a, "release.adopt", id); err != nil {
		return release, err
	}
	if err = s.captureReleaseSourceTx(ctx, tx, project, id); err != nil {
		return release, err
	}
	release.Status = "READY"
	return release, tx.Commit()
}
func (s *IP) PublishRelease(ctx context.Context, a domain.Actor, project, id string) (domain.Release, error) {
	return retryReleaseSourceAuthority(ctx, func() (domain.Release, error) {
		return s.publishRelease(ctx, a, project, id)
	})
}

func (s *IP) publishRelease(ctx context.Context, a domain.Actor, project, id string) (domain.Release, error) {
	var release domain.Release
	if err := s.preparePublicationIndex(ctx, a, project, "", id, ""); err != nil {
		return release, err
	}
	tx, a, p, err := s.begin(ctx, a, project, false)
	if err != nil {
		return release, err
	}
	defer tx.Rollback()
	if (a.Role != "owner" && a.Role != "publisher") || (p.Role != "owner" && p.Role != "publisher") {
		return release, domain.ErrForbidden
	}
	release, err = releaseTx(ctx, tx, project, id)
	if err != nil {
		return release, err
	}
	if release.Status == "PUBLISHED" {
		return release, nil
	}
	prepared, err := s.prepareReleasePublicationTx(ctx, tx, project, release)
	if err != nil {
		return release, err
	}
	release, err = s.applyReleasePublicationTx(ctx, tx, a, project, prepared)
	if err != nil {
		return release, err
	}
	return release, tx.Commit()
}

type releasePublication struct {
	release       domain.Release
	candidate     *candidatePublication
	chapterMemory []memory.Item
}

func (s *IP) prepareReleasePublicationTx(ctx context.Context, tx *sql.Tx, project string, release domain.Release) (releasePublication, error) {
	return s.prepareReleasePublicationWithPeersTx(ctx, tx, project, release, nil)
}

func (s *IP) prepareReleasePublicationWithPeersTx(ctx context.Context, tx *sql.Tx, project string, release domain.Release, group map[string]domain.Release) (releasePublication, error) {
	prepared := releasePublication{release: release}
	id := release.ID
	var err error
	if release.Status != "READY" {
		return prepared, domain.ErrConflict
	}
	peers, err := releasePeersTx(ctx, tx, project, release.Manifest)
	if err != nil {
		return prepared, err
	}
	for _, peer := range peers {
		if peer.Status == "PUBLISHED" {
			continue
		}
		member, exists := group[peer.ID]
		if !exists || member.Hash != peer.Hash || member.Status != "READY" {
			return prepared, fmt.Errorf("%w：未发布的上游候选必须一起发布", domain.ErrConflict)
		}
	}
	if err = s.requireReleaseSemanticReviewTx(ctx, tx, project, release); err != nil {
		return prepared, err
	}
	if err = s.requirePublicationIndex(ctx, tx, project, "", id, ""); err != nil {
		return prepared, err
	}
	var adopted sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT adopted_hash FROM artifact_releases WHERE id=?", id).Scan(&adopted); err != nil {
		return prepared, err
	}
	if !adopted.Valid || adopted.String != release.Hash {
		return prepared, domain.ErrConflict
	}
	if err = s.validateManifest(ctx, tx, project, release.Manifest); err != nil {
		return prepared, err
	}
	prepared.chapterMemory, err = s.chapterMemoryForReleaseTx(ctx, tx, project, release)
	if err != nil {
		return prepared, err
	}
	if _, err = s.Blobs.ReadObject(ctx, project, release.Hash); err != nil {
		return prepared, domain.ErrNotReady
	}
	if err = s.captureReleaseSourceTx(ctx, tx, project, id); err != nil {
		return prepared, err
	}
	if release.Manifest.IPCandidateID != "" {
		candidate, candidateErr := s.prepareCandidatePublicationTx(ctx, tx, project, release.Manifest.BranchID, release.Manifest.IPCandidateID)
		if candidateErr != nil {
			return prepared, candidateErr
		}
		prepared.candidate = &candidate
	}
	return prepared, nil
}

func (s *IP) applyReleasePublicationTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, prepared releasePublication) (domain.Release, error) {
	release := prepared.release
	id := release.ID
	var err error
	if prepared.candidate != nil {
		if _, err = s.applyCandidatePublicationTx(ctx, tx, a, project, release.Manifest.BranchID, *prepared.candidate); err != nil {
			return release, err
		}
	}
	for _, ref := range release.Manifest.Artifacts {
		if err = s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "resources", Primary: map[string]string{"id": ref.ResourceID}}); err != nil {
			return release, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE resources SET head_revision=?,content_hash=?,revision=revision+1 WHERE id=? AND project_id=?", id, ref.Hash, ref.ResourceID, project)
		if err != nil {
			return release, err
		}
		if err = s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "resources", Primary: map[string]string{"id": ref.ResourceID}}); err != nil {
			return release, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE artifact_releases SET status='PUBLISHED' WHERE id=?", id); err != nil {
		return release, err
	}
	mem := &Memory{IP: s}
	memo := map[string]memorySourceInfo{}
	for _, item := range prepared.chapterMemory {
		if _, err = mem.validateSourcesMemo(ctx, tx, project, release.Manifest.BranchID, item, memo); err != nil {
			return release, err
		}
		if err = mem.persist(ctx, tx, a, item, "", "chapter-memory-"+item.ID, valueHash(item)); err != nil {
			return release, err
		}
	}
	if err = s.rebasePublishedChapterDraftsTx(ctx, tx, project, release); err != nil {
		return release, err
	}
	event, err := json.Marshal(release.Manifest)
	if err != nil {
		return release, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'release.published',?,?)", NewID(), project, id, event)
	if err != nil {
		return release, err
	}
	if err = audit(ctx, tx, a, "release.publish", id); err != nil {
		return release, err
	}
	if err = s.captureReleaseSourceTx(ctx, tx, project, id); err != nil {
		return release, err
	}
	release.Status = "PUBLISHED"
	return release, nil
}
func (s *IP) ReadRelease(ctx context.Context, a domain.Actor, project, id string) (domain.Release, error) {
	var release domain.Release
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return release, err
	}
	defer tx.Rollback()
	release, err = releaseTx(ctx, tx, project, id)
	if err != nil {
		return release, err
	}
	release.Contents = []domain.ResourceUpdate{}
	for _, ref := range release.Manifest.Artifacts {
		data, readErr := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if readErr != nil {
			return release, fmt.Errorf("版本内容不可读取: %w", domain.ErrNotReady)
		}
		// The full server evidence is on the manifest. The update carries only the
		// parent selection, so editing this result never supplies authority fields.
		origin := &domain.ArtifactOriginInput{ParentReleaseID: release.ID}
		release.Contents = append(release.Contents, domain.ResourceUpdate{Origin: origin, ResourceID: ref.ResourceID, ExpectedHead: ref.ExpectedHead, Text: string(data), Policy: ref.Policy, Dependencies: ref.Dependencies, FrozenChangeReason: ref.FrozenChangeReason})
	}
	if release.Manifest.Schema == 2 && release.Status != "PUBLISHED" {
		release.RequiresSemanticReview, err = s.releaseSemanticRequirementTx(ctx, tx, project, release)
		if err != nil {
			return release, err
		}
	}
	return release, nil
}
func (s *IP) Releases(ctx context.Context, a domain.Actor, project, branch string) ([]domain.Release, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,content_hash,status,manifest FROM artifact_releases WHERE project_id=? AND branch_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.Release{}
	payloads := map[string][]byte{}
	for rows.Next() {
		var release domain.Release
		var payload []byte
		if err = rows.Scan(&release.ID, &release.Hash, &release.Status, &payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &release.Manifest); err != nil {
			return nil, err
		}
		items = append(items, release)
		payloads[release.ID] = payload
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Retention, err = publicationRetentionTx(ctx, tx, project, "artifact_releases", items[i].ID, items[i].Hash, payloads[items[i].ID])
		if err != nil {
			return nil, err
		}
		if items[i].Retention != nil {
			items[i].Manifest = domain.Manifest{Artifacts: []domain.ArtifactRef{}}
		}
	}
	return items, nil
}
