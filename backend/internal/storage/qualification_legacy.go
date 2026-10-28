package storage

import (
	"context"
	"database/sql"
	"errors"
	"novelstudio/internal/qualificationledger"
)

// This runs only before migration30 is journalled. A current missing mapping is
// never repaired merely because it is missing: old policy and the actual
// executed migration29 timestamp must both prove a pre-budget call.
func backfillLegacyQualificationCalls(ctx context.Context, conn *sql.Conn) error {
	var boundary, checksum, origin string
	if err := conn.QueryRowContext(ctx, "SELECT CAST(recorded_at AS CHAR),checksum,origin FROM schema_migrations WHERE version=29").Scan(&boundary, &checksum, &origin); err != nil {
		return err
	}
	if origin != "EXECUTED" || checksum != qualificationledger.BoundaryChecksum() {
		return errors.New("executed migration29 boundary required for legacy evaluation conversion")
	}
	source := qualificationledger.Source{Schema: 28, Kind: "DATABASE", Boundary: boundary, MigrationChecksum: checksum}
	for {
		rows, err := conn.QueryContext(ctx, `SELECT r.project_id,r.actor_id,r.id,s.id,g.id,c.id,
 r.project_id,s.project_id,s.run_id,g.project_id,g.actor_id,c.project_id,c.actor_id,
 s.child_id,s.child_kind,g.operation_id,g.input_hash,CAST(r.created_at AS CHAR),CAST(g.created_at AS CHAR),CAST(c.created_at AS CHAR),
 r.frozen,g.frozen,c.state,a.currency,c.reserved_micros,c.charged_micros
 FROM generation_tasks g JOIN model_calls c ON c.id=g.call_id
 JOIN model_call_accounting a ON a.call_id=c.id
 JOIN qualification_runs r ON r.id=JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.capability.evaluation_id'))
 JOIN qualification_steps s ON s.run_id=r.id AND g.operation_id=CONCAT('evaluation-',s.id)
 LEFT JOIN qualification_calls q ON q.call_id=c.id
 WHERE q.call_id IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.capability.mode'))='EVALUATION'
 AND JSON_UNQUOTE(JSON_EXTRACT(r.frozen,'$.suite.policy')) IN ('repository-deterministic-v1','repository-deterministic-v2')
 AND g.created_at<? AND r.created_at<? AND c.created_at<? ORDER BY g.id LIMIT 1000`, boundary, boundary, boundary)
		if err != nil {
			return err
		}
		type entry struct {
			record  qualificationledger.Record
			mapping qualificationledger.Mapping
		}
		entries := []entry{}
		for rows.Next() {
			var r qualificationledger.Record
			err = rows.Scan(&r.Project, &r.Actor, &r.RunID, &r.StepID, &r.GenerationID, &r.CallID, &r.RunProject, &r.StepProject, &r.StepRun, &r.GenerationProject, &r.GenerationActor, &r.CallProject, &r.CallActor, &r.ChildID, &r.ChildKind, &r.Operation, &r.InputHash, &r.RunCreated, &r.GenerationCreated, &r.CallCreated, &r.RunFrozen, &r.GenerationFrozen, &r.State, &r.Currency, &r.Reserved, &r.Charged)
			if err != nil {
				rows.Close()
				return err
			}
			mapping, e := qualificationledger.Convert(r, source)
			if e != nil {
				rows.Close()
				return e
			}
			entries = append(entries, entry{r, mapping})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			r, m := entry.record, entry.mapping
			if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_calls(call_id,project_id,run_id,step_id,grant_id,reserved_micros,held_micros,charged_micros,state,origin,legacy_source) VALUES(?,?,?,?,'',?,?,?,?,?,?)", r.CallID, r.Project, r.RunID, r.StepID, m.Reserved, m.Held, m.Charged, m.State, m.Origin, m.Provenance); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}
