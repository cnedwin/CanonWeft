package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelChapterBasisSide struct {
	DetailRef        domain.DependencyRef    `json:"detail_ref"`
	Detail           *novel.PlanningDocument `json:"detail"`
	SettingsRevision int64                   `json:"settings_revision"`
	SettingsHash     string                  `json:"settings_hash"`
	IPHead           string                  `json:"ip_head"`
}

type NovelChapterBasisPreview struct {
	ResourceID      string                        `json:"resource_id"`
	ChapterID       string                        `json:"chapter_id"`
	Revision        int64                         `json:"revision"`
	Hash            string                        `json:"hash"`
	BaseHead        string                        `json:"base_head"`
	CurrentHead     string                        `json:"current_head"`
	BasisHash       string                        `json:"basis_hash"`
	CanRefresh      bool                          `json:"can_refresh"`
	Reason          string                        `json:"reason"`
	Before          NovelChapterBasisSide         `json:"before"`
	After           NovelChapterBasisSide         `json:"after"`
	SettingsChanges []novel.SettingChange         `json:"settings_changes"`
	IPChanges       []domain.DependencyDifference `json:"ip_changes"`
	Limitations     []string                      `json:"limitations"`
}

// BasisPreview reads only the current author's saved chapter and its exact
// historical authority pins. It creates no draft, receipt, audit, or model task.
func (s *NovelChapters) BasisPreview(ctx context.Context, actor, bookID, id string) (NovelChapterBasisPreview, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelChapterBasisPreview{}, err
	}
	defer tx.Rollback()
	draft, err := s.draftTx(ctx, tx, a, book, id)
	if err != nil {
		return NovelChapterBasisPreview{}, err
	}
	return s.basisPreviewTx(ctx, tx, book, draft)
}

func basisUnavailable(err error) bool {
	return errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotReady) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrMaterialExpired) || errors.Is(err, sql.ErrNoRows)
}

func (s *NovelChapters) basisPreviewTx(ctx context.Context, tx *sql.Tx, book NovelBook, draft NovelChapterDraft) (NovelChapterBasisPreview, error) {
	d := draft.Document
	out := NovelChapterBasisPreview{
		ResourceID: draft.Resource.ID, ChapterID: d.ChapterID, Revision: draft.Revision, Hash: draft.Hash,
		BaseHead: draft.BaseHead, CurrentHead: draft.Resource.Head,
		Before:          NovelChapterBasisSide{DetailRef: d.Detail, SettingsRevision: d.SettingsRevision, SettingsHash: d.SettingsHash, IPHead: d.IPHead},
		SettingsChanges: []novel.SettingChange{}, IPChanges: []domain.DependencyDifference{}, Limitations: []string{},
	}
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	out.After = NovelChapterBasisSide{SettingsRevision: check.settingsRevision, SettingsHash: check.settingsHash, IPHead: check.head}
	target, detail, targetErr := check.current("detail", d.ChapterID)
	if target.ID != "" {
		out.After.DetailRef = planPin(target)
		if detail.Schema == 1 {
			out.After.Detail = &detail
		}
	}
	if targetErr != nil {
		if !basisUnavailable(targetErr) && !errors.Is(targetErr, domain.ErrValidation) {
			return out, targetErr
		}
		out.Reason = "当前细纲或其上游规划尚未有效确认，请先处理规划：" + targetErr.Error()
	} else if draft.BaseHead != draft.Resource.Head {
		out.Reason = "本章已有更新的正式版本，请先核对正式稿与私人稿，不能仅更新写作依据。"
	} else {
		updated := d
		updated.Detail, updated.SettingsRevision, updated.SettingsHash, updated.IPHead = planPin(target), check.settingsRevision, check.settingsHash, check.head
		if err = s.validateDocumentTx(ctx, tx, book, updated); err != nil {
			if !basisUnavailable(err) && !errors.Is(err, domain.ErrValidation) {
				return out, err
			}
			out.Reason = "更新后仍有写作依据需要核对：" + err.Error()
		} else {
			out.CanRefresh = true
		}
	}
	if d.Detail.BranchID != book.BranchID {
		return out, domain.ErrForbidden
	}
	if d.Detail == out.After.DetailRef && out.After.Detail != nil {
		out.Before.Detail = out.After.Detail
	} else {
		old, e := s.basisDetailTx(ctx, tx, book, d)
		if e == nil {
			out.Before.Detail = &old
		} else if basisUnavailable(e) {
			out.Limitations = append(out.Limitations, "本章原先引用的细纲版本不可完整读取，不能据此判断细纲没有变化。")
		} else {
			return out, e
		}
	}
	oldSettings := check.setup
	var settingsErr error
	if d.SettingsRevision != check.settingsRevision || d.SettingsHash != check.settingsHash {
		oldSettings, settingsErr = basisSettingsTx(ctx, tx, book.ID, d.SettingsRevision, d.SettingsHash)
	}
	if settingsErr == nil {
		out.SettingsChanges = novel.SettingsChanges(oldSettings, check.setup)
	} else if basisUnavailable(settingsErr) {
		out.Limitations = append(out.Limitations, "本章原先引用的小说设置版本不可完整读取，不能据此判断创作约定没有变化。")
	} else {
		return out, settingsErr
	}
	if d.IPHead != check.head {
		before, beforeErr := basisSnapshotTx(ctx, tx, book.ProjectID, d.IPHead)
		after, afterErr := basisSnapshotTx(ctx, tx, book.ProjectID, check.head)
		if beforeErr == nil && afterErr == nil {
			changes := publicationSnapshotChanges(snapshotEntries(book.BranchID, d.IPHead, "", before), snapshotEntries(book.BranchID, check.head, "", after))
			out.IPChanges = dependencyDifferences(changes, before, after)
		} else {
			for _, e := range []error{beforeErr, afterErr} {
				if e != nil && !basisUnavailable(e) {
					return out, e
				}
			}
			out.Limitations = append(out.Limitations, "人物或世界设定的历史版本不可完整读取，不能据此判断设定没有变化。")
		}
	}
	// Bind both the saved chapter and the proposed target authority. The basis
	// action recomputes this under the same project lock before returning pins.
	out.BasisHash = capabilityDigest(struct {
		Resource, Chapter, Hash, Base, Head string
		Revision                            int64
		After                               NovelChapterBasisSide
		CanRefresh                          bool
	}{out.ResourceID, out.ChapterID, out.Hash, out.BaseHead, out.CurrentHead, out.Revision, out.After, out.CanRefresh})
	return out, nil
}

func (s *NovelChapters) basisDetailTx(ctx context.Context, tx *sql.Tx, book NovelBook, chapter novel.ChapterDocument) (novel.PlanningDocument, error) {
	var empty novel.PlanningDocument
	pin := chapter.Detail
	r, err := resourceTx(ctx, tx, book.ProjectID, pin.ID)
	if err != nil {
		return empty, err
	}
	if r.BranchID != book.BranchID || r.Kind != novel.PlanKind("detail") || r.Name != planResourceName("detail", chapter.ChapterID) {
		return empty, domain.ErrForbidden
	}
	release, err := releaseTx(ctx, tx, book.ProjectID, pin.Revision)
	if err != nil {
		return empty, err
	}
	if release.Status != "PUBLISHED" || release.Manifest.BranchID != book.BranchID {
		return empty, domain.ErrNotReady
	}
	for _, artifact := range release.Manifest.Artifacts {
		if artifact.ResourceID != pin.ID {
			continue
		}
		if resourceNodeHash(artifact.Hash, artifact.Policy, artifact.Dependencies) != pin.Hash {
			return empty, domain.ErrNotReady
		}
		raw, err := s.IP.Blobs.ReadObject(ctx, book.ProjectID, artifact.Hash)
		if err != nil || digest(string(raw)) != artifact.Hash {
			return empty, domain.ErrNotReady
		}
		doc, err := decodeNovelPlan(raw)
		if err != nil || doc.Kind != "detail" || doc.Scope != chapter.ChapterID {
			return empty, domain.ErrNotReady
		}
		return doc, nil
	}
	return empty, domain.ErrNotReady
}

func basisSettingsTx(ctx context.Context, tx *sql.Tx, book string, revision int64, expectedHash string) (novel.Setup, error) {
	var setup novel.Setup
	var raw, hash string
	if err := tx.QueryRowContext(ctx, "SELECT payload,content_hash FROM novel_settings_versions WHERE novel_id=? AND revision=?", book, revision).Scan(&raw, &hash); err != nil {
		return setup, err
	}
	if hash != expectedHash || digest(raw) != hash || json.Unmarshal([]byte(raw), &setup) != nil || setup.Validate(true) != nil {
		return setup, domain.ErrNotReady
	}
	return setup, nil
}

func basisSnapshotTx(ctx context.Context, tx *sql.Tx, project, revision string) (domain.Snapshot, error) {
	snapshot, err := snapshotTx(ctx, tx, project, revision)
	if err != nil || revision == "" {
		return snapshot, err
	}
	var hash string
	if err = tx.QueryRowContext(ctx, "SELECT content_hash FROM ip_revisions WHERE id=?", revision).Scan(&hash); err != nil {
		return snapshot, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || digest(string(raw)) != hash {
		return snapshot, fmt.Errorf("%w：设定历史版本校验失败", domain.ErrNotReady)
	}
	return snapshot, nil
}
