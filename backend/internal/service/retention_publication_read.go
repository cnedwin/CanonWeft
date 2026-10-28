package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

func publicationRetentionTx(ctx context.Context, tx *sql.Tx, project, table, id, hash string, raw []byte) (*RuntimeRetention, error) {
	field := "payload"
	if table == "artifact_releases" {
		field = "manifest"
	}
	if err := rejectExpiredRuntime(raw, table+"."+field); err != nil {
		if err != domain.ErrMaterialExpired {
			return nil, err
		}
		return runtimeTransientTx(ctx, tx, project, table, field, id)
	}
	state, err := runtimeObjectRetentionTx(ctx, tx, project, table, id, "content_hash", hash)
	if err != nil || state != nil || table != "artifact_releases" {
		return state, err
	}
	var m domain.Manifest
	if json.Unmarshal(raw, &m) != nil {
		return nil, domain.ErrNotReady
	}
	for _, a := range m.Artifacts {
		for _, original := range []bool{false, true} {
			hash := a.Hash
			if original {
				hash = a.OriginalHash
			}
			if hash == "" {
				continue
			}
			field, err := retention.ArtifactField(a.ResourceID, original)
			if err != nil {
				return nil, domain.ErrNotReady
			}
			state, err := runtimeObjectRetentionTx(ctx, tx, project, table, id, field, hash)
			if err != nil || state != nil {
				return state, err
			}
		}
	}
	return nil, nil
}
