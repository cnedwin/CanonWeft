package backup

import (
	"context"
	"database/sql"
	"errors"
	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
	"os"
	"slices"
	"strings"
	"time"
)

// ColumnContract binds the SQL representation used when comparing a snapshot
// with later row images. These fields are metadata, never executable SQL.
type ColumnContract struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	SQLType   string `json:"sql_type"`
	Nullable  bool   `json:"nullable"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
}

type LogTable struct {
	Layout  binlog.TableSchema `json:"layout"`
	Columns []ColumnContract   `json:"columns"`
}

// A baseline becomes durable only when its owner atomically publishes this
// metadata together with the named immutable archive. Capture alone is not
// evidence of a continuously protected interval or a completed log journal.
type LogBaseline struct {
	Format        int             `json:"format"`
	ArchiveSHA256 string          `json:"archive_sha256"`
	Source        Source          `json:"source"`
	Schema        int             `json:"schema"`
	ObservedAt    time.Time       `json:"observed_at"`
	Position      binlog.Position `json:"position"`
	Tables        []LogTable      `json:"tables"`
}

func CreateLogBaseline(ctx context.Context, db *sql.DB, blobs storage.BlobStore, output string) (LogBaseline, Metrics, error) {
	return createLogBaseline(ctx, db, blobs, func() (*os.File, error) {
		return os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	}, func() { _ = os.Remove(output) })
}

func createLogBaseline(ctx context.Context, db *sql.DB, blobs storage.BlobStore, openOutput func() (*os.File, error), removeIncomplete func()) (LogBaseline, Metrics, error) {
	baseline := LogBaseline{Format: 1, Schema: supportedSchemaVersion}
	begin := func(ctx context.Context, conn *sql.Conn) (time.Time, error) {
		var err error
		if baseline.Source, err = sourceIdentity(ctx, conn); err != nil {
			return time.Time{}, errors.New("log baseline source identity unavailable")
		}
		if err = requireLogSource(ctx, conn); err != nil {
			return time.Time{}, err
		}
		// Schema advisory lock is already held. Read the fixed catalog before
		// the short global lock; managed migrations cannot interleave here.
		if err = conn.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&baseline.Schema); err != nil {
			return time.Time{}, errors.New("log baseline source schema unavailable")
		}
		if err = checkTables(ctx, conn, baseline.Schema); err != nil {
			return time.Time{}, err
		}
		if baseline.Tables, err = readLogTables(ctx, conn, baseline.Schema); err != nil {
			return time.Time{}, err
		}
		locked, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if _, err = conn.ExecContext(locked, "FLUSH TABLES WITH READ LOCK"); err != nil {
			return time.Time{}, errors.New("log baseline write boundary unavailable")
		}
		// FLUSH implicitly commits and resets one-shot transaction settings.
		// Re-establish isolation after it, even for a READ COMMITTED session.
		if _, err = conn.ExecContext(locked, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
			return time.Time{}, errors.New("log baseline repeatable read unavailable")
		}
		// captureSnapshot always discards this physical connection on failure;
		// there is no failed-UNLOCK path that can return a locked pooled session.
		if _, err = conn.ExecContext(locked, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
			return time.Time{}, errors.New("log baseline snapshot unavailable")
		}
		var observed string
		if err = conn.QueryRowContext(locked, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&observed); err != nil {
			return time.Time{}, errors.New("log baseline source clock unavailable")
		}
		if baseline.ObservedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", observed, time.UTC); err != nil {
			return time.Time{}, errors.New("invalid source clock")
		}
		var included, excluded, gtids string
		if err = conn.QueryRowContext(locked, "SHOW BINARY LOG STATUS").Scan(&baseline.Position.File, &baseline.Position.Offset, &included, &excluded, &gtids); err != nil {
			return time.Time{}, errors.New("log baseline coordinate unavailable")
		}
		if included != "" || excluded != "" {
			return time.Time{}, errors.New("filtered source logs cannot establish full coverage")
		}
		if _, err = conn.ExecContext(locked, "UNLOCK TABLES"); err != nil {
			return time.Time{}, errors.New("log baseline write lock release failed")
		}
		return baseline.ObservedAt, nil
	}
	metrics, err := captureSnapshot(ctx, db, blobs, openOutput, removeIncomplete, begin)
	if err != nil {
		return LogBaseline{}, metrics, err
	}
	baseline.ArchiveSHA256 = metrics.ArchiveSHA256
	return baseline, metrics, nil
}

func requireLogSource(ctx context.Context, conn *sql.Conn) error {
	var enabled, compressed bool
	var format, image, metadata, checksum, options string
	err := conn.QueryRowContext(ctx, "SELECT @@log_bin,@@binlog_format,@@binlog_row_image,@@binlog_row_metadata,@@binlog_checksum,@@binlog_row_value_options,@@binlog_transaction_compression").Scan(&enabled, &format, &image, &metadata, &checksum, &options, &compressed)
	if err != nil || !enabled || compressed || format != "ROW" || image != "FULL" || metadata != "FULL" || checksum != "CRC32" || options != "" {
		return errors.New("log baseline requires full ROW metadata and CRC32 without partial JSON or compression")
	}
	return nil
}

func logTableCatalog() []tableSpec {
	result := slices.Clone(tableCatalog)
	return append(result, tableSpec{"schema_version", "id,version,checksum,history_initialized"}, tableSpec{"schema_migrations", "version,name,checksum,origin,recorded_at"})
}

func readLogTables(ctx context.Context, conn *sql.Conn, schema ...int) ([]LogTable, error) {
	catalog, err := logSchemaCatalog(schema...)
	if err != nil {
		return nil, err
	}
	var result []LogTable
	for _, spec := range catalog {
		table := LogTable{Layout: binlog.TableSchema{Name: spec.name, Columns: spec.cols()}}
		rows, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME,DATA_TYPE,COLUMN_TYPE,IS_NULLABLE,COALESCE(CHARACTER_SET_NAME,''),COALESCE(COLLATION_NAME,'') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ORDINAL_POSITION", spec.name)
		if err != nil {
			return nil, errors.New("log source column contract unavailable")
		}
		for rows.Next() {
			var column ColumnContract
			var nullable string
			if err = rows.Scan(&column.Name, &column.Type, &column.SQLType, &nullable, &column.Charset, &column.Collation); err != nil {
				rows.Close()
				return nil, errors.New("log source column contract unreadable")
			}
			column.Nullable = nullable == "YES"
			if len(table.Columns) >= len(table.Layout.Columns) || column.Name != table.Layout.Columns[len(table.Columns)] {
				rows.Close()
				return nil, errors.New("log source columns changed")
			}
			table.Columns = append(table.Columns, column)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(table.Columns) != len(table.Layout.Columns) {
			return nil, errors.New("incomplete log source column contract")
		}
		keys, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME,COALESCE(SUB_PART,0) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY SEQ_IN_INDEX", spec.name)
		if err != nil {
			return nil, errors.New("log source key contract unavailable")
		}
		for keys.Next() {
			var name string
			var prefix int
			if err = keys.Scan(&name, &prefix); err != nil {
				keys.Close()
				return nil, errors.New("log source key contract unreadable")
			}
			index := slices.Index(table.Layout.Columns, name)
			if index < 0 || prefix != 0 || slices.Contains(table.Layout.PrimaryKey, index) {
				keys.Close()
				return nil, errors.New("unsupported log source key")
			}
			table.Layout.PrimaryKey = append(table.Layout.PrimaryKey, index)
		}
		err = keys.Err()
		keys.Close()
		if err != nil || len(table.Layout.PrimaryKey) == 0 {
			return nil, errors.New("log source requires complete primary keys")
		}
		result = append(result, table)
	}
	return result, nil
}

func sameLogTableCatalog(a, b []LogTable) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Layout.Name != b[i].Layout.Name || strings.Join(a[i].Layout.Columns, ",") != strings.Join(b[i].Layout.Columns, ",") || !slices.Equal(a[i].Layout.PrimaryKey, b[i].Layout.PrimaryKey) || !slices.Equal(a[i].Columns, b[i].Columns) {
			return false
		}
	}
	return true
}
