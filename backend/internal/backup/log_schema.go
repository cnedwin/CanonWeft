package backup

import (
	"errors"
	"slices"
)

// Only the explicitly qualified additive 30 -> 31 -> 32 -> 33 -> 34 -> 35 transitions are supported for
// retained log chains. Earlier archives have conversions but no log protocol.
// A future migration must extend this mapping, never infer compatibility.
func logSchemaVersion(versions ...int) (int, error) {
	v := supportedSchemaVersion
	if len(versions) > 1 {
		return 0, errors.New("ambiguous log schema")
	}
	if len(versions) == 1 {
		v = versions[0]
	}
	if supportedSchemaVersion != 35 || (v != 30 && v != 31 && v != 32 && v != 33 && v != 34 && v != 35 && !hasRetentionSchema(v)) {
		return 0, errors.New("unsupported log schema")
	}
	return v, nil
}

func logSchemaCatalog(versions ...int) ([]tableSpec, error) {
	v, err := logSchemaVersion(versions...)
	if err != nil {
		return nil, err
	}
	var result []tableSpec
	if hasRetentionSchema(v) {
		catalog, err := snapshotCatalog(v)
		if err != nil {
			return nil, err
		}
		return append(catalog, logTableCatalog()[len(tableCatalog):]...), nil
	}
	for _, spec := range logTableCatalog() {
		if logSchemaFutureTable(v, spec.name) {
			continue
		}
		result = append(result, spec)
	}
	return result, nil
}

// Replay uses the current validated Data shape, with new tables empty. Export
// projects only the trusted original schema so its migration/hash provenance
// remains truthful and normal restore can apply the checked-in conversion.
func logSchemaData(data Data, versions ...int) (Data, error) {
	v, err := logSchemaVersion(versions...)
	if err != nil {
		return nil, err
	}
	if hasRetentionSchema(v) {
		_, _, err := splitRetentionData(v, data)
		return data, err
	}
	for _, spec := range retentionTableCatalog() {
		if _, exists := data[spec.name]; exists {
			return nil, errors.New("retention tables in historical log state")
		}
	}
	if v == 35 {
		return data, nil
	}
	result := make(Data, len(data))
	for name, rows := range data {
		if logSchemaFutureTable(v, name) {
			if len(rows) != 0 {
				return nil, errors.New("new schema rows in historical log state")
			}
			continue
		}
		result[name] = rows
	}
	return result, nil
}

func logSchemaFutureTable(version int, name string) bool {
	return version < 35 && slices.Contains(memoryTables, name) || version < 34 && slices.Contains(recipeTables, name) || version < 33 && slices.Contains(ingestTables, name) || version < 32 && name == "patch_drafts" || version < 31 && slices.Contains(semanticReviewTables, name)
}
