package backup

import (
	"novelstudio/internal/binlog"
	"time"
)

func (r *LogReplay) stateReferences(data Data, at time.Time) (map[string]bool, error) {
	if !hasRetentionSchema(r.baseline.Schema) {
		return blobReferences(data)
	}
	business, metadata, err := splitRetentionData(r.baseline.Schema, data)
	if err != nil {
		return nil, err
	}
	manifest := r.archive.Manifest
	manifest.SnapshotAt = at
	return validatedRetentionReferences(&Archive{Manifest: manifest, Data: data}, business, metadata)
}

func (r *LogReplay) applyRows(data Data, start, end binlog.Position, transactions []binlog.Transaction, at time.Time) (Data, error) {
	if !hasRetentionSchema(r.baseline.Schema) {
		return applyLogRows(data, r.baseline.Tables, start, end, transactions, r.baseline.Schema)
	}
	validate := func(d Data) error { _, err := r.stateReferences(d, at); return err }
	return applyLogRowsValidated(data, r.baseline.Tables, start, end, transactions, validate, r.baseline.Schema)
}

func (r *LogReplay) recoveryTime(previous, committed, observed time.Time) time.Time {
	if hasRetentionSchema(r.baseline.Schema) {
		// Binlog headers have only second precision; source evidence has
		// microseconds. Positions select the exact commit boundary, while the
		// verified observation is the upper clock bound for its contents.
		return observed
	}
	return logPointTime(previous, committed, observed)
}
