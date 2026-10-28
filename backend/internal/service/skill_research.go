package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

type SkillFetch struct {
	OperationID string               `json:"operation_id"`
	Source      skills.RemoteRequest `json:"source"`
}
type SkillMerge struct {
	OperationID string            `json:"operation_id"`
	BaseID      string            `json:"base_id"`
	UpstreamID  string            `json:"upstream_id"`
	LocalID     string            `json:"local_id"`
	Choices     map[string]string `json:"choices"`
}
type SkillMergePreview struct {
	Conflicts []skills.Conflict `json:"conflicts"`
	Report    skills.Report     `json:"report"`
}

func (s *Skills) Fetch(ctx context.Context, a domain.Actor, project string, input SkillFetch) (SkillVersion, error) {
	if !validName(input.OperationID, 120) {
		return SkillVersion{}, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return SkillVersion{}, err
	}
	// A retried discovery operation returns its pinned receipt even if main moved.
	requestRaw, _ := json.Marshal(input.Source)
	requestHash := digest("skill.fetch\x00" + project + "\x00" + string(requestRaw))
	var previous, id string
	previous, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if previous != requestHash {
			tx.Rollback()
			return SkillVersion{}, domain.ErrConflict
		}
		tx.Rollback()
		return s.Read(ctx, a, project, id)
	}
	tx.Rollback()
	if err != sql.ErrNoRows {
		return SkillVersion{}, err
	}
	bundle, source, err := skills.NewFetcher(nil).Fetch(ctx, input.Source)
	if err != nil {
		return SkillVersion{}, domain.ErrNotReady
	}
	return s.save(ctx, a, project, input.OperationID, "", "", bundle, SkillSource{Kind: "github", Repository: source.Repository, Commit: source.Commit, Path: source.Path, URL: source.URL}, requestHash)
}

func (s *Skills) merge(ctx context.Context, a domain.Actor, project string, input SkillMerge) (skills.Bundle, SkillMergePreview, SkillVersion, error) {
	base, b, err := s.bundle(ctx, a, project, input.BaseID)
	if err != nil {
		return b, SkillMergePreview{}, base, err
	}
	up, u, err := s.bundle(ctx, a, project, input.UpstreamID)
	if err != nil {
		return b, SkillMergePreview{}, base, err
	}
	local, l, err := s.bundle(ctx, a, project, input.LocalID)
	if err != nil {
		return b, SkillMergePreview{}, base, err
	}
	if !base.Available || !up.Available || !local.Available {
		return b, SkillMergePreview{}, local, domain.ErrNotReady
	}
	if base.Report.Name != up.Report.Name || base.Report.Name != local.Report.Name {
		return b, SkillMergePreview{}, local, domain.ErrValidation
	}
	// The declared base must be in the local derivation chain, not an unrelated name match.
	cursor := local.ID
	found := false
	for count := 0; cursor != "" && count < 128; count++ {
		if cursor == base.ID {
			found = true
			break
		}
		ancestor, readErr := s.Read(ctx, a, project, cursor)
		if readErr != nil {
			return b, SkillMergePreview{}, local, readErr
		}
		cursor = ancestor.ParentID
	}
	if !found {
		return b, SkillMergePreview{}, local, domain.ErrValidation
	}
	merged, conflicts, err := skills.Merge(b, u, l, input.Choices)
	if err != nil {
		return merged, SkillMergePreview{}, local, domain.ErrValidation
	}
	report, err := skills.Analyze(merged)
	if err != nil {
		return merged, SkillMergePreview{}, local, domain.ErrValidation
	}
	return merged, SkillMergePreview{Conflicts: conflicts, Report: report}, local, nil
}
func (s *Skills) PreviewMerge(ctx context.Context, a domain.Actor, project string, input SkillMerge) (SkillMergePreview, error) {
	_, preview, _, err := s.merge(ctx, a, project, input)
	return preview, err
}
func (s *Skills) Merge(ctx context.Context, a domain.Actor, project string, input SkillMerge) (SkillVersion, error) {
	b, preview, local, err := s.merge(ctx, a, project, input)
	if err != nil {
		return SkillVersion{}, err
	}
	if len(preview.Conflicts) > 0 {
		return SkillVersion{}, domain.ErrConflict
	}
	return s.save(ctx, a, project, input.OperationID, input.LocalID, input.UpstreamID, b, local.Source)
}
