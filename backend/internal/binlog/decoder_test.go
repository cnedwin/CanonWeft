package binlog

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
)

func testEvent(kind replication.EventType, position uint32, event replication.Event) *replication.BinlogEvent {
	return &replication.BinlogEvent{Header: &replication.EventHeader{EventType: kind, LogPos: position}, Event: event}
}

func testRows(database string, rows ...[]any) *replication.RowsEvent {
	return &replication.RowsEvent{
		ColumnCount: 2,
		Table: &replication.TableMapEvent{
			Schema: []byte(database), Table: []byte("documents"), ColumnCount: 2,
			ColumnName: [][]byte{[]byte("id"), []byte("body")},
			PrimaryKey: []uint64{0}, PrimaryKeyPrefix: []uint64{0},
			ColumnType: []byte{mysql.MYSQL_TYPE_LONGLONG, mysql.MYSQL_TYPE_BLOB}, ColumnMeta: []uint16{0, 4},
		},
		Rows: rows,
	}
}

func TestEnumAndSetUseFullMetadataLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   byte
		labels []string
		value  any
		want   string
	}{
		{"enum owner", mysql.MYSQL_TYPE_ENUM, []string{"owner", "editor", "0", ""}, int64(1), "owner"},
		{"enum numeric label", mysql.MYSQL_TYPE_ENUM, []string{"owner", "editor", "0", ""}, int64(3), "0"},
		{"enum explicit empty", mysql.MYSQL_TYPE_ENUM, []string{"owner", "editor", "0", ""}, int64(4), ""},
		{"enum zero sentinel", mysql.MYSQL_TYPE_ENUM, []string{"owner"}, int64(0), ""},
		{"enum null", mysql.MYSQL_TYPE_ENUM, []string{"owner"}, nil, ""},
		{"set order", mysql.MYSQL_TYPE_SET, []string{"write", "read", "2"}, int64(5), "write,2"},
		{"set empty", mysql.MYSQL_TYPE_SET, []string{"write"}, int64(0), ""},
		{"set null", mysql.MYSQL_TYPE_SET, []string{"write"}, nil, ""},
		{"set bit64", mysql.MYSQL_TYPE_SET, append(make([]string, 63), "last"), int64(math.MinInt64), "last"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startedDecoder(t)
			r := testRows("studio", []any{int64(7), tc.value}, []any{int64(7), tc.value})
			r.Table.ColumnType[1] = mysql.MYSQL_TYPE_STRING
			r.Table.ColumnMeta[1] = uint16(tc.kind)<<8 | 1
			labels := make([][]byte, len(tc.labels))
			for i, label := range tc.labels {
				labels[i] = []byte(label)
			}
			if tc.kind == mysql.MYSQL_TYPE_ENUM {
				r.Table.EnumStrValue = [][][]byte{labels}
			} else {
				r.Table.SetStrValue = [][][]byte{labels}
			}
			if _, err := d.Consume(testEvent(replication.UPDATE_ROWS_EVENTv2, 200, r)); err != nil {
				t.Fatal(err)
			}
			tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{}))
			if err != nil || tx == nil || len(tx.Changes) != 1 {
				t.Fatal("missing enum/set transaction", err)
			}
			for _, row := range []Row{tx.Changes[0].Before, tx.Changes[0].After} {
				if string(row[1]) != tc.want || (row[1] == nil) != (tc.value == nil) {
					t.Fatalf("enum/set label changed: got %q want %q", row[1], tc.want)
				}
			}
		})
	}
}

func TestInvalidEnumSetMetadataOrOrdinalPoisonsTransaction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*replication.RowsEvent)
	}{
		{"missing labels", func(r *replication.RowsEvent) { r.Table.EnumStrValue = nil }},
		{"missing labels even null", func(r *replication.RowsEvent) { r.Table.EnumStrValue = nil; r.Rows[0][1] = nil }},
		{"extra label group", func(r *replication.RowsEvent) {
			r.Table.EnumStrValue = append(r.Table.EnumStrValue, [][]byte{[]byte("extra")})
		}},
		{"enum out of range", func(r *replication.RowsEvent) { r.Rows[0][1] = int64(2) }},
		{"enum negative", func(r *replication.RowsEvent) { r.Rows[0][1] = int64(-1) }},
		{"enum text masquerading as ordinal", func(r *replication.RowsEvent) { r.Rows[0][1] = "1" }},
		{"set out of range", func(r *replication.RowsEvent) {
			r.Table.ColumnMeta[1] = uint16(mysql.MYSQL_TYPE_SET)<<8 | 1
			r.Table.EnumStrValue = nil
			r.Table.SetStrValue = [][][]byte{{[]byte("one")}}
			r.Rows[0][1] = int64(2)
		}},
		{"missing type metadata", func(r *replication.RowsEvent) { r.Table.ColumnType = nil }},
		{"missing packed metadata", func(r *replication.RowsEvent) { r.Table.ColumnMeta = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startedDecoder(t)
			r := testRows("studio", []any{int64(7), int64(1)})
			r.Table.ColumnType[1] = mysql.MYSQL_TYPE_STRING
			r.Table.ColumnMeta[1] = uint16(mysql.MYSQL_TYPE_ENUM)<<8 | 1
			r.Table.EnumStrValue = [][][]byte{{[]byte("owner")}}
			tc.alter(r)
			if tx, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, r)); err == nil || tx != nil {
				t.Fatal("unsafe enum/set accepted")
			}
			if tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{})); err == nil || tx != nil {
				t.Fatal("invalid enum/set transaction committed")
			}
		})
	}
}

func TestRepeatedEnumLabelsCannotBypassDecodedCapacity(t *testing.T) {
	d := startedDecoder(t)
	r := testRows("studio")
	r.Table.ColumnType[1] = mysql.MYSQL_TYPE_STRING
	r.Table.ColumnMeta[1] = uint16(mysql.MYSQL_TYPE_ENUM)<<8 | 1
	r.Table.EnumStrValue = [][][]byte{{[]byte(strings.Repeat("x", 1<<20))}}
	for i := 0; i < 17; i++ {
		r.Rows = append(r.Rows, []any{int64(i), int64(1)})
	}
	if _, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, r)); err == nil {
		t.Fatal("small wire ordinals expanded beyond the decoded event budget")
	}
}

// Omitting the XID boundary or leaking another database must break this test.
// Expected content and timestamp are independently specified literals.
func TestOnlyCompleteSourceTransactionsBecomeRecoveryRecords(t *testing.T) {
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000001", 100})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("first chapter")
	for _, event := range []*replication.BinlogEvent{
		testEvent(replication.ANONYMOUS_GTID_EVENT, 130, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000000123456}),
		testEvent(replication.QUERY_EVENT, 150, &replication.QueryEvent{Schema: []byte("studio"), Query: []byte("BEGIN")}),
		testEvent(replication.WRITE_ROWS_EVENTv2, 230, testRows("studio", []any{int64(7), body})),
		testEvent(replication.WRITE_ROWS_EVENTv2, 270, testRows("unrelated", []any{int64(8), "private unrelated data"})),
	} {
		tx, consumeErr := d.Consume(event)
		if consumeErr != nil {
			t.Fatal(consumeErr)
		}
		if tx != nil {
			t.Fatal("uncommitted rows were published as a recovery point")
		}
	}
	body[0] = 'X'
	tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{XID: 91}))
	if err != nil {
		t.Fatal(err)
	}
	if tx == nil || len(tx.Changes) != 1 {
		t.Fatalf("expected one committed source change, got %+v", tx)
	}
	if tx.Start != (Position{"binlog.000001", 100}) || tx.End != (Position{"binlog.000001", 300}) {
		t.Fatal("incorrect recovery boundaries", tx.Start, tx.End)
	}
	if !tx.CommittedAt.Equal(time.UnixMicro(1750000000123456).UTC()) {
		t.Fatal("commit time did not use the actual commit timestamp")
	}
	change := tx.Changes[0]
	if change.Table != "documents" || change.Kind != "insert" || change.Before != nil || len(change.After) != 2 || !bytes.Equal(change.After[0], []byte("7")) || !bytes.Equal(change.After[1], []byte("first chapter")) {
		t.Fatalf("committed source row was lost, mutated or confused with unrelated data: %+v", change)
	}
}

func startedDecoder(t *testing.T) *Decoder {
	t.Helper()
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000001", 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []*replication.BinlogEvent{
		testEvent(replication.ANONYMOUS_GTID_EVENT, 130, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000000123456}),
		testEvent(replication.QUERY_EVENT, 150, &replication.QueryEvent{Query: []byte("BEGIN")}),
	} {
		if _, err = d.Consume(e); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// A plausible column count is not enough: swapped columns or partial images
// would silently corrupt a restored chapter. Errors poison this stream instance.
func TestIncompleteOrChangedMetadataCannotProduceARecoveryPoint(t *testing.T) {
	cases := []struct {
		name   string
		change func(*replication.RowsEvent)
	}{
		{"missing names", func(r *replication.RowsEvent) { r.Table.ColumnName = nil }},
		{"swapped names", func(r *replication.RowsEvent) { r.Table.ColumnName = [][]byte{[]byte("body"), []byte("id")} }},
		{"missing key", func(r *replication.RowsEvent) { r.Table.PrimaryKey = nil }},
		{"changed key", func(r *replication.RowsEvent) { r.Table.PrimaryKey = []uint64{1} }},
		{"prefix key", func(r *replication.RowsEvent) { r.Table.PrimaryKeyPrefix = []uint64{3} }},
		{"partial image", func(r *replication.RowsEvent) { r.SkippedColumns = [][]int{{1}} }},
		{"missing cell", func(r *replication.RowsEvent) { r.Rows[0] = []any{int64(7)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := startedDecoder(t)
			r := testRows("studio", []any{int64(7), "chapter"})
			tc.change(r)
			if tx, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, r)); err == nil || tx != nil {
				t.Fatal("unsafe row metadata accepted")
			}
			if tx, err := d.Consume(testEvent(replication.XID_EVENT, 240, &replication.XIDEvent{XID: 92})); err == nil || tx != nil {
				t.Fatal("failed stream subsequently emitted a partial recovery point")
			}
		})
	}
}

func TestUpdateDeleteAndNullImagesRemainDistinct(t *testing.T) {
	d := startedDecoder(t)
	for _, e := range []*replication.BinlogEvent{
		testEvent(replication.UPDATE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), nil}, []any{int64(9), []byte{}})),
		testEvent(replication.DELETE_ROWS_EVENTv2, 250, testRows("studio", []any{int64(9), []byte{}})),
	} {
		if tx, err := d.Consume(e); err != nil || tx != nil {
			t.Fatal("row operation failed", err)
		}
	}
	tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{XID: 93}))
	if err != nil || tx == nil || len(tx.Changes) != 2 {
		t.Fatal("missing committed changes", tx, err)
	}
	update, deleted := tx.Changes[0], tx.Changes[1]
	if update.Kind != "update" || string(update.Before[0]) != "7" || string(update.After[0]) != "9" || update.Before[1] != nil || update.After[1] == nil || len(update.After[1]) != 0 {
		t.Fatal("key mutation or NULL/empty distinction lost", update)
	}
	if deleted.Kind != "delete" || string(deleted.Before[0]) != "9" || deleted.Before[1] == nil || deleted.After != nil {
		t.Fatal("delete image corrupted", deleted)
	}
}

func TestInvalidBaselineContractIsRejected(t *testing.T) {
	valid := TableSchema{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}
	for _, tc := range []struct {
		name     string
		database string
		tables   []TableSchema
		start    Position
	}{
		{"no database", "", []TableSchema{valid}, Position{"binlog.000001", 100}},
		{"no tables", "studio", nil, Position{"binlog.000001", 100}},
		{"duplicate table", "studio", []TableSchema{valid, valid}, Position{"binlog.000001", 100}},
		{"duplicate column", "studio", []TableSchema{{Name: "documents", Columns: []string{"id", "id"}, PrimaryKey: []int{0}}}, Position{"binlog.000001", 100}},
		{"missing key", "studio", []TableSchema{{Name: "documents", Columns: []string{"id"}}}, Position{"binlog.000001", 100}},
		{"invalid key", "studio", []TableSchema{{Name: "documents", Columns: []string{"id"}, PrimaryKey: []int{1}}}, Position{"binlog.000001", 100}},
		{"file traversal", "studio", []TableSchema{valid}, Position{"../binlog.000001", 100}},
		{"invalid offset", "studio", []TableSchema{valid}, Position{"binlog.000001", 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDecoder(tc.database, tc.tables, tc.start); err == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}

func TestBaselineContractCannotChangeThroughCallerSlices(t *testing.T) {
	table := TableSchema{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}
	d, err := NewDecoder("studio", []TableSchema{table}, Position{"binlog.000001", 100})
	if err != nil {
		t.Fatal(err)
	}
	table.Columns[1] = "secret"
	table.PrimaryKey[0] = 1
	for _, e := range []*replication.BinlogEvent{
		testEvent(replication.ANONYMOUS_GTID_EVENT, 130, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000000123456}),
		testEvent(replication.QUERY_EVENT, 150, &replication.QueryEvent{Query: []byte("BEGIN")}),
		testEvent(replication.WRITE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), "chapter"})),
	} {
		if _, err = d.Consume(e); err != nil {
			t.Fatal("frozen source schema was externally changed", err)
		}
	}
}

func TestMySQLScalarWidthsRetainExactRowValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"tiny signed", int8(-8), "-8"}, {"small signed", int16(-32768), "-32768"},
		{"long signed", int32(-2147483648), "-2147483648"}, {"year", int(2026), "2026"},
		{"unsigned tiny", uint8(255), "255"}, {"unsigned short", uint16(65535), "65535"},
		{"unsigned long", uint32(4294967295), "4294967295"},
		{"unsigned longlong", uint64(math.MaxUint64), "18446744073709551615"},
		{"float", float32(1.5), "1.5"}, {"double", float64(-1.25), "-1.25"},
		{"decimal text", "12345678901234567890.001", "12345678901234567890.001"},
		{"datetime text", "2026-10-25 04:00:00.123456", "2026-10-25 04:00:00.123456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startedDecoder(t)
			if _, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), tc.value}))); err != nil {
				t.Fatal(err)
			}
			tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{}))
			if err != nil || tx == nil || len(tx.Changes) != 1 || string(tx.Changes[0].After[1]) != tc.want {
				t.Fatal("SQL value fidelity lost", tx, err)
			}
		})
	}
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1)} {
		d := startedDecoder(t)
		if _, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), value}))); err == nil {
			t.Fatal("nonfinite SQL value accepted")
		}
	}
}

func TestBackwardOrMissingCursorPoisonsStream(t *testing.T) {
	for _, position := range []uint32{0, 100, 150} {
		d := startedDecoder(t)
		if _, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, position, testRows("studio", []any{int64(7), "chapter"}))); err == nil {
			t.Fatal("nonadvancing cursor accepted", position)
		}
		if tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{})); err == nil || tx != nil {
			t.Fatal("broken cursor produced a recovery point")
		}
	}
}

func TestLogRotationKeepsRecoveryChainContinuous(t *testing.T) {
	d := startedDecoder(t)
	if _, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{})); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*replication.BinlogEvent{
		testEvent(replication.ROTATE_EVENT, 350, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000002")}),
		testEvent(replication.ROTATE_EVENT, 0, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000002")}),
		testEvent(replication.FORMAT_DESCRIPTION_EVENT, 125, &replication.FormatDescriptionEvent{}),
		testEvent(replication.ANONYMOUS_GTID_EVENT, 150, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000001123456}),
		testEvent(replication.QUERY_EVENT, 180, &replication.QueryEvent{Query: []byte("BEGIN")}),
		testEvent(replication.WRITE_ROWS_EVENTv2, 230, testRows("studio", []any{int64(8), "next chapter"})),
	} {
		if tx, err := d.Consume(e); err != nil || tx != nil {
			t.Fatal("rotation failed", tx, err)
		}
	}
	tx, err := d.Consume(testEvent(replication.XID_EVENT, 270, &replication.XIDEvent{}))
	if err != nil || tx == nil || tx.Start != (Position{"binlog.000001", 300}) || tx.End != (Position{"binlog.000002", 270}) {
		t.Fatal("recovery chain lost across rotation", tx, err)
	}
}

func TestReconnectMetadataDoesNotRewindDurableCursor(t *testing.T) {
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000003", 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []*replication.BinlogEvent{
		testEvent(replication.ROTATE_EVENT, 0, &replication.RotateEvent{Position: 1000, NextLogName: []byte("binlog.000003")}),
		testEvent(replication.FORMAT_DESCRIPTION_EVENT, 0, &replication.FormatDescriptionEvent{}),
		testEvent(replication.FORMAT_DESCRIPTION_EVENT, 125, &replication.FormatDescriptionEvent{}),
		testEvent(replication.ANONYMOUS_GTID_EVENT, 1100, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000002123456}),
		testEvent(replication.QUERY_EVENT, 1200, &replication.QueryEvent{Query: []byte("BEGIN")}),
	} {
		if _, err = d.Consume(e); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := d.Consume(testEvent(replication.XID_EVENT, 1500, &replication.XIDEvent{}))
	if err != nil || tx == nil || tx.Start != (Position{"binlog.000003", 1000}) || tx.End.Offset != 1500 {
		t.Fatal("metadata rewound recovered cursor", tx, err)
	}
}

func TestBeginAndCommitMustBelongToOneTransaction(t *testing.T) {
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000001", 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Consume(testEvent(replication.ANONYMOUS_GTID_EVENT, 130, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000000123456})); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), "chapter"}))); err == nil {
		t.Fatal("rows outside BEGIN accepted")
	}
	d = startedDecoder(t)
	if _, err = d.Consume(testEvent(replication.QUERY_EVENT, 180, &replication.QueryEvent{Query: []byte("BEGIN")})); err == nil {
		t.Fatal("nested BEGIN accepted")
	}
}

func TestOversizedUncommittedTransactionCannotGrowWithoutLimit(t *testing.T) {
	d := startedDecoder(t)
	large := strings.Repeat("x", 8*1024*1024)
	rejected := false
	for i := uint32(0); i < 9; i++ {
		if _, err := d.Consume(testEvent(replication.WRITE_ROWS_EVENTv2, 200+i*100, testRows("studio", []any{int64(i), large}))); err != nil {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("more than 64 MiB of uncommitted data accumulated without a bound")
	}
	if tx, err := d.Consume(testEvent(replication.XID_EVENT, 2000, &replication.XIDEvent{})); err == nil || tx != nil {
		t.Fatal("capacity failure emitted a partial transaction")
	}
}

func TestOversizedWireEventAndInvalidCommitTimeAreRejected(t *testing.T) {
	d := startedDecoder(t)
	e := testEvent(replication.WRITE_ROWS_EVENTv2, 200, testRows("studio", []any{int64(7), "small body"}))
	e.RawData = make([]byte, 17*1024*1024)
	if _, err := d.Consume(e); err == nil {
		t.Fatal("oversized raw event accepted")
	}
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000001", 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Consume(testEvent(replication.ANONYMOUS_GTID_EVENT, 130, &replication.GTIDEvent{ImmediateCommitTimestamp: math.MaxUint64})); err == nil {
		t.Fatal("overflowed commit timestamp accepted")
	}
}
