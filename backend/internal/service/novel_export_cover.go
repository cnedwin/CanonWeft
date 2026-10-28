package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelExportCover struct {
	ImageID   string `json:"image_id"`
	ReleaseID string `json:"release_id"`
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Warning   string `json:"warning,omitempty"`
}

// coverTx pins one explicitly selected adopted original. Other books, branches,
// characters, scenes and private candidates cannot become the exported cover.
func (s *NovelExport) coverTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (*NovelExportCover, *novel.ExportCover, error) {
	if !lowerHex32(id) {
		return nil, nil, domain.ErrValidation
	}
	item, err := visualImageTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id)
	if err != nil {
		return nil, nil, err
	}
	m := item.Report.NovelImage
	if m != nil && m.BookID != book.ID {
		return nil, nil, domain.ErrForbidden
	}
	if !item.Published || m == nil || m.Kind != "cover" {
		return nil, nil, fmt.Errorf("%w：请选择本书已明确采用的封面图片", domain.ErrValidation)
	}
	var release string
	err = tx.QueryRowContext(ctx, `SELECT c.release_id FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=? AND r.project_id=? AND r.branch_id=? AND r.status='PUBLISHED' ORDER BY c.release_id LIMIT 1`, id, book.ProjectID, book.BranchID).Scan(&release)
	if err != nil {
		return nil, nil, err
	}
	out := &NovelExportCover{ImageID: id, ReleaseID: release, Hash: item.Hash, Name: m.Name, MIME: m.MIME, Width: m.Width, Height: m.Height}
	if err = s.IP.validateNovelImageReportTx(ctx, tx, book.ProjectID, book.BranchID, id, m, nil); err != nil {
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrMaterialExpired) {
			out.Warning = "封面基于旧版角色、设置或资料；请核对后确认使用这个已采用的历史版本。"
		} else {
			return nil, nil, err
		}
	}
	data, err := s.IP.Blobs.ReadObject(ctx, book.ProjectID, item.Hash)
	if err != nil || int64(len(data)) != item.Size || digest(string(data)) != item.Hash {
		return nil, nil, domain.ErrNotReady
	}
	cover, err := novel.NewExportCover(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：封面无法转为兼容图片，请上传较小的完整图片", domain.ErrValidation)
	}
	if cover.Width != m.Width || cover.Height != m.Height {
		return nil, nil, domain.ErrNotReady
	}
	return out, cover, nil
}
