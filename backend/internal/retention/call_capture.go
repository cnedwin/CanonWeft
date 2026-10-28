package retention

import (
	"context"
	"database/sql"
)

// A call's accounting state and its original receipt identities travel
// together. Qualification balances are included only through the actual link.
func captureCallKeys(ctx context.Context, tx *sql.Tx, call string) ([]SourceKey, error) {
	keys := []SourceKey{
		{Table: "model_call_accounting", Primary: map[string]string{"call_id": call}},
		{Table: "qualification_calls", Primary: map[string]string{"call_id": call}, IfPresent: true},
	}
	for _, table := range []string{"model_call_receipts", "model_reconciliations"} {
		// Identifiers are a fixed compiled list; the call ID remains a parameter.
		rows, err := tx.QueryContext(ctx, "SELECT id FROM `"+table+"` WHERE call_id=? ORDER BY id LIMIT 2049 FOR SHARE", call)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if len(keys) >= 2048 {
				rows.Close()
				return nil, ErrSourceOwnership
			}
			keys = append(keys, SourceKey{Table: table, Primary: map[string]string{"id": id}})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}
