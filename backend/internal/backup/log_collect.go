package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
	"slices"
	"time"
)

// CaptureSegment observes and reads an actual source range. The caller must
// hold its repository writer lease while capturing, publishing and applying.
// Only sanitized rows and referenced immutable object bytes leave this method.
// It does not advance the durable point: publish first, then ApplySegment.
func (r *LogReplay) CaptureSegment(ctx context.Context, db *sql.DB, reader *binlog.Reader, blobs storage.BlobStore) ([]byte, error) {
	var raw []byte
	err := r.withLogCapture(ctx, db, reader, func(ctx context.Context, source queryer, layouts []binlog.TableSchema, through binlog.Position, observed time.Time) error {
		var transactions []binlog.Transaction
		var rows int
		var size int64
		err := reader.ReadRange(ctx, r.baseline.Source.Database, layouts, r.point.Position, through, func(tx binlog.Transaction) error {
			if len(transactions) >= 1000 {
				return errors.New("log segment transaction limit")
			}
			clean, err := prepareSourceLogTransaction(ctx, source, r.baseline.Tables, tx, r.baseline.Schema)
			if err != nil {
				return err
			}
			rows += len(clean.Changes)
			size += logCaptureTransactionBytes(clean)
			if rows > 100000 || size > 64<<20 {
				return errors.New("log segment row capacity exceeded")
			}
			transactions = append(transactions, clean)
			return nil
		})
		if err != nil {
			return err
		}
		raw, err = r.PrepareSegment(ctx, blobs, through, observed, transactions)
		return err
	})
	return raw, err
}

// CaptureSegments publishes complete bounded prefixes while reading one actual
// observed range. The caller holds the repository writer lease; publish must
// durably append these bytes and return their exact point. An error returns the
// last acknowledged prefix, never a claim of coverage through the unread tail.
// An ambiguous publish error requires reloading the repository before retrying.
func (r *LogReplay) CaptureSegments(ctx context.Context, db *sql.DB, reader *binlog.Reader, blobs storage.BlobStore, publish func(context.Context, []byte) (LogPoint, error)) (LogPoint, error) {
	if r == nil || publish == nil {
		return LogPoint{}, errors.New("log capture publisher unavailable")
	}
	err := r.withLogCapture(ctx, db, reader, func(ctx context.Context, source queryer, layouts []binlog.TableSchema, through binlog.Position, observed time.Time) error {
		if observed.Before(r.point.ObservedAt) {
			return errors.New("source observation clock moved backwards")
		}
		batch := newLogCaptureBatch(ctx, r, blobs, observed, func(raw []byte) (LogPoint, error) { return publish(ctx, raw) })
		if err := reader.ReadRange(ctx, r.baseline.Source.Database, layouts, r.point.Position, through, func(tx binlog.Transaction) error {
			clean, err := prepareSourceLogTransaction(ctx, source, r.baseline.Tables, tx, r.baseline.Schema)
			if err != nil {
				return err
			}
			return batch.consume(clean)
		}); err != nil {
			return err
		}
		_, err := batch.finish(through)
		return err
	})
	return r.Point(), err
}

// Both capture APIs share the same verified source connection, schema lease,
// catalog checks, clock observation and final source coordinate.
func (r *LogReplay) withLogCapture(ctx context.Context, db *sql.DB, reader *binlog.Reader, consumeRange func(context.Context, queryer, []binlog.TableSchema, binlog.Position, time.Time) error) error {
	if r == nil || db == nil || reader == nil {
		return errors.New("log capture dependencies unavailable")
	}
	if reader.ExpectedServerUUID() != r.baseline.Source.ServerUUID {
		return errors.New("log reader belongs to a different source")
	}
	// Closing the replication socket cannot cancel its synchronous consumer.
	// The schema lease, JSON rendering, validation and publish share one budget.
	ctx, cancel := context.WithTimeout(ctx, binlog.ReadTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.New("log capture source unavailable")
	}
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var locked int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':schema'),10)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("log capture schema lock unavailable")
	}
	source, err := sourceIdentity(ctx, conn)
	if err != nil || source != r.baseline.Source {
		return errors.New("log capture source changed")
	}
	if err := requireLogSource(ctx, conn); err != nil {
		return err
	}
	history, err := readHistory(ctx, conn, r.baseline.Schema)
	if err != nil || !slices.Equal(history, r.archive.Manifest.Migrations) {
		return errors.New("source migration changed; new log baseline required")
	}
	if err := checkTables(ctx, conn, r.baseline.Schema); err != nil {
		return errors.New("source schema changed; new log baseline required")
	}
	tables, err := readLogTables(ctx, conn, r.baseline.Schema)
	if err != nil || !sameLogTableCatalog(tables, r.baseline.Tables) {
		return errors.New("source row contract changed; new log baseline required")
	}
	var through binlog.Position
	var included, excluded, gtids string
	if err := conn.QueryRowContext(ctx, "SHOW BINARY LOG STATUS").Scan(&through.File, &through.Offset, &included, &excluded, &gtids); err != nil || included != "" || excluded != "" {
		return errors.New("full source observation boundary unavailable")
	}
	// Read the upper clock bound after the position. A commit between the
	// two queries must never be included with an earlier observation time.
	var clock string
	if err := conn.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return errors.New("source observation clock unavailable")
	}
	observed, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return errors.New("invalid source observation clock")
	}
	var layouts []binlog.TableSchema
	for _, table := range tables {
		layouts = append(layouts, table.Layout)
	}
	return consumeRange(ctx, conn, layouts, through, observed)
}

type logCaptureLimits struct {
	transactions, rows int
	bytes              int64
}
type logCaptureBatch struct {
	ctx          context.Context
	replay       *LogReplay
	blobs        storage.BlobStore
	observed     time.Time
	publish      func([]byte) (LogPoint, error)
	limits       logCaptureLimits
	transactions []binlog.Transaction
	rows         int
	bytes        int64
}

func newLogCaptureBatch(ctx context.Context, replay *LogReplay, blobs storage.BlobStore, observed time.Time, publish func([]byte) (LogPoint, error)) *logCaptureBatch {
	return &logCaptureBatch{ctx: ctx, replay: replay, blobs: blobs, observed: observed, publish: publish, limits: logCaptureLimits{transactions: 1000, rows: 100000, bytes: 64 << 20}}
}

func logCaptureTransactionBytes(tx binlog.Transaction) int64 {
	var size int64
	for _, change := range tx.Changes {
		for _, row := range []binlog.Row{change.Before, change.After} {
			for _, cell := range row {
				size += int64(len(cell)) + 32
			}
		}
	}
	return size
}

func (b *logCaptureBatch) consume(tx binlog.Transaction) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	clean, err := prepareLogTransaction(b.replay.baseline.Tables, tx, b.replay.baseline.Schema)
	if err != nil {
		return err
	}
	size := logCaptureTransactionBytes(clean)
	if len(clean.Changes) > b.limits.rows || size > b.limits.bytes {
		return errors.New("single log transaction exceeds segment capacity")
	}
	if len(b.transactions) > 0 && (len(b.transactions) >= b.limits.transactions || b.rows+len(clean.Changes) > b.limits.rows || b.bytes+size > b.limits.bytes) {
		if err := b.flushPrefix(); err != nil {
			return err
		}
	}
	b.transactions = append(b.transactions, clean)
	b.rows += len(clean.Changes)
	b.bytes += size
	if len(b.transactions) >= b.limits.transactions || b.rows >= b.limits.rows || b.bytes >= b.limits.bytes {
		return b.flushPrefix()
	}
	return nil
}

func (b *logCaptureBatch) flushPrefix() error {
	last := b.transactions[len(b.transactions)-1]
	observed := b.replay.recoveryTime(b.replay.point.ObservedAt, last.CommittedAt, b.observed)
	return b.flush(last.End, observed)
}

func (b *logCaptureBatch) flush(through binlog.Position, observed time.Time) error {
	if err := b.flushTransactions(b.transactions, through, observed); err != nil {
		return err
	}
	b.transactions = nil
	b.rows, b.bytes = 0, 0
	return nil
}

func (b *logCaptureBatch) flushTransactions(transactions []binlog.Transaction, through binlog.Position, observed time.Time) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	prepared, err := b.replay.prepareSegment(b.ctx, b.blobs, through, observed, transactions)
	if errors.Is(err, errLogSegmentCapacity) {
		if len(transactions) == 1 {
			return fmt.Errorf("single log transaction exceeds segment capacity: %w", err)
		}
		if len(transactions) > 1 {
			// Object bytes and their JSON expansion are known only after prepare.
			// Split at a complete transaction, applying its acknowledged prefix
			// before preparing the suffix against the new point and object set.
			mid := len(transactions) / 2
			last := transactions[mid-1]
			prefixObserved := b.replay.recoveryTime(b.replay.point.ObservedAt, last.CommittedAt, observed)
			if err := b.flushTransactions(transactions[:mid], last.End, prefixObserved); err != nil {
				return err
			}
			return b.flushTransactions(transactions[mid:], through, observed)
		}
	}
	if err != nil {
		return err
	}
	raw := prepared.raw
	expected := b.replay.Point()
	expected.Position, expected.ObservedAt, expected.Hash = through, observed, digest(raw)
	if len(transactions) > 0 {
		expected.LastCommitAt = transactions[len(transactions)-1].CommittedAt
	}
	point, err := b.publish(raw)
	if err != nil {
		return err
	}
	if point != expected {
		return errors.New("published log segment receipt mismatch; reload required")
	}
	// Even if cancellation arrived during publish, this acknowledged prefix is
	// already durable. Apply it before returning cancellation at the next boundary.
	// Preparation already validated every intermediate state. Install that
	// exact state after its durable acknowledgement, even if cancellation has
	// arrived, rather than starting another uncancellable full replay.
	if err := prepared.apply(b.replay); err != nil {
		return err
	}
	return nil
}

func (b *logCaptureBatch) finish(through binlog.Position) (LogPoint, error) {
	if err := b.ctx.Err(); err != nil {
		return b.replay.Point(), err
	}
	if len(b.transactions) == 0 && through == b.replay.point.Position && b.observed.Equal(b.replay.point.ObservedAt) {
		return b.replay.Point(), nil
	}
	err := b.flush(through, b.observed)
	return b.replay.Point(), err
}
