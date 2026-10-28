package backup

import (
	"errors"
	"novelstudio/internal/retention"
)

var errRetentionOwner = errors.New("retention source owner is missing, ambiguous or outside project scope")

// Archived and live sources share exactly one ownership resolver. The adapter
// only converts the independently checked archive column order and NULLs.
type retentionArchiveOwners struct {
	index *retention.SourceIndex
	rows  map[string]map[string]retention.SourceRow
}

func newRetentionArchiveOwners(data Data) (*retentionArchiveOwners, error) {
	var source []retention.SourceRow
	for _, spec := range businessTableCatalog(data) {
		columns := spec.cols()
		if retention.ValidateColumnCoverage(spec.name, columns) != nil {
			return nil, errRetentionOwner
		}
		for _, row := range data[spec.name] {
			if len(row) != len(columns) {
				return nil, errRetentionOwner
			}
			converted := retention.SourceRow{Table: spec.name, Values: map[string]string{}, Nulls: map[string]bool{}}
			for i, column := range columns {
				converted.Values[column] = string(row[i])
				if row[i] == nil {
					converted.Nulls[column] = true
				}
			}
			source = append(source, converted)
		}
	}
	index, err := retention.NewSourceIndex(source)
	if err != nil {
		return nil, errRetentionOwner
	}
	owners := &retentionArchiveOwners{index: index, rows: map[string]map[string]retention.SourceRow{}}
	for _, row := range index.Records() {
		key, err := retention.RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return nil, errRetentionOwner
		}
		if owners.rows[row.Table] == nil {
			owners.rows[row.Table] = map[string]retention.SourceRow{}
		}
		owners.rows[row.Table][key] = row
	}
	return owners, nil
}

func (owners *retentionArchiveOwners) field(table, key, column string) (expiredArchiveOwner, error) {
	if owners == nil {
		return expiredArchiveOwner{}, errRetentionOwner
	}
	row, exists := owners.rows[table][key]
	if !exists {
		return expiredArchiveOwner{}, errRetentionOwner
	}
	value, exists := row.Values[column]
	if !exists {
		return expiredArchiveOwner{}, errRetentionOwner
	}
	owner, err := owners.index.Owner(table, row.Values)
	if table == "memory_sessions" || table == "memory_items" || table == "memory_revisions" {
		// Scope only here; archive journal and business-document validation
		// separately establish whether this is a retained or expired session.
		owner, err = owners.index.MemoryDocumentMetadataOwner(row)
	}
	if err != nil || owner.Shared || owner.ProjectID == "" {
		return expiredArchiveOwner{}, errRetentionOwner
	}
	var raw []byte
	if !row.Nulls[column] {
		raw = []byte(value)
	}
	actual := expiredArchiveOwner{ProjectID: owner.ProjectID, Table: table, Key: key, Field: column, CallID: row.Values["call_id"], ContentHash: row.Values["content_hash"], SourceRevision: row.Values["revision"], ContextIndexID: row.Values["index_id"], RunID: row.Values["run_id"], StepKey: row.Values["step_key"], FrozenHash: row.Values["frozen_hash"], Value: raw}
	if len(retention.TransientColumns(table+"."+column)) > 0 {
		actual.TransientScopeHash, err = retention.TransientIdentity(table+"."+column, row.Values)
		if err != nil {
			return expiredArchiveOwner{}, err
		}
	}
	return actual, nil
}
