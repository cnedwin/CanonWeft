package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"novelstudio/internal/storage"
	"os"
	"strconv"
	"strings"
	"time"
)

type Metrics struct {
	ArchiveSHA256      string    `json:"archive_sha256,omitempty"`
	SnapshotAt         time.Time `json:"snapshot_at"`
	ElapsedMS          int64     `json:"elapsed_ms"`
	ObjectCount        int       `json:"object_count"`
	ObjectBytes        int64     `json:"object_bytes"`
	RowCount           int       `json:"row_count"`
	TargetDatabase     string    `json:"target_database,omitempty"`
	AuthorityCheckedAt time.Time `json:"authority_checked_at,omitempty"`
	RecoveryBoundary   string    `json:"recovery_boundary"`
}

const recoveryBoundary = "Application content and accounting only; identities disabled, memberships/bindings absent, jobs stopped; no Temporal history or credentials. Manual reconciliation and rebinding required. Protected ledger required independently of business DB; same MySQL physical failure domain, not whole-server disaster recovery."

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sourceIdentity(ctx context.Context, q queryer) (Source, error) {
	var s Source
	err := q.QueryRowContext(ctx, "SELECT @@server_uuid,DATABASE()").Scan(&s.ServerUUID, &s.Database)
	return s, err
}
func snapshotCatalog(schema int) ([]tableSpec, error) {
	if schema == supportedSchemaVersion {
		return tableCatalog, nil
	}
	if hasRetentionSchema(schema) {
		catalog := append([]tableSpec(nil), tableCatalog...)
		if schema >= 37 {
			catalog = append(catalog, novelCatalogForSchema(schema)...)
		}
		return append(catalog, retentionTableCatalog()...), nil
	}
	return nil, errors.New("unsupported source schema")
}

func readHistory(ctx context.Context, q queryer, schema ...int) ([]Migration, error) {
	expected := supportedSchemaVersion
	if len(schema) > 1 {
		return nil, errors.New("ambiguous source schema")
	}
	if len(schema) == 1 {
		expected = schema[0]
	}
	if _, err := snapshotCatalog(expected); err != nil {
		return nil, err
	}
	var version int
	if err := q.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil {
		return nil, err
	}
	if version != expected {
		return nil, errors.New("unsupported source schema")
	}
	rows, err := q.QueryContext(ctx, "SELECT version,name,checksum,origin FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []Migration
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.Version, &m.Name, &m.SHA256, &m.Origin); err != nil {
			return nil, err
		}
		history = append(history, m)
	}
	return history, rows.Err()
}
func checkTables(ctx context.Context, q queryer, schema ...int) error {
	version := supportedSchemaVersion
	if len(schema) > 1 {
		return errors.New("ambiguous source schema")
	}
	if len(schema) == 1 {
		version = schema[0]
	}
	catalog, err := snapshotCatalog(version)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, "SELECT TABLE_NAME,ENGINE FROM information_schema.tables WHERE table_schema=DATABASE()")
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var name, engine string
		if err := rows.Scan(&name, &engine); err != nil {
			rows.Close()
			return err
		}
		if engine != "InnoDB" {
			rows.Close()
			return errors.New("nontransactional source table")
		}
		found[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(found) != len(catalog)+2 || !found["schema_version"] || !found["schema_migrations"] {
		return errors.New("source table catalog mismatch")
	}
	for _, s := range catalog {
		if !found[s.name] {
			return errors.New("missing source table")
		}
		rows, err := q.QueryContext(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", s.name)
		if err != nil {
			return err
		}
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			cols = append(cols, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if strings.Join(cols, ",") != s.columns {
			return errors.New("source column catalog mismatch")
		}
	}
	return nil
}
func readRows(ctx context.Context, q queryer, s tableSpec) ([]Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+s.columns+" FROM "+s.name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Row
	var size int64
	for rows.Next() {
		r := make(Row, len(s.cols()))
		values := make([]any, len(r))
		dest := make([]any, len(r))
		for i := range r {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, value := range values {
			switch v := value.(type) {
			case nil:
			case []byte:
				r[i] = append([]byte{}, v...)
			case string:
				r[i] = []byte(v)
			case time.Time:
				// DATETIME has no timezone. Preserve its wall-clock fields,
				// not database/sql's RFC3339 serialization for parseTime DSNs.
				r[i] = []byte(v.Format("2006-01-02 15:04:05.999999"))
			case int64:
				r[i] = []byte(strconv.FormatInt(v, 10))
			case uint64:
				r[i] = []byte(strconv.FormatUint(v, 10))
			case float64:
				r[i] = []byte(strconv.FormatFloat(v, 'g', -1, 64))
			case bool:
				r[i] = []byte("0")
				if v {
					r[i] = []byte("1")
				}
			default:
				return nil, errors.New("unsupported database cell type")
			}
		}
		for _, cell := range r {
			size += int64(len(cell))
		}
		if size > maxDataBytes/2 || len(result) >= 200000 {
			return nil, errors.New("snapshot data capacity limit")
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Create holds the same advisory schema lock as RunMigrations. All application
// reads use one explicit REPEATABLE READ snapshot; blobs are immutable by hash.
// Unmanaged concurrent DDL is outside this contract and must not run.
func Create(ctx context.Context, db *sql.DB, blobs storage.BlobStore, output string) (Metrics, error) {
	return createSnapshot(ctx, db, blobs, func() (*os.File, error) {
		return os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	}, func() { os.Remove(output) })
}

// The repository supplies rooted file operations so directory replacement or
// links cannot redirect automatic captures outside its owned directory.
func createSnapshot(ctx context.Context, db *sql.DB, blobs storage.BlobStore, openOutput func() (*os.File, error), removeIncomplete func()) (Metrics, error) {
	return captureSnapshot(ctx, db, blobs, openOutput, removeIncomplete, nil)
}

type snapshotBegin func(context.Context, *sql.Conn) (time.Time, error)

func captureSnapshot(ctx context.Context, db *sql.DB, blobs storage.BlobStore, openOutput func() (*os.File, error), removeIncomplete func(), begin snapshotBegin) (Metrics, error) {
	started := time.Now()
	metrics := Metrics{RecoveryBoundary: recoveryBoundary}
	if db == nil || blobs == nil {
		return metrics, errors.New("source and blobs required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return metrics, err
	}
	defer conn.Close()
	// The capture owns session locks and an explicit SQL transaction. Never
	// return that physical connection to the pool, even if cancellation made
	// SQL cleanup impossible. Closing it releases all server-side locks.
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':schema'),10)").Scan(&locked); err != nil || locked != 1 {
		return metrics, errors.New("source schema lock unavailable")
	}
	if _, err = conn.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
		return metrics, err
	}
	observedAt := time.Now().UTC()
	if begin == nil {
		if _, err = conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
			return metrics, err
		}
	} else {
		if observedAt, err = begin(ctx, conn); err != nil {
			return metrics, err
		}
	}
	a := &Archive{Data: Data{}, Blobs: map[string][]byte{}, Manifest: Manifest{Format: 1, Schema: storage.SchemaVersion, Exclusions: exclusions}}
	if err = conn.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&a.Manifest.Schema); err != nil {
		return metrics, err
	}
	if hasRetentionSchema(a.Manifest.Schema) {
		a.Manifest.Exclusions = retentionExclusions
	}
	catalog, err := snapshotCatalog(a.Manifest.Schema)
	if err != nil {
		return metrics, err
	}
	if hasRetentionSchema(a.Manifest.Schema) && begin == nil {
		var clock string
		if err = conn.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
			return metrics, err
		}
		observedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
		if err != nil {
			return metrics, errors.New("invalid snapshot source clock")
		}
	}
	a.Manifest.SnapshotAt = observedAt
	metrics.SnapshotAt = a.Manifest.SnapshotAt
	if a.Manifest.Source, err = sourceIdentity(ctx, conn); err != nil {
		return metrics, err
	}
	provenance, err := configuredLedger().ProvenanceTx(ctx, conn)
	if err != nil {
		return metrics, err
	}
	a.Manifest.Authority = &Provenance{LedgerID: provenance.LedgerID, Protocol: provenance.Protocol}
	if a.Manifest.Migrations, err = readHistory(ctx, conn, a.Manifest.Schema); err != nil {
		return metrics, err
	}
	if err = validateManifest(a.Manifest); err != nil {
		return metrics, err
	}
	if err = checkTables(ctx, conn, a.Manifest.Schema); err != nil {
		return metrics, err
	}
	var total int64
	for _, s := range catalog {
		if excludedTableForSchema(s.name, a.Manifest.Schema) {
			a.Data[s.name] = nil
			continue
		}
		rows, err := readRows(ctx, conn, s)
		if err != nil {
			return metrics, fmt.Errorf("snapshot table %s: %w", s.name, err)
		}
		a.Data[s.name] = rows
		metrics.RowCount += len(rows)
		for _, r := range rows {
			for _, cell := range r {
				total += int64(len(cell))
			}
		}
		if total > maxDataBytes/2 {
			return metrics, errors.New("snapshot capacity limit")
		}
	}
	sanitizeArchiveData(a.Data, a.Manifest.Schema)
	var refs map[string]bool
	if hasRetentionSchema(a.Manifest.Schema) {
		business, metadata, splitErr := splitRetentionData(a.Manifest.Schema, a.Data)
		if splitErr != nil {
			return metrics, splitErr
		}
		refs, err = validatedRetentionReferences(a, business, metadata)
	} else {
		if err = validateData(a.Data); err != nil {
			return metrics, err
		}
		refs, err = blobReferences(a.Data)
	}
	if err != nil {
		return metrics, err
	}
	for path := range refs {
		parts := strings.Split(path, "/")
		raw, err := blobs.ReadObject(ctx, parts[1], parts[2])
		if err != nil {
			return metrics, fmt.Errorf("required object unavailable: %s", path)
		}
		if int64(len(raw)) > storage.MaxBlobBytes || digest(raw) != parts[2] {
			return metrics, errors.New("required object corrupt")
		}
		metrics.ObjectBytes += int64(len(raw))
		if metrics.ObjectBytes+total > maxArchiveBytes {
			return metrics, errors.New("snapshot capacity limit")
		}
		a.Blobs[path] = raw
	}
	if err = validateMemoryObjects(a.Data, a.Blobs, hasRetentionSchema(a.Manifest.Schema)); err != nil {
		return metrics, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return metrics, err
	}
	// O_EXCL protects existing backups. Incomplete files created by this operation
	// are removed; no source data or existing destination is ever removed.
	f, err := openOutput()
	if err != nil {
		return metrics, err
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			removeIncomplete()
		}
	}()
	if err = writeArchive(f, a); err != nil {
		return metrics, err
	}
	if err = f.Sync(); err != nil {
		return metrics, err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return metrics, err
	}
	if _, err = validateArchiveContents(f, size); err != nil {
		return metrics, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return metrics, err
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, io.LimitReader(f, maxArchiveBytes+1)); err != nil {
		return metrics, err
	}
	metrics.ArchiveSHA256 = hex.EncodeToString(hasher.Sum(nil))
	if err = f.Close(); err != nil {
		return metrics, err
	}
	success = true
	metrics.ObjectCount = len(a.Blobs)
	metrics.ElapsedMS = time.Since(started).Milliseconds()
	return metrics, nil
}
