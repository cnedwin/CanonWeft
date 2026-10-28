package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type recoveryExpiryRows struct {
	plan   *retention.RecoveryExpiryPlan
	owners map[string][]string
}

func recoveryRowSource(table recoveryTable, row recoveryRow) retention.SourceRow {
	result := retention.SourceRow{Table: table.name, Values: map[string]string{}, Nulls: map[string]bool{}}
	for i, column := range strings.Split(table.columns, ",") {
		result.Values[column] = row[i].String
		if !row[i].Valid {
			result.Nulls[column] = true
		}
	}
	return result
}

func prepareRecoveryExpiryRows(ctx context.Context, tx *sql.Tx, project string, tables []recoveryTable, data map[string][]recoveryRow) (*recoveryExpiryRows, error) {
	result := &recoveryExpiryRows{owners: map[string][]string{}}
	keys := []retention.SourceKey{}
	for _, table := range tables {
		for _, row := range data[table.name] {
			values := recoveryRowSource(table, row)
			key, err := retention.RowOwnerKey(table.name, values.Values)
			if err != nil {
				return nil, domain.ErrNotReady
			}
			primary := map[string]string{}
			for _, column := range retention.PrimaryKey(table.name) {
				primary[column.Name] = values.Values[column.Name]
			}
			keys = append(keys, retention.SourceKey{Table: table.name, Primary: primary})
			result.owners[table.name] = append(result.owners[table.name], key)
		}
	}
	var err error
	result.plan, err = retention.ReadRecoveryExpiriesTx(ctx, tx, project, keys)
	if err != nil {
		return nil, domain.ErrNotReady
	}
	return result, nil
}

// Called before ID rewriting. A syntactically valid envelope alone is never
// enough: the plan has verified the original row, reference and journal.
func (r *recoveryExpiryRows) field(row recoveryRow, table recoveryTable, field string) (retention.ExpiredProjection, bool) {
	key, err := retention.RowOwnerKey(table.name, recoveryRowSource(table, row).Values)
	if err != nil {
		return retention.ExpiredProjection{}, false
	}
	return r.plan.Field(table.name, key, field)
}

func (r *recoveryExpiryRows) expired(name string, row recoveryRow, table recoveryTable, fields ...string) bool {
	key, err := retention.RowOwnerKey(name, recoveryRowSource(table, row).Values)
	if err != nil {
		return false
	}
	for _, field := range fields {
		if _, exists := r.plan.Field(name, key, field); exists {
			return true
		}
	}
	return false
}

func (r *recoveryExpiryRows) rebind(source, target string, tables []recoveryTable, data map[string][]recoveryRow, mapping retention.RecoveryExpiryMap) (retention.RecoveryExpiryMap, retention.RecoveryExpiryGraph, error) {
	mapping.SourceProject, mapping.TargetProject = source, target
	mapping.Owners = map[retention.RecoveryOwner]string{}
	mapping.Rows = map[retention.RecoveryOwner]retention.SourceRow{}
	for _, table := range tables {
		for i, row := range data[table.name] {
			values := recoveryRowSource(table, row)
			key, err := retention.RowOwnerKey(table.name, values.Values)
			if err != nil {
				return mapping, retention.RecoveryExpiryGraph{}, domain.ErrNotReady
			}
			owner := retention.RecoveryOwner{Table: table.name, Key: r.owners[table.name][i]}
			mapping.Owners[owner], mapping.Rows[owner] = key, values
		}
	}
	graph, err := r.plan.Rebind(mapping)
	if err != nil {
		return mapping, graph, domain.ErrNotReady
	}
	for _, table := range tables {
		for i, row := range data[table.name] {
			for _, field := range strings.Split(table.columns, ",") {
				proof, exists, err := r.plan.ReboundField(table.name, r.owners[table.name][i], field, mapping, graph)
				if err != nil {
					return mapping, graph, domain.ErrNotReady
				}
				if !exists {
					continue
				}
				if proof.Materialization == retention.InlineEnvelope {
					raw, err := json.Marshal(proof)
					if err != nil {
						return mapping, graph, err
					}
					table.set(row, field, string(raw))
				} else if proof.Materialization == retention.ClearScalar {
					table.set(row, field, "")
				} else if proof.Materialization != retention.BlobReference {
					return mapping, graph, domain.ErrNotReady
				}
			}
		}
	}
	return mapping, graph, nil
}
