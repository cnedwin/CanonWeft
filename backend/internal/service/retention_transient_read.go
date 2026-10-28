package service

import (
	"context"
	"database/sql"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// Only compiled columns are selectable. The caller has already authorized the
// project and closes any open result set before loading independent evidence.
func runtimeTransientTx(ctx context.Context, tx *sql.Tx, project, table, field, id string) (*RuntimeRetention, error) {
	rule := table + "." + field
	columns := retention.TransientColumns(rule)
	if len(columns) == 0 {
		return nil, domain.ErrNotReady
	}
	columns = append(columns, field)
	if table == "outbox" {
		columns = append(columns, "processed_at")
	}
	selected := make([]string, len(columns))
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for i, column := range columns {
		selected[i] = "s." + column
		pointers[i] = &values[i]
	}
	primary := "id"
	from := table + " s"
	filter := "s.id=? AND s.project_id=?"
	if table == "generation_previews" {
		primary = "generation_id"
		from += " JOIN generation_tasks g ON g.id=s.generation_id"
		filter = "s.generation_id=? AND g.project_id=?"
	}
	if table == "ip_promotions" || table == "ip_branch_deltas" {
		primary = "candidate_id"
		from += " JOIN ip_revisions c ON c.id=s.candidate_id JOIN branches b ON b.id=c.branch_id"
		filter = "s.candidate_id=? AND b.project_id=?"
	}
	if table == "ip_revisions" {
		from += " JOIN branches b ON b.id=s.branch_id"
		filter = "s.id=? AND b.project_id=?"
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+strings.Join(selected, ",")+" FROM "+from+" WHERE "+filter, id, project).Scan(pointers...); err != nil {
		return nil, domain.ErrNotReady
	}
	row := map[string]string{}
	for i, column := range columns {
		row[column] = values[i].String
	}
	scope, err := retention.TransientIdentity(rule, row)
	if err != nil {
		return nil, domain.ErrNotReady
	}
	key, err := retention.RowOwnerKey(table, map[string]string{primary: id})
	if err != nil {
		return nil, domain.ErrNotReady
	}
	proof, err := storedExpiredProjectionTx(ctx, tx, retention.StoredOwner{ProjectID: project, Table: table, Key: key, Field: field, TransientScopeHash: scope, Value: []byte(row[field])})
	if err != nil {
		return nil, err
	}
	return &RuntimeRetention{State: "EXPIRED", ExpiredAt: proof.ExpiredAt}, nil
}

func candidateAuxiliaryRetentionTx(ctx context.Context, tx *sql.Tx, project, id string) (*RuntimeRetention, error) {
	var promotion, delta sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT p.payload,d.payload FROM ip_revisions c LEFT JOIN ip_promotions p ON p.candidate_id=c.id LEFT JOIN ip_branch_deltas d ON d.candidate_id=c.id WHERE c.id=?", id).Scan(&promotion, &delta); err != nil {
		return nil, err
	}
	var status *RuntimeRetention
	for _, doc := range []struct {
		table string
		raw   sql.NullString
	}{{"ip_promotions", promotion}, {"ip_branch_deltas", delta}} {
		if !doc.raw.Valid {
			continue
		}
		err := rejectExpiredRuntime([]byte(doc.raw.String), doc.table+".payload")
		if err == nil {
			continue
		}
		if err != domain.ErrMaterialExpired {
			return nil, err
		}
		verified, err := runtimeTransientTx(ctx, tx, project, doc.table, "payload", id)
		if err != nil {
			return nil, err
		}
		if status == nil || verified.ExpiredAt.Before(status.ExpiredAt) {
			status = verified
		}
	}
	if status == nil {
		return nil, domain.ErrNotReady
	}
	return status, nil
}
