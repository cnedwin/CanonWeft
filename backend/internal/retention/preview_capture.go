package retention

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"unicode/utf8"

	"novelstudio/internal/authority"
)

// CheckpointGenerationPreview records the first and final persisted previews.
// Intermediate checkpoints invalidate the cleanup snapshot under the project
// lock, without producing one immutable history entry per streaming frame.
// After a crash, normal inventory captures the actual remaining row and uses
// the original slot age. A preview is never an input to adoption or execution.
func CheckpointGenerationPreview(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project, generation string, final bool) error {
	if tx == nil || !id32.MatchString(project) || !id32.MatchString(generation) {
		return ErrSourceOwnership
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil || !enabled {
		return err
	}
	key, err := RowOwnerKey("generation_previews", map[string]string{"generation_id": generation})
	if err != nil {
		return err
	}
	id, err := ReferenceID(project, "generation_previews", key, "content")
	if err != nil {
		return err
	}
	var state PayloadState
	err = tx.QueryRowContext(ctx, `SELECT r.state FROM retention_references r
JOIN generation_tasks g ON g.id=? AND g.project_id=r.project_id
WHERE r.id=? AND r.project_id=? AND r.owner_table='generation_previews' AND r.owner_key=? AND r.field_key='content' FOR UPDATE`, generation, id, project, key).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && state != Retained {
		return ErrInvalidInventory
	}
	if final || errors.Is(err, sql.ErrNoRows) {
		return CaptureSourceValues(ctx, tx, ledger, project, SourceKey{Table: "generation_previews", Primary: map[string]string{"generation_id": generation}})
	}
	return invalidateCapturedInventory(ctx, tx, project)
}

func validPreviewSource(row SourceRow) bool {
	sequence, err := strconv.ParseInt(row.Values["sequence"], 10, 64)
	text := row.Values["content"]
	return err == nil && sequence > 0 && len(text) <= 4*1024*1024 && utf8.ValidString(text) && financialDigest([]byte(text)) == row.Values["content_hash"]
}
