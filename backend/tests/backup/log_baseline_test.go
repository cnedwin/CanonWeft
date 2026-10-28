package backup_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type delayedBaselineBlobs struct {
	storage.BlobStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// Delegate all IO to real MySQL; only pause between two snapshot table reads.
// The connector configures real READ COMMITTED sessions to expose an implicit
// commit resetting a one-shot isolation setting during FLUSH TABLES.
type pausedSnapshotConnector struct {
	driver.Connector
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	afterLock bool
}

func (c *pausedSnapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pausedSnapshotConn{Conn: conn, owner: c}, nil
}

type pausedSnapshotConn struct {
	driver.Conn
	owner *pausedSnapshotConnector
}

func (c *pausedSnapshotConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && c.owner.afterLock && query == "FLUSH TABLES WITH READ LOCK" {
		c.owner.once.Do(func() { close(c.owner.entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return result, err
}

func (c *pausedSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "SELECT id,workspace_id,name,revision,created_at,deleted_at FROM projects" {
		c.owner.once.Do(func() { close(c.owner.entered) })
		select {
		case <-c.owner.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestLogBaselineRetainsRepeatableReadAfterFlushWithSessionReadCommitted(t *testing.T) {
	other := isolated(t)
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	execute(t, f.db, "UPDATE workspaces SET name='before' WHERE id=?", f.workspace)
	execute(t, f.db, "UPDATE projects SET name='before' WHERE id=?", f.project)
	config := f.config.Clone()
	if config.Params == nil {
		config.Params = map[string]string{}
	}
	config.Params["transaction_isolation"] = "'READ-COMMITTED'"
	base, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	connector := &pausedSnapshotConnector{Connector: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(connector.release) })
	source := sql.OpenDB(connector)
	defer source.Close()
	var actualIsolation string
	if err := source.QueryRowContext(ctx, "SELECT @@transaction_isolation").Scan(&actualIsolation); err != nil || actualIsolation != "READ-COMMITTED" {
		t.Fatal("fixture did not select actual session READ COMMITTED", err)
	}
	archive := filepath.Join(f.root, "read-committed-log-baseline.zip")
	type captured struct {
		baseline backup.LogBaseline
		err      error
	}
	result := make(chan captured, 1)
	go func() {
		b, _, err := backup.CreateLogBaseline(ctx, source, f.blobs, archive)
		result <- captured{b, err}
	}()
	select {
	case <-connector.entered:
	case r := <-result:
		t.Fatal("capture did not reach cross-table boundary", r.err)
	case <-ctx.Done():
		t.Fatal("capture stalled before table read")
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE workspaces SET name='after' WHERE id=?", f.workspace); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET name='after' WHERE id=?", f.project); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The source stream is shared with other databases. Their committed
	// transactions still advance the coverage boundary, but own no source rows.
	execute(t, other.db, "UPDATE projects SET name='unrelated commit' WHERE id=?", other.project)
	release.Do(func() { close(connector.release) })
	c := <-result
	if c.err != nil {
		t.Fatal("capture failed", c.err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	a, err := backup.ValidateArchive(file, stat.Size())
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Data["workspaces"][0][1]) != "before" || string(a.Data["projects"][0][2]) != "before" {
		t.Fatal("snapshot mixed commits after its log coordinate")
	}
	var through binlog.Position
	var included, excluded, gtids string
	if err := f.db.QueryRowContext(ctx, "SHOW BINARY LOG STATUS").Scan(&through.File, &through.Offset, &included, &excluded, &gtids); err != nil {
		t.Fatal(err)
	}
	reader, err := binlog.NewReader(f.config.FormatDSN(), c.baseline.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	var tables []binlog.TableSchema
	for _, table := range c.baseline.Tables {
		tables = append(tables, table.Layout)
	}
	var transactions []binlog.Transaction
	err = reader.ReadRange(ctx, f.config.DBName, tables, c.baseline.Position, through, func(tx binlog.Transaction) error { transactions = append(transactions, tx); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var matching []binlog.Transaction
	empty := 0
	boundary := c.baseline.Position
	for _, transaction := range transactions {
		if transaction.Start != boundary {
			t.Fatal("transaction coverage has a gap")
		}
		boundary = transaction.End
		if len(transaction.Changes) == 0 {
			empty++
		} else {
			matching = append(matching, transaction)
		}
	}
	if boundary != through || empty < 1 || len(matching) != 1 || len(matching[0].Changes) != 2 {
		t.Fatal("wrong source transaction or coverage", len(matching), empty)
	}
	seen := map[string]bool{}
	for _, change := range matching[0].Changes {
		index, id := 1, f.workspace
		if change.Table == "projects" {
			index, id = 2, f.project
		} else if change.Table != "workspaces" {
			t.Fatal("foreign table included")
		}
		if seen[change.Table] || change.Kind != "update" || len(change.Before) <= index || len(change.After) <= index || string(change.Before[0]) != id || string(change.After[0]) != id || string(change.Before[index]) != "before" || string(change.After[index]) != "after" {
			t.Fatal("target row was lost, duplicated or replaced")
		}
		seen[change.Table] = true
	}
}

func TestLogBaselineCancellationDiscardsLockedSessionAndPublishesNothing(t *testing.T) {
	f := isolated(t)
	base, err := mysql.NewConnector(f.config)
	if err != nil {
		t.Fatal(err)
	}
	connector := &pausedSnapshotConnector{Connector: base, entered: make(chan struct{}), afterLock: true}
	source := sql.OpenDB(connector)
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(1)
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beforeID, afterID uint64
	if err := source.QueryRow("SELECT CONNECTION_ID()").Scan(&beforeID); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.root, "cancelled-log-baseline.zip")
	type captured struct {
		baseline backup.LogBaseline
		err      error
	}
	result := make(chan captured, 1)
	go func() {
		b, _, err := backup.CreateLogBaseline(ctx, source, f.blobs, archive)
		result <- captured{b, err}
	}()
	select {
	case <-connector.entered:
	case c := <-result:
		t.Fatal("capture did not acquire lock", c.err)
	case <-time.After(5 * time.Second):
		t.Fatal("capture never reached owned lock")
	}
	cancel()
	select {
	case c := <-result:
		if c.err == nil || c.baseline.ArchiveSHA256 != "" {
			t.Fatal("cancelled capture published baseline")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled capture retained owned lock")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("cancelled baseline left a recovery archive")
	}
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := f.db.ExecContext(check, "UPDATE projects SET name='write after cancellation' WHERE id=?", f.project); err != nil {
		t.Fatal("global read lock survived cancellation", err)
	}
	conn, err := f.db.Conn(check)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var acquired int
	if err := conn.QueryRowContext(check, "SELECT GET_LOCK(CONCAT(DATABASE(), ':schema'),0)").Scan(&acquired); err != nil || acquired != 1 {
		t.Fatal("schema lock survived cancellation", err)
	}
	if err := source.QueryRowContext(check, "SELECT CONNECTION_ID()").Scan(&afterID); err != nil || afterID == beforeID {
		t.Fatal("locked physical session returned to the pool", err)
	}
}

func (b *delayedBaselineBlobs) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.BlobStore.ReadObject(ctx, project, hash)
}

func TestLogBaselineCoordinatesMatchSnapshotAndReleaseWriteLockBeforeBlobs(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	branch, resource := service.NewID(), service.NewID()
	hash := f.put(t, "需要完整恢复的正文")
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	execute(t, f.db, "INSERT INTO resources(id,project_id,branch_id,name,kind,content_hash) VALUES(?,?,?,'正文','document',?)", resource, f.project, branch, hash)
	blobs := &delayedBaselineBlobs{BlobStore: f.blobs, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(blobs.release) })
	type capture struct {
		baseline backup.LogBaseline
		metrics  backup.Metrics
		err      error
	}
	result := make(chan capture, 1)
	archive := filepath.Join(f.root, "log-baseline.zip")
	go func() { b, m, err := backup.CreateLogBaseline(ctx, f.db, blobs, archive); result <- capture{b, m, err} }()
	select {
	case <-blobs.entered:
	case c := <-result:
		t.Fatal("baseline failed before object copy", c.err)
	case <-ctx.Done():
		t.Fatal("baseline did not reach object copy")
	}
	written := make(chan error, 1)
	go func() {
		_, err := f.db.ExecContext(ctx, "UPDATE projects SET name='after snapshot' WHERE id=?", f.project)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("global source write lock held during object copy")
	}
	release.Do(func() { close(blobs.release) })
	c := <-result
	if c.err != nil {
		t.Fatal("log baseline failed", c.err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if c.baseline.ArchiveSHA256 != hex.EncodeToString(sum[:]) || c.baseline.Source.Database != f.config.DBName || c.baseline.ObservedAt.IsZero() || c.baseline.Position.Offset < 4 || len(c.baseline.Tables) < 2 {
		t.Fatal("baseline identity not bound to the actual snapshot")
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	a, err := backup.ValidateArchive(file, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Data["projects"][0][2]) != "project" || !a.Manifest.SnapshotAt.Equal(c.baseline.ObservedAt) {
		t.Fatal("snapshot included later commit or used a different observation time")
	}
	var through binlog.Position
	var included, excluded, gtids string
	if err := f.db.QueryRowContext(ctx, "SHOW BINARY LOG STATUS").Scan(&through.File, &through.Offset, &included, &excluded, &gtids); err != nil {
		t.Fatal(err)
	}
	reader, err := binlog.NewReader(f.config.FormatDSN(), c.baseline.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	var tables []binlog.TableSchema
	for _, table := range c.baseline.Tables {
		tables = append(tables, table.Layout)
	}
	var changes []binlog.Change
	err = reader.ReadRange(ctx, f.config.DBName, tables, c.baseline.Position, through, func(tx binlog.Transaction) error { changes = append(changes, tx.Changes...); return nil })
	if err != nil || len(changes) != 1 || changes[0].Table != "projects" || string(changes[0].Before[2]) != "project" || string(changes[0].After[2]) != "after snapshot" {
		t.Fatal("snapshot coordinate lost or duplicated the concurrent commit", err)
	}
}
