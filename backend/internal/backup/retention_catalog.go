package backup

import (
	"errors"
	"slices"
	"strings"

	"novelstudio/internal/retention"
	"novelstudio/migrations"
)

const retentionSchemaVersion = 36

// The exact schema36 contract is shared with the application migration catalog.
// Its split source remains outside the numbered schema1..35 legacy archive files.
func retentionMigration() (Migration, string, error) {
	text, checksum, err := migrations.PendingRetentionSQL()
	if err != nil {
		return Migration{}, "", err
	}
	return Migration{Version: retentionSchemaVersion, Name: retention.MigrationName, SHA256: checksum}, text, nil
}

// Keep metadata column order identical to the live registration contract.
// Sorting table names makes the archive and hash independent of Go map order.
func retentionTableCatalog() []tableSpec {
	metadata := retention.RegistrationMetadata()
	names := make([]string, 0, len(metadata))
	for name := range metadata {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]tableSpec, 0, len(names))
	for _, name := range names {
		result = append(result, tableSpec{name, strings.Join(metadata[name], ",")})
	}
	return result
}

// splitRetentionData transports metadata in the existing data.json. It never
// normalizes byte cells or NULLs, executes archive SQL, or admits restoration.
// The returned business map is isolated; callers cannot drop metadata from the
// original archive by deleting entries in that map.
func splitRetentionData(schema int, data Data) (Data, Data, error) {
	business, metadata := Data{}, Data{}
	metadataColumns := retention.RegistrationMetadata()
	want := map[string]tableSpec{}
	for _, table := range tableCatalog {
		want[table.name] = table
	}
	if schema >= 37 {
		for _, table := range novelCatalogForSchema(schema) {
			want[table.name] = table
		}
	}
	if hasRetentionSchema(schema) {
		for _, table := range retentionTableCatalog() {
			if _, duplicate := want[table.name]; duplicate {
				return nil, nil, errors.New("duplicate retention table contract")
			}
			want[table.name] = table
		}
	} else if schema < legacySchemaVersion || schema > supportedSchemaVersion {
		return nil, nil, errors.New("unsupported retention transport schema")
	}
	// Called after existing legacy business-table conversions.
	if len(data) != len(want) {
		return nil, nil, errors.New("incomplete archive table contract")
	}
	for name, spec := range want {
		rows, exists := data[name]
		if !exists {
			return nil, nil, errors.New("missing archive table")
		}
		for _, row := range rows {
			if len(row) != len(spec.cols()) {
				return nil, nil, errors.New("archive metadata column mismatch")
			}
		}
		if _, isMetadata := metadataColumns[name]; isMetadata {
			metadata[name] = rows
		} else {
			business[name] = rows
		}
	}
	return business, metadata, nil
}
