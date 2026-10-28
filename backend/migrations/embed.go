package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

// PendingFiles holds the historical split schema36 SQL. The application catalog
// registers all three as one exact migration; individual files are never run alone.
//
//go:embed retention.sql.pending retention_identity.sql.pending retention_sources.sql.pending
var PendingFiles embed.FS
