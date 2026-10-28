package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

type PriceCard = models.PriceCard

func (s *Generations) SavePrice(ctx context.Context, actor string, input PriceCard) (PriceCard, error) {
	if err := input.Validate(); err != nil {
		return input, err
	}
	tx, a, err := (&Models{Workspace: s.IP.Workspace}).owner(ctx, actor)
	if err != nil {
		return input, err
	}
	defer tx.Rollback()
	config, _, err := modelTx(ctx, tx, a.WorkspaceID, input.ConnectionID)
	if err != nil {
		return input, err
	}
	if config.Revision != input.ConnectionRevision {
		return input, domain.ErrConflict
	}
	input.ID = NewID()
	raw, _ := json.Marshal(input)
	_, err = tx.ExecContext(ctx, "INSERT INTO model_prices(id,workspace_id,connection_id,connection_revision,payload,content_hash) VALUES(?,?,?,?,?,?)", input.ID, a.WorkspaceID, input.ConnectionID, input.ConnectionRevision, raw, digest(string(raw)))
	if err != nil {
		return input, err
	}
	if err = audit(ctx, tx, a, "model.price", input.ID); err != nil {
		return input, err
	}
	return input, tx.Commit()
}
func priceTx(ctx context.Context, tx *sql.Tx, workspace, id string) (PriceCard, error) {
	var result PriceCard
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT payload,content_hash FROM model_prices WHERE id=? AND workspace_id=?", id, workspace).Scan(&raw, &hash)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	if json.Unmarshal(raw, &result) != nil {
		return result, domain.ErrNotReady
	}
	canonical, _ := json.Marshal(result)
	if digest(string(canonical)) != hash {
		return result, domain.ErrNotReady
	}
	return result, nil
}
