package binlog

import (
	"math"
	"testing"

	"github.com/go-mysql-org/go-mysql/replication"
)

func rotationDecoder(t *testing.T, offset uint32) *Decoder {
	t.Helper()
	d, err := NewDecoder("studio", []TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, Position{"binlog.000025", offset})
	if err != nil {
		t.Fatal(err)
	}
	d.closedLogEnds, err = verifiedClosedLogEnds([]sourceLogFile{{File: "binlog.000025", Size: 1000}, {File: "binlog.000026", Size: 500}}, Position{"binlog.000025", offset}, Position{"binlog.000026", 300})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func restartRotation() *replication.BinlogEvent {
	e := testEvent(replication.ROTATE_EVENT, 0, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000026")})
	e.Header.Flags = replication.LOG_EVENT_ARTIFICIAL_F
	return e
}
func TestVerifiedClosedEOFRotationKeepsOriginalTransactionBoundary(t *testing.T) {
	d := rotationDecoder(t, 1000)
	for _, event := range []*replication.BinlogEvent{
		restartRotation(),
		testEvent(replication.ROTATE_EVENT, 0, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000026")}),
		testEvent(replication.FORMAT_DESCRIPTION_EVENT, 127, &replication.FormatDescriptionEvent{}),
		testEvent(replication.ANONYMOUS_GTID_EVENT, 180, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000001123456}),
		testEvent(replication.QUERY_EVENT, 210, &replication.QueryEvent{Query: []byte("BEGIN")}),
		testEvent(replication.WRITE_ROWS_EVENTv2, 260, testRows("studio", []any{int64(8), "next chapter"})),
	} {
		if tx, err := d.Consume(event); err != nil || tx != nil {
			t.Fatal("verified restart transition failed", err)
		}
	}
	tx, err := d.Consume(testEvent(replication.XID_EVENT, 300, &replication.XIDEvent{}))
	if err != nil || tx == nil || tx.Start != (Position{"binlog.000025", 1000}) || tx.End != (Position{"binlog.000026", 300}) || len(tx.Changes) != 1 {
		t.Fatal("cross-file recovery boundary lost", tx, err)
	}
}
func TestSyntheticRotationCannotSkipBytesFilesOrFormat(t *testing.T) {
	for _, name := range []string{"missing proof", "before EOF", "different size", "different successor", "missing artificial flag", "timestamped", "skipped file", "wrong offset", "open transaction"} {
		t.Run(name, func(t *testing.T) {
			d := rotationDecoder(t, 1000)
			event := restartRotation()
			switch name {
			case "missing proof":
				d.closedLogEnds = nil
			case "before EOF":
				d.position.Offset = 999
			case "different size":
				d.closedLogEnds["binlog.000025"] = closedLogFile{Size: 1001, Next: "binlog.000026"}
			case "different successor":
				d.closedLogEnds["binlog.000025"] = closedLogFile{Size: 1000, Next: "binlog.000027"}
			case "missing artificial flag":
				event.Header.Flags = 0
			case "timestamped":
				event.Header.Timestamp = 1
			case "skipped file":
				event.Event.(*replication.RotateEvent).NextLogName = []byte("binlog.000027")
			case "wrong offset":
				event.Event.(*replication.RotateEvent).Position = 8
			case "open transaction":
				d.pending = &Transaction{}
			}
			if _, err := d.Consume(event); err == nil {
				t.Fatal("unproven rotation accepted")
			}
			if _, err := d.Consume(testEvent(replication.XID_EVENT, 1500, &replication.XIDEvent{})); err == nil {
				t.Fatal("failed stream resumed")
			}
		})
	}
	d := rotationDecoder(t, 1000)
	if _, err := d.Consume(restartRotation()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Consume(testEvent(replication.ANONYMOUS_GTID_EVENT, 180, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000001123456})); err == nil {
		t.Fatal("transaction accepted before new format")
	}
}
func TestSourceCatalogRejectsMissingAmbiguousAndUnboundedRanges(t *testing.T) {
	start, end := Position{"binlog.000025", 1000}, Position{"binlog.000026", 300}
	for _, files := range [][]sourceLogFile{
		nil, {{File: "binlog.000025", Size: 1000}}, {{File: "binlog.000026", Size: 500}, {File: "binlog.000025", Size: 1000}},
		{{File: "binlog.000025", Size: 999}, {File: "binlog.000026", Size: 500}},
		{{File: "binlog.000025", Size: 1000}, {File: "binlog.000026", Size: 299}},
		{{File: "binlog.000025", Size: math.MaxUint32 + 1}, {File: "binlog.000026", Size: 500}},
		{{File: "binlog.000025", Size: 1000}, {File: "binlog.000025", Size: 1000}, {File: "binlog.000026", Size: 500}},
		{{File: "binlog.000025", Size: 1000, Encrypted: true}, {File: "binlog.000026", Size: 500}},
		{{File: "binlog.000025", Size: 1000}, {File: "binlog.000027", Size: 500}, {File: "binlog.000026", Size: 500}},
	} {
		if _, err := verifiedClosedLogEnds(files, start, end); err == nil {
			t.Fatal("invalid source catalog accepted", files)
		}
	}
}

func TestGracefulStopAtVerifiedEOFDoesNotBecomeATransaction(t *testing.T) {
	d := rotationDecoder(t, 977)
	stop := testEvent(replication.STOP_EVENT, 1000, &replication.GenericEvent{})
	stop.Header.EventSize = 23
	stop.Header.Timestamp = 1750000000
	stop.RawData = make([]byte, 23)
	if tx, err := d.Consume(stop); err != nil || tx != nil || d.position.Offset != 1000 || d.boundary.Offset != 977 {
		t.Fatal("verified metadata-only stop rejected or changed committed boundary", err)
	}
	if _, err := d.Consume(restartRotation()); err != nil {
		t.Fatal("restart after stop rejected", err)
	}
}

func TestShutdownMetadataRequiresEmptyEnvelopeAndClosedEOF(t *testing.T) {
	for _, name := range []string{"missing proof", "wrong EOF", "skipped bytes", "payload", "wrong envelope", "missing bytes", "artificial", "open transaction"} {
		t.Run(name, func(t *testing.T) {
			d := rotationDecoder(t, 977)
			stop := testEvent(replication.STOP_EVENT, 1000, &replication.GenericEvent{})
			stop.Header.EventSize = 23
			stop.Header.Timestamp = 1750000000
			stop.RawData = make([]byte, 23)
			switch name {
			case "missing proof":
				d.closedLogEnds = nil
			case "wrong EOF":
				d.closedLogEnds["binlog.000025"] = closedLogFile{Size: 1001, Next: "binlog.000026"}
			case "skipped bytes":
				d.position.Offset = 976
			case "payload":
				stop.Event.(*replication.GenericEvent).Data = []byte{1}
			case "wrong envelope":
				stop.Header.EventSize = 24
			case "missing bytes":
				stop.RawData = nil
			case "artificial":
				stop.Header.Flags = replication.LOG_EVENT_ARTIFICIAL_F
			case "open transaction":
				d.pending = &Transaction{}
			}
			if _, err := d.Consume(stop); err == nil {
				t.Fatal("unproven source shutdown accepted")
			}
		})
	}
}

func TestStoppedFileCannotProduceMoreTransactionsOrOrdinaryRotations(t *testing.T) {
	for _, name := range []string{"transaction", "ordinary rotate", "format"} {
		t.Run(name, func(t *testing.T) {
			d := rotationDecoder(t, 977)
			stop := testEvent(replication.STOP_EVENT, 1000, &replication.GenericEvent{})
			stop.Header.EventSize = 23
			stop.Header.Timestamp = 1750000000
			stop.RawData = make([]byte, 23)
			if _, err := d.Consume(stop); err != nil {
				t.Fatal(err)
			}
			var event *replication.BinlogEvent
			switch name {
			case "transaction":
				event = testEvent(replication.ANONYMOUS_GTID_EVENT, 1080, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000001123456})
			case "ordinary rotate":
				event = testEvent(replication.ROTATE_EVENT, 1250, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000026")})
			case "format":
				event = testEvent(replication.FORMAT_DESCRIPTION_EVENT, 0, &replication.FormatDescriptionEvent{})
			}
			if _, err := d.Consume(event); err == nil {
				t.Fatal("stopped source accepted an event other than verified rotation")
			}
		})
	}
}

func TestCatalogEOFAlsoBoundsUnstoppedClosedFile(t *testing.T) {
	d := rotationDecoder(t, 999)
	if _, err := d.Consume(testEvent(replication.ANONYMOUS_GTID_EVENT, 1080, &replication.GTIDEvent{ImmediateCommitTimestamp: 1750000001123456})); err == nil {
		t.Fatal("event beyond verified closed EOF accepted")
	}
}

func TestVerifiedCatalogBindsOrdinaryRotationToExactEOF(t *testing.T) {
	d := rotationDecoder(t, 300)
	e := testEvent(replication.ROTATE_EVENT, 350, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000026")})
	if _, err := d.Consume(e); err == nil {
		t.Fatal("ordinary rotation skipped retained bytes before closed EOF")
	}
}

func TestVerifiedPhysicalRotationEndsAtEOFAndRequiresFormat(t *testing.T) {
	d := rotationDecoder(t, 956)
	e := testEvent(replication.ROTATE_EVENT, 1000, &replication.RotateEvent{Position: 4, NextLogName: []byte("binlog.000026")})
	e.Header.EventSize = 44
	e.RawData = make([]byte, 44)
	if _, err := d.Consume(e); err != nil || !d.awaitingFormat {
		t.Fatal("exact physical EOF rotation rejected or omitted format gate", err)
	}
	if _, err := d.Consume(testEvent(replication.FORMAT_DESCRIPTION_EVENT, 127, &replication.FormatDescriptionEvent{})); err != nil {
		t.Fatal(err)
	}
}
