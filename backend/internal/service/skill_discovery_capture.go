package service

import (
	"context"
	"database/sql"
)

// State-only transitions must remain usable when an old package is missing.
// The caller holds the project lock and checks the current task state first.
func updateDiscoveryStateTx(ctx context.Context, tx *sql.Tx, project, id, state, reason string) error {
	if err := captureDiagnosticSourceTx(ctx, tx, project, "skill_discovery_runs", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE skill_discovery_runs SET state=?,reason=? WHERE id=? AND project_id=?", state, reason, id, project); err != nil {
		return err
	}
	return captureDiagnosticSourceTx(ctx, tx, project, "skill_discovery_runs", id)
}

func blockDiscoverySourceRunsTx(ctx context.Context, tx *sql.Tx, project, source string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM skill_discovery_runs WHERE project_id=? AND source_id=? AND state IN ('QUEUED','RUNNING','PAUSED') FOR UPDATE", project, source)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := updateDiscoveryStateTx(ctx, tx, project, id, "BLOCKED", "来源配置或授权版本已变更，请重新发现"); err != nil {
			return err
		}
	}
	return nil
}
