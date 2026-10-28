package retention

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SourceObservation identifies an actual SQL source column or one of the seven
// fixed, body-free accounting projections, not an executable expiry reference.
// A JSON source can contain multiple independently retained objects.
type SourceObservation struct {
	ID, Table, OwnerKey, Field, PayloadHash string
	Null                                    bool
}

func observedSourceColumn(table, field string) bool {
	column, ok := Column(table, field)
	if !ok {
		return false
	}
	if column.Privacy == PrivateBody || column.Privacy == PrivateText || column.Privacy == PrivateKey {
		return true
	}
	// A fixed blob rule names a genuine reference; other fingerprints ending
	// in _hash are not implicitly objects or source bodies.
	for _, rule := range referenceRules {
		parts := strings.Split(rule.Key, ".")
		if rule.Storage == Blob && parts[0] == table && parts[1] == field {
			return true
		}
	}
	return false
}

func observationID(project, table, owner, field string) string {
	return stableHash([]string{project, table, owner, field})
}

// SourceSlotID validates an archived slot against the same observed-column
// catalog as live registration, including synthetic accounting projections.
func SourceSlotID(project, table, owner, field string) (string, error) {
	if !id32.MatchString(project) || field == "" || (!observedSourceColumn(table, field) && field != accountingField(table)) {
		return "", ErrSourceOwnership
	}
	var parts []string
	if json.Unmarshal([]byte(owner), &parts) != nil {
		return "", ErrSourceOwnership
	}
	canonical, err := OwnerKey(parts...)
	if err != nil || canonical != owner {
		return "", ErrSourceOwnership
	}
	return observationID(project, table, owner, field), nil
}

func (index *SourceIndex) SourceObservations(project string) ([]SourceObservation, error) {
	if index == nil || !id32.MatchString(project) {
		return nil, ErrSourceOwnership
	}
	if _, err := index.sourceRow("projects", map[string]string{"id": project}); err != nil {
		return nil, err
	}
	var observations []SourceObservation
	fields := map[string][]string{}
	for _, table := range KnownTablesForSchema(38) {
		spec, _ := OwnerSpec(table)
		for _, column := range spec.Columns {
			if observedSourceColumn(table, column.Column) {
				fields[table] = append(fields[table], column.Column)
			}
		}
	}
	for _, row := range index.Records() {
		owner, err := index.Owner(row.Table, row.Values)
		if err != nil {
			return nil, fmt.Errorf("owner table %s: %w", row.Table, err)
		}
		if owner.Shared || owner.ProjectID != project {
			continue
		}
		key, err := RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return nil, err
		}
		for _, field := range fields[row.Table] {
			if _, replaced := index.replacedSources[observationID(project, row.Table, key, field)]; replaced {
				continue
			}
			if len(observations) == 250000 {
				return nil, ErrSourceOwnership
			}
			observations = append(observations, SourceObservation{
				ID:    observationID(project, row.Table, key, field),
				Table: row.Table, OwnerKey: key, Field: field,
				PayloadHash: financialDigest([]byte(row.Values[field])), Null: row.Nulls[field],
			})
		}
	}
	accounting, err := index.AccountingReferences(project)
	if err != nil {
		return nil, fmt.Errorf("accounting sources: %w", err)
	}
	for _, ref := range accounting {
		observations = append(observations, SourceObservation{ID: ref.SourceID, Table: ref.Table, OwnerKey: ref.OwnerKey, Field: ref.Field, PayloadHash: ref.SourcePayloadHash})
	}
	if len(observations) > 250000 {
		return nil, ErrSourceOwnership
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ID < observations[j].ID })
	return observations, nil
}
