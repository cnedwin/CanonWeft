package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/binlog"
	"slices"
	"sort"
	"strings"
)

func logContracts(tables []LogTable, schema ...int) (map[string]LogTable, error) {
	expected, err := logSchemaCatalog(schema...)
	if err != nil {
		return nil, err
	}
	if len(tables) != len(expected) {
		return nil, errors.New("incomplete log schema contract")
	}
	result := map[string]LogTable{}
	var layouts []binlog.TableSchema
	for i, table := range tables {
		if table.Layout.Name != expected[i].name || !slices.Equal(table.Layout.Columns, expected[i].cols()) || len(table.Columns) != len(table.Layout.Columns) {
			return nil, errors.New("log schema contract changed")
		}
		for j, column := range table.Columns {
			if column.Name != table.Layout.Columns[j] || column.Type == "" || column.SQLType == "" {
				return nil, errors.New("invalid log SQL type contract")
			}
		}
		result[table.Layout.Name] = table
		layouts = append(layouts, table.Layout)
	}
	if _, err := binlog.NewDecoder("contract", layouts, binlog.Position{File: "binlog.000001", Offset: 4}); err != nil {
		return nil, errors.New("invalid log row layout")
	}
	return result, nil
}

func normalizeLogRow(table LogTable, row binlog.Row, schema ...int) (Row, error) {
	if len(row) != len(table.Columns) {
		return nil, errors.New("log row width mismatch")
	}
	result := make(Row, len(row))
	for i, cell := range row {
		if cell == nil {
			continue
		}
		result[i] = append([]byte{}, cell...)
		switch table.Columns[i].Type {
		case "json":
			if len(schema) == 1 && hasRetentionSchema(schema[0]) {
				// Live capture renders JSON through the source engine. Offline
				// replay must preserve the bytes bound by the source journals.
				if !json.Valid(cell) {
					return nil, errors.New("invalid log JSON value")
				}
				continue
			}
			var compact bytes.Buffer
			if json.Compact(&compact, cell) != nil {
				return nil, errors.New("invalid log JSON value")
			}
			result[i] = compact.Bytes()
		case "datetime", "timestamp", "time":
			value := string(cell)
			if strings.Contains(value, ".") {
				value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
			}
			result[i] = []byte(value)
		case "date":
			value := strings.TrimSuffix(string(cell), " 00:00:00")
			result[i] = []byte(value)
		}
	}
	return result, nil
}

func prepareLogTransaction(tables []LogTable, tx binlog.Transaction, schema ...int) (binlog.Transaction, error) {
	contracts, err := logContracts(tables, schema...)
	if err != nil {
		return binlog.Transaction{}, err
	}
	version := supportedSchemaVersion
	if len(schema) != 0 {
		version = schema[0]
	}
	if len(tx.Changes) > 100000 {
		return binlog.Transaction{}, errors.New("log transaction row limit")
	}
	var size int64
	for _, change := range tx.Changes {
		for _, row := range []binlog.Row{change.Before, change.After} {
			for _, cell := range row {
				size += int64(len(cell)) + 32
				if size > 64<<20 {
					return binlog.Transaction{}, errors.New("log transaction byte limit")
				}
			}
		}
	}
	clean := tx
	clean.Changes = nil
	for _, change := range tx.Changes {
		table, ok := contracts[change.Table]
		if !ok || change.Table == "schema_version" || change.Table == "schema_migrations" {
			return binlog.Transaction{}, errors.New("source schema changed; new baseline required")
		}
		if change.Kind != "insert" && change.Kind != "update" && change.Kind != "delete" {
			return binlog.Transaction{}, errors.New("unsupported log row operation")
		}
		if (change.Kind == "insert" && change.Before != nil) || (change.Kind == "delete" && change.After != nil) || (change.Kind != "insert" && change.Before == nil) || (change.Kind != "delete" && change.After == nil) {
			return binlog.Transaction{}, errors.New("incomplete log row operation")
		}
		for _, row := range []binlog.Row{change.Before, change.After} {
			if row != nil && len(row) != len(table.Columns) {
				return binlog.Transaction{}, errors.New("log row width mismatch")
			}
		}
		if excludedTableForSchema(change.Table, version) {
			continue
		}
		copy := binlog.Change{Table: change.Table, Kind: change.Kind}
		for index, row := range []binlog.Row{change.Before, change.After} {
			if row == nil {
				continue
			}
			normalized, err := normalizeLogRow(table, row, version)
			if err != nil {
				return binlog.Transaction{}, err
			}
			data := Data{change.Table: []Row{normalized}}
			sanitizeArchiveData(data, version)
			if index == 0 {
				copy.Before = binlog.Row(data[change.Table][0])
			} else {
				copy.After = binlog.Row(data[change.Table][0])
			}
		}
		clean.Changes = append(clean.Changes, copy)
	}
	return clean, nil
}

func equalLogRow(a, b binlog.Row) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if (a[i] == nil) != (b[i] == nil) || !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func logRowKey(table LogTable, row binlog.Row) (string, error) {
	var cells [][]byte
	for _, index := range table.Layout.PrimaryKey {
		if index >= len(row) || row[index] == nil {
			return "", errors.New("log primary key is incomplete")
		}
		cells = append(cells, row[index])
	}
	raw, err := json.Marshal(cells)
	return string(raw), err
}

func applyLogRows(data Data, tables []LogTable, start, end binlog.Position, transactions []binlog.Transaction, schema ...int) (Data, error) {
	version, err := logSchemaVersion(schema...)
	if err != nil {
		return nil, err
	}
	if hasRetentionSchema(version) {
		return nil, errors.New("retention replay requires archive provenance")
	}
	return applyLogRowsValidated(data, tables, start, end, transactions, validateData, schema...)
}

func applyLogRowsValidated(data Data, tables []LogTable, start, end binlog.Position, transactions []binlog.Transaction, validate func(Data) error, schema ...int) (Data, error) {
	contracts, err := logContracts(tables, schema...)
	if err != nil {
		return nil, err
	}
	if validate == nil {
		return nil, errors.New("missing log state validator")
	}
	if err = validate(data); err != nil {
		return nil, err
	}
	if _, err := logSchemaData(data, schema...); err != nil {
		return nil, err
	}
	if order, err := binlog.ComparePositions(start, end); err != nil || order > 0 {
		return nil, errors.New("invalid log replay range")
	}
	indexed := map[string]map[string]Row{}
	catalog := tableCatalog
	if len(schema) == 1 && hasRetentionSchema(schema[0]) {
		catalog, err = snapshotCatalog(schema[0])
		if err != nil {
			return nil, err
		}
	}
	for _, spec := range catalog {
		rows := map[string]Row{}
		for _, raw := range data[spec.name] {
			row, err := normalizeLogRow(contracts[spec.name], binlog.Row(raw), schema...)
			if err != nil {
				return nil, err
			}
			key, err := logRowKey(contracts[spec.name], binlog.Row(row))
			if err != nil {
				return nil, err
			}
			if _, exists := rows[key]; exists {
				return nil, errors.New("duplicate baseline primary key")
			}
			rows[key] = row
		}
		indexed[spec.name] = rows
	}
	cursor := start
	for _, tx := range transactions {
		order, endErr := binlog.ComparePositions(tx.Start, tx.End)
		through, rangeErr := binlog.ComparePositions(tx.End, end)
		if tx.Start != cursor || endErr != nil || rangeErr != nil || order >= 0 || through > 0 || tx.CommittedAt.IsZero() {
			return nil, errors.New("noncontiguous or invalid committed transaction")
		}
		clean, err := prepareLogTransaction(tables, tx, schema...)
		if err != nil {
			return nil, err
		}
		if len(clean.Changes) != len(tx.Changes) {
			return nil, errors.New("excluded data in persisted log")
		}
		for i, change := range clean.Changes {
			if !equalLogRow(change.Before, tx.Changes[i].Before) || !equalLogRow(change.After, tx.Changes[i].After) {
				return nil, errors.New("unsanitized or noncanonical persisted log row")
			}
			rows, table := indexed[change.Table], contracts[change.Table]
			beforeKey, afterKey := "", ""
			if change.Before != nil {
				beforeKey, err = logRowKey(table, change.Before)
				if err != nil {
					return nil, err
				}
				previous, exists := rows[beforeKey]
				if !exists || !equalLogRow(binlog.Row(previous), change.Before) {
					column := -1
					if exists {
						for j := range previous {
							if (previous[j] == nil) != (change.Before[j] == nil) || !bytes.Equal(previous[j], change.Before[j]) {
								column = j
								break
							}
						}
					}
					return nil, fmt.Errorf("log before image does not match recovery state: table=%s column=%d", change.Table, column)
				}
			}
			if change.After != nil {
				afterKey, err = logRowKey(table, change.After)
				if err != nil {
					return nil, err
				}
				if _, exists := rows[afterKey]; exists && (change.Kind == "insert" || afterKey != beforeKey) {
					return nil, errors.New("log insert collides with recovery state")
				}
			}
			if change.Before != nil {
				delete(rows, beforeKey)
			}
			if change.After != nil {
				rows[afterKey] = Row(change.After)
			}
		}
		cursor = tx.End
	}
	result := Data{}
	for _, spec := range catalog {
		rows := indexed[spec.name]
		keys := make([]string, 0, len(rows))
		for key := range rows {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result[spec.name] = nil
		for _, key := range keys {
			result[spec.name] = append(result[spec.name], rows[key])
		}
	}
	if err = validate(result); err != nil {
		return nil, err
	}
	return result, nil
}
