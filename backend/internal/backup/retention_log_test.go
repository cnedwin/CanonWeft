package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

func retentionLogFixture(t *testing.T) (*LogReplay, *Archive, LogBaseline) {
	t.Helper()
	a, _ := archiveCleanupFixture(t)
	sanitizeArchiveData(a.Data, retentionSchemaVersion)
	catalog, err := snapshotCatalog(retentionSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Real key contracts are separately exercised by the MySQL baseline test.
	// These fixtures mutate only tables whose primary key is the first column.
	catalog = append(catalog, logTableCatalog()[len(tableCatalog):]...)
	var tables []LogTable
	for _, spec := range catalog {
		table := LogTable{Layout: binlog.TableSchema{Name: spec.name, Columns: spec.cols(), PrimaryKey: []int{0}}}
		for _, column := range spec.cols() {
			table.Columns = append(table.Columns, ColumnContract{Name: column, Type: "varchar", SQLType: "varchar(255)", Charset: "utf8mb4", Collation: "utf8mb4_bin"})
		}
		tables = append(tables, table)
	}
	raw := archiveBytes(t, a)
	b := LogBaseline{Format: 1, ArchiveSHA256: digest(raw), Source: a.Manifest.Source, Schema: retentionSchemaVersion, ObservedAt: a.Manifest.SnapshotAt, Position: binlog.Position{File: "binlog.000001", Offset: 100}, Tables: tables}
	r, err := OpenLogReplay(b, raw)
	if err != nil {
		t.Fatal("retention baseline rejected", err)
	}
	return r, a, b
}

func TestRetentionLogReplaysBusinessAndMetadataAtCommittedBoundaries(t *testing.T) {
	r, a, b := retentionLogFixture(t)
	copyRow := func(row Row) Row {
		out := make(Row, len(row))
		for i, cell := range row {
			if cell != nil {
				out[i] = append([]byte{}, cell...)
			}
		}
		return out
	}
	projectBefore := a.Data["projects"][0]
	projectAfter := copyRow(projectBefore)
	memorySpec("projects").set(projectAfter, "name", "after transaction")
	lifecycleBefore := a.Data["project_lifecycle"][0]
	lifecycleAfter := copyRow(lifecycleBefore)
	for _, spec := range retentionTableCatalog() {
		if spec.name == "project_lifecycle" {
			spec.set(lifecycleAfter, "inventory_revision", "4")
			spec.set(lifecycleAfter, "updated_at", b.ObservedAt.Add(time.Second).Format("2006-01-02 15:04:05.000000"))
		}
	}
	end := binlog.Position{File: b.Position.File, Offset: 200}
	tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{
		{Table: "projects", Kind: "update", Before: binlog.Row(projectBefore), After: binlog.Row(projectAfter)},
		{Table: "project_lifecycle", Kind: "update", Before: binlog.Row(lifecycleBefore), After: binlog.Row(lifecycleAfter)},
	}}
	segment, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.ArchiveAt(segment, digest(segment), end)
	if err != nil {
		t.Fatal(err)
	}
	got, err := validateArchiveContents(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if !equalLogRow(binlog.Row(got.Data["projects"][0]), binlog.Row(projectAfter)) || !equalLogRow(binlog.Row(got.Data["project_lifecycle"][0]), binlog.Row(lifecycleAfter)) {
		t.Fatal("metadata/business transaction split or lost")
	}
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("log export failed public validation", err)
	}
	// A complete row transaction with a dangling metadata owner is not a
	// recoverable point and must not advance the saved baseline.
	bad := tx
	bad.Changes = []binlog.Change{{Table: "projects", Kind: "delete", Before: binlog.Row(projectBefore)}}
	before, _ := json.Marshal(r.Point())
	if _, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{bad}); err == nil {
		t.Fatal("orphan retention metadata accepted")
	}
	after, _ := json.Marshal(r.Point())
	if !bytes.Equal(before, after) {
		t.Fatal("failed replay advanced baseline")
	}
}

func TestRetentionLogKeepsSourceJSONBytesAndRejectsDowngrade(t *testing.T) {
	table := LogTable{Columns: []ColumnContract{{Name: "payload", Type: "json"}}}
	raw := []byte(`{"n": 9007199254740993, "x": 1.0, "z": "中文 <>&"}`)
	got, err := normalizeLogRow(table, binlog.Row{raw}, retentionSchemaVersion)
	if err != nil || !bytes.Equal(got[0], raw) {
		t.Fatal("source JSON bytes changed", err)
	}
	got[0][0] = '['
	if raw[0] != '{' {
		t.Fatal("normalization aliased source bytes")
	}
	legacy, err := normalizeLogRow(table, binlog.Row{raw}, supportedSchemaVersion)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(legacy[0], compact.Bytes()) {
		t.Fatal("legacy JSON contract changed", err)
	}
	if _, err = normalizeLogRow(table, binlog.Row{[]byte(`{"broken":`)}, retentionSchemaVersion); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	_, a, b := retentionLogFixture(t)
	if _, err = logSchemaData(a.Data, supportedSchemaVersion); err == nil {
		t.Fatal("retention metadata silently removed on downgrade")
	}
	if _, err = applyLogRows(a.Data, b.Tables, b.Position, b.Position, nil, retentionSchemaVersion); err == nil {
		t.Fatal("retention replay admitted without provenance")
	}
}

func TestRetentionLogBatchUsesObservationUpperBound(t *testing.T) {
	r, a, b := retentionLogFixture(t)
	observed := b.ObservedAt.Add(2 * time.Second)
	row := a.Data["project_lifecycle"][0]
	after := make(Row, len(row))
	for i, cell := range row {
		if cell != nil {
			after[i] = bytes.Clone(cell)
		}
	}
	for _, spec := range retentionTableCatalog() {
		if spec.name == "project_lifecycle" {
			spec.set(after, "updated_at", b.ObservedAt.Add(1500*time.Millisecond).Format("2006-01-02 15:04:05.999999"))
		}
	}
	end := binlog.Position{File: b.Position.File, Offset: 200}
	tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: b.ObservedAt.Add(time.Second).Truncate(time.Second), Changes: []binlog.Change{{Table: "project_lifecycle", Kind: "update", Before: binlog.Row(row), After: binlog.Row(after)}}}
	published := 0
	batch := newLogCaptureBatch(context.Background(), r, nil, observed, func(raw []byte) (LogPoint, error) {
		var segment LogSegment
		if err := strictLogJSON(raw, &segment); err != nil {
			return LogPoint{}, err
		}
		if !segment.ObservedAt.Equal(observed) || segment.Through != end {
			t.Fatal("prefix backdated evidence or claimed a different position")
		}
		published++
		return LogPoint{Position: end, ObservedAt: observed, LastCommitAt: tx.CommittedAt, Hash: digest(raw)}, nil
	})
	batch.limits.transactions = 1
	if err := batch.consume(tx); err != nil {
		t.Fatal(err)
	}
	if published != 1 || r.Point().Position != end {
		t.Fatal("complete prefix not acknowledged")
	}
}
