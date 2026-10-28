package backup

import (
	"context"
	"encoding/json"
	"errors"

	"novelstudio/internal/binlog"
)

// The JSONB decoder preserves scalar types but does not promise MySQL's
// whitespace, escaping or floating-point spelling. Schema36 source journals
// bind the actual SQL bytes. Ask the same source engine to render decoded JSON
// before persisting the sanitized row images; offline replay preserves bytes.
// This is a read-only expression over the row image, never a read of the newer
// live row, so both before/after images keep their original transaction value.
func prepareSourceLogTransaction(ctx context.Context, source queryer, tables []LogTable, tx binlog.Transaction, schema int) (binlog.Transaction, error) {
	clean, err := prepareLogTransaction(tables, tx, schema)
	if err != nil || !hasRetentionSchema(schema) {
		return clean, err
	}
	if source == nil {
		return binlog.Transaction{}, errors.New("source JSON renderer unavailable")
	}
	contracts, err := logContracts(tables, schema)
	if err != nil {
		return binlog.Transaction{}, err
	}
	// Cache only within this already size-bounded transaction. Never keep
	// private draft text in a process-global or cross-project cache.
	rendered := map[string][]byte{}
	var renderedBytes int64
	for _, change := range clean.Changes {
		table := contracts[change.Table]
		for _, row := range []binlog.Row{change.Before, change.After} {
			for i, cell := range row {
				if cell == nil || table.Columns[i].Type != "json" {
					continue
				}
				value, exists := rendered[string(cell)]
				if !exists {
					if err := source.QueryRowContext(ctx, "SELECT CAST(CAST(? AS JSON) AS CHAR CHARACTER SET utf8mb4)", string(cell)).Scan(&value); err != nil || !json.Valid(value) {
						return binlog.Transaction{}, errors.New("source JSON rendering failed")
					}
					renderedBytes += int64(len(value))
					if renderedBytes > 64<<20 {
						return binlog.Transaction{}, errors.New("source JSON rendering capacity exceeded")
					}
					rendered[string(cell)] = value
				}
				row[i] = value
			}
		}
	}
	return clean, nil
}
