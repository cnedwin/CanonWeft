package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type RuntimeRetention = domain.MaterialRetention

func requireRuntimeObjectTx(ctx context.Context, tx *sql.Tx, project, table, id, field, hash string) error {
	expired, err := runtimeObjectRetentionTx(ctx, tx, project, table, id, field, hash)
	if err != nil {
		return err
	}
	if expired != nil {
		return domain.ErrMaterialExpired
	}
	return nil
}

// Hash columns remain as historical identities after their material expires.
// Verify the separate evidence before showing a marker or refusing adoption;
// the physical file may still exist until the later object deletion phase.
func runtimeObjectRetentionTx(ctx context.Context, tx *sql.Tx, project, table, id, field, hash string) (*RuntimeRetention, error) {
	if hash == "" {
		return nil, nil
	}
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", retention.MigrationName).Scan(&installed); err != nil {
		return nil, err
	}
	if installed == 0 {
		var present int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('retention_references','retention_evidence')").Scan(&present); err != nil || present != 0 {
			return nil, domain.ErrNotReady
		}
		return nil, nil
	}
	if installed != 1 {
		return nil, domain.ErrNotReady
	}
	key, err := retention.RowOwnerKey(table, map[string]string{"id": id})
	if err != nil {
		return nil, domain.ErrNotReady
	}
	ref, err := retention.ReferenceID(project, table, key, field)
	if err != nil {
		return nil, domain.ErrNotReady
	}
	var state string
	err = tx.QueryRowContext(ctx, "SELECT state FROM retention_references WHERE id=? AND project_id=? FOR SHARE", ref, project).Scan(&state)
	if err == sql.ErrNoRows || (err == nil && state == string(retention.Retained)) {
		return nil, nil
	}
	if err != nil || state != string(retention.Expired) {
		return nil, domain.ErrNotReady
	}
	proof, err := storedExpiredProjectionTx(ctx, tx, retention.StoredOwner{ProjectID: project, Table: table, Key: key, Field: field, Value: []byte(hash)})
	if err != nil {
		return nil, err
	}
	return &RuntimeRetention{State: "EXPIRED", ExpiredAt: proof.ExpiredAt}, nil
}

// A list can show a terminal historical marker without making expired material
// executable. It must verify the independent proof after closing its SQL rows.
func runtimeRetentionTx(ctx context.Context, tx *sql.Tx, project, table, id, call string, raw []byte, originalHash ...string) (*RuntimeRetention, error) {
	key, err := retention.RowOwnerKey(table, map[string]string{"id": id})
	if err != nil {
		return nil, domain.ErrNotReady
	}
	owner := retention.StoredOwner{ProjectID: project, Table: table, Key: key, Field: "frozen", Value: raw}
	if table == "recipe_runs" {
		if err := tx.QueryRowContext(ctx, "SELECT frozen_hash FROM recipe_runs WHERE id=? AND project_id=?", id, project).Scan(&owner.FrozenHash); err != nil {
			return nil, domain.ErrNotReady
		}
	}
	if table == "recipe_steps" {
		if err := tx.QueryRowContext(ctx, "SELECT run_id,step_key,frozen_hash FROM recipe_steps WHERE id=? AND project_id=?", id, project).Scan(&owner.RunID, &owner.StepKey, &owner.FrozenHash); err != nil {
			return nil, domain.ErrNotReady
		}
		if _, marked := retention.RetiredLabelHash(owner.StepKey); marked {
			if _, err := runtimeTransientTx(ctx, tx, project, table, "step_key", id); err != nil {
				return nil, err
			}
		}
	}
	proof, err := storedExpiredProjectionTx(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	if table == "generation_tasks" && proof.Summary.CallID != call {
		return nil, domain.ErrNotReady
	}
	if len(originalHash) > 0 && proof.OriginalPayloadHash != originalHash[0] {
		return nil, domain.ErrNotReady
	}
	return &RuntimeRetention{State: "EXPIRED", ExpiredAt: proof.ExpiredAt}, nil
}

// This is an execution refusal, not acceptance of historical evidence. Runtime
// decoders must never interpret an expiry envelope as an empty frozen config.
// Showing financial history separately requires the full registry/source proof.
func rejectExpiredRuntime(raw []byte, rule string) error {
	var header struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &header) != nil || header.State != "EXPIRED" {
		return nil
	}
	if _, err := retention.ParseSourceProjection(raw, rule); err != nil {
		return domain.ErrNotReady
	}
	return domain.ErrMaterialExpired
}
