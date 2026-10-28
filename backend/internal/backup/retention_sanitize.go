package backup

// Schema is checked by the archive/log caller. Source history commits exact
// diagnostic bytes and accounting state; cancellation is independent authority,
// not a new provider result. Keep those bytes while disabling every executor.
// Legacy exports retain their established canonical form for log replay.
func sanitizeArchiveData(d Data, schema int) {
	sanitizeWithSourceHistory(d, hasRetentionSchema(schema))
}
