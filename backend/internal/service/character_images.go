package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"strings"
)

type CharacterImageItem struct {
	SourceImport
	Published   bool   `json:"published"`
	NeedsReview string `json:"needs_review,omitempty"`
}

type CharacterImagePage struct {
	Items []CharacterImageItem `json:"items"`
	Next  string               `json:"next"`
}

func requireImageCharacterTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) error {
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return err
	}
	snapshot, err := snapshotTx(ctx, tx, project, b.Head)
	if err != nil {
		return err
	}
	draft, err := ipDraftTx(ctx, tx, actor, project, branch)
	if err != nil {
		return err
	}
	if draft.Revision > 0 {
		snapshot = draft.Snapshot
	}
	for _, entity := range snapshot.Entities {
		if entity.ID == id && entity.Kind == "character" {
			return nil
		}
	}
	return fmt.Errorf("%w：请先保存该角色的设定草稿", domain.ErrConflict)
}

func (s *IP) ImportCharacterImage(ctx context.Context, a domain.Actor, project, branch, name, op string, input retrieval.CharacterImage, data []byte) (SourceImport, error) {
	if input.Generated != nil {
		return SourceImport{}, domain.ErrValidation
	}
	if !validName(name, 160) || !validName(op, 120) || strings.ContainsAny(name, "/\\\x00") {
		return SourceImport{}, domain.ErrValidation
	}
	// Permission precedes image decoding; transaction rechecks before storage.
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return SourceImport{}, err
	}
	_, err = branchTx(ctx, tx, project, branch)
	tx.Rollback()
	if err != nil {
		return SourceImport{}, err
	}
	report, err := retrieval.InspectCharacterImage(input, data)
	if err != nil {
		return SourceImport{}, fmt.Errorf("%w：请上传完整的PNG/JPEG/WebP图片（16MB以内、2400万像素以内），填写角色、造型与来源说明", domain.ErrValidation)
	}
	metadata, err := json.Marshal(report.CharacterImage)
	if err != nil {
		return SourceImport{}, err
	}
	return s.storeSourceImport(ctx, a, project, branch, name, op, data, report, string(metadata))
}

// Published references are project-visible. Unadopted uploads stay with their
// author, exactly like other source imports; knowing a blob hash grants nothing.
func visualImageTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (CharacterImageItem, error) {
	var result CharacterImageItem
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT i.id,i.filename,i.source_hash,i.source_size,i.report,
	 EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED')
	 FROM source_imports i WHERE i.id=? AND i.project_id=? AND i.branch_id=? AND
	 (i.actor_id=? OR EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED'))`, id, project, branch, actor).Scan(&result.ID, &result.Name, &result.Hash, &result.Size, &raw, &result.Published)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	if err = rejectExpiredRuntime(raw, "source_imports.report"); err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result.Report); err != nil {
		return result, err
	}
	if (result.Report.Parser != "character-image-v1" || result.Report.CharacterImage == nil) && (result.Report.Parser != "novel-image-v1" || result.Report.NovelImage == nil) {
		return result, domain.ErrValidation
	}
	if err = requireRuntimeObjectTx(ctx, tx, project, "source_imports", id, "source_hash", result.Hash); err != nil {
		return result, err
	}
	return result, nil
}

func characterImageTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (CharacterImageItem, error) {
	v, err := visualImageTx(ctx, tx, actor, project, branch, id)
	if err == nil && v.Report.CharacterImage == nil {
		err = domain.ErrValidation
	}
	return v, err
}
func (s *IP) CharacterImages(ctx context.Context, a domain.Actor, project, branch, character, after string) (CharacterImagePage, error) {
	result := CharacterImagePage{Items: []CharacterImageItem{}}
	if character == "" || len(character) > 640 || len(after) > 64 {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return result, err
	}
	ids, err := queryIDs(ctx, tx, `SELECT i.id FROM source_imports i WHERE i.project_id=? AND i.branch_id=? AND i.id>?
	 AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.parser'))='character-image-v1'
	 AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.character_image.character_id'))=?
	 AND (i.actor_id=? OR EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED')) ORDER BY i.id LIMIT 101`, project, branch, after, character, a.ID)
	if err != nil {
		return result, err
	}
	if len(ids) > 100 {
		ids = ids[:100]
		result.Next = ids[99]
	}
	for _, id := range ids {
		item, err := characterImageTx(ctx, tx, a.ID, project, branch, id)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s *IP) ReadCharacterImage(ctx context.Context, a domain.Actor, project, branch, id string) (CharacterImageItem, []byte, error) {
	return s.readVisualImage(ctx, a, project, branch, id, true)
}
func (s *IP) readVisualImage(ctx context.Context, a domain.Actor, project, branch, id string, characterOnly bool) (CharacterImageItem, []byte, error) {
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return CharacterImageItem{}, nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return CharacterImageItem{}, nil, err
	}
	item, err := visualImageTx(ctx, tx, a.ID, project, branch, id)
	if err == nil && characterOnly && item.Report.CharacterImage == nil {
		err = domain.ErrValidation
	}
	if err != nil {
		return item, nil, err
	}
	data, err := s.Blobs.ReadObject(ctx, project, item.Hash)
	if err != nil || int64(len(data)) != item.Size || digest(string(data)) != item.Hash {
		return item, nil, domain.ErrNotReady
	}
	return item, data, nil
}

type CharacterImageCandidateInput struct {
	OperationID     string               `json:"operation_id"`
	ExpectedIPHead  string               `json:"expected_ip_head"`
	CharacterSource domain.DependencyRef `json:"character_source"`
}

func (s *IP) CharacterPortrait(ctx context.Context, a domain.Actor, project, branch, character string) (string, error) {
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT i.id FROM source_imports i WHERE i.project_id=? AND i.branch_id=?
	 AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.parser'))='character-image-v1'
	 AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.character_image.character_id'))=?
	 AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.character_image.view'))='portrait'
	 AND EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED')
	 ORDER BY i.created_at DESC,i.id DESC LIMIT 1`, project, branch, character).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *IP) PrepareCharacterImage(ctx context.Context, a domain.Actor, project, branch, id string, input CharacterImageCandidateInput) (domain.Release, error) {
	var empty domain.Release
	if !validName(input.OperationID, 120) {
		return empty, domain.ErrValidation
	}
	source, err := s.ReadImport(ctx, a, project, branch, id)
	if err != nil {
		return empty, err
	}
	metadata := source.Report.CharacterImage
	if source.Report.Parser != "character-image-v1" || metadata == nil || input.CharacterSource.Kind != "entity" || input.CharacterSource.BranchID != branch || input.CharacterSource.ID != metadata.CharacterID || input.CharacterSource.Revision != input.ExpectedIPHead {
		return empty, domain.ErrValidation
	}
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return empty, err
	}
	snapshot, err := snapshotTx(ctx, tx, project, input.ExpectedIPHead)
	if err == nil {
		err = s.validateGeneratedImageTx(ctx, tx, project, branch, id, metadata, snapshot)
	}
	tx.Rollback()
	if err != nil {
		return empty, err
	}
	characterFound := false
	for _, entity := range snapshot.Entities {
		if entity.ID == metadata.CharacterID && entity.Kind == "character" {
			characterFound = true
		}
	}
	if !characterFound {
		return empty, domain.ErrConflict
	}
	dependencies := []domain.Dependency{{Kind: "representation", Reason: "角色造型参考，人物变更需要复核", Source: input.CharacterSource}}
	// Visual references depend on the character's actual assertions as well as
	// its identity, so changes to clothing/appearance do not disappear from impact.
	for _, assertion := range snapshot.Assertions {
		if assertion.Subject == metadata.CharacterID {
			dependencies = append(dependencies, domain.Dependency{Kind: "representation", Reason: "角色设定影响视觉参考", Source: domain.DependencyRef{Kind: "assertion", BranchID: branch, ID: assertion.ID, Revision: input.ExpectedIPHead, Hash: valueHash(assertion)}})
		}
	}
	// The caller pins the precise published entity it reviewed. Generic release
	// validation checks that version/hash at save, adoption, and publication.
	resource, err := s.CreateResource(ctx, a, project, ResourceInput{BranchID: branch, Name: "角色视觉 · " + metadata.Look + " · " + metadata.View, Kind: "source", OperationID: "character-image-" + id})
	if err != nil {
		return empty, err
	}
	text, _ := json.Marshal(metadata)
	return s.PrepareRelease(ctx, a, project, domain.ReleaseInput{
		BranchID: branch, ExpectedIPHead: input.ExpectedIPHead, OperationID: input.OperationID,
		Updates: []domain.ResourceUpdate{{ResourceID: resource.ID, Text: string(text), SourceImportID: id,
			Dependencies: &domain.DependencyPolicy{Inputs: dependencies}}},
	})
}
