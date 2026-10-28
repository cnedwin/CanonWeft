package binlog_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"novelstudio/internal/binlog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func cursor(t *testing.T, db *sql.DB) binlog.Position {
	t.Helper()
	var p binlog.Position
	var included, excluded, gtids string
	if err := db.QueryRow("SHOW BINARY LOG STATUS").Scan(&p.File, &p.Offset, &included, &excluded, &gtids); err != nil {
		t.Fatal("source coordinate unavailable", err)
	}
	return p
}

func TestActualMySQLCommittedRangeExcludesRollbackAndStopsAtBoundary(t *testing.T) {
	dsn := os.Getenv("STUDIO_BINLOG_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("required real MySQL log suite selects a dedicated local admin connection")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test connection")
	}
	if config.Net != "tcp" || config.Addr != "127.0.0.1:13306" || config.DBName != "" {
		t.Fatal("log test requires the isolated local runtime admin endpoint")
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("admin connection unavailable")
	}
	defer admin.Close()
	// The task's runtime owner coordinated this metadata-only configuration;
	// no source application data is changed by this setting.
	if _, err = admin.Exec("SET GLOBAL binlog_row_metadata='FULL'"); err != nil {
		t.Fatal("full log metadata unavailable", err)
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	name := "studio_binlog_" + hex.EncodeToString(id[:])
	if _, err = admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + name)
	config.DBName = name
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE documents(id BIGINT UNSIGNED PRIMARY KEY,body LONGBLOB NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE fidelity(id BIGINT UNSIGNED PRIMARY KEY,payload JSON,amount DECIMAL(65,30),moment DATETIME(6),elapsed TIME(6),binary_value LONGBLOB,nullable_value LONGBLOB,empty_value LONGBLOB) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE semantics(id BIGINT PRIMARY KEY,role ENUM('owner','editor','0','') NULL,flags SET('write','read','2') NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	start := cursor(t, admin)
	if _, err = db.Exec("INSERT INTO documents VALUES(7,?)", []byte("first chapter")); err != nil {
		t.Fatal(err)
	}
	aborted, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = aborted.Exec("INSERT INTO documents VALUES(8,?)", []byte("rolled back chapter")); err != nil {
		aborted.Rollback()
		t.Fatal(err)
	}
	if err = aborted.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE documents SET body=? WHERE id=7", []byte("revised chapter")); err != nil {
		t.Fatal(err)
	}
	end := cursor(t, admin)
	if _, err = db.Exec("DELETE FROM documents WHERE id=7"); err != nil {
		t.Fatal(err)
	}
	var sourceUUID string
	if err = admin.QueryRow("SELECT @@server_uuid").Scan(&sourceUUID); err != nil {
		t.Fatal(err)
	}
	reader, err := binlog.NewReader(dsn, sourceUUID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var changes []binlog.Change
	err = reader.ReadRange(ctx, name, []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, start, end, func(tx binlog.Transaction) error { changes = append(changes, tx.Changes...); return nil })
	if err != nil {
		t.Fatal("real committed range could not be collected", err)
	}
	if len(changes) != 2 || changes[0].Kind != "insert" || string(changes[0].After[1]) != "first chapter" || changes[1].Kind != "update" || string(changes[1].After[1]) != "revised chapter" {
		t.Fatal("log range included rollback/late deletion or lost committed changes", changes)
	}
	t.Run("another source cannot validate even an empty range", func(t *testing.T) {
		wrong, err := binlog.NewReader(dsn, "00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatal(err)
		}
		err = wrong.ReadRange(ctx, name, []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, end, end, func(binlog.Transaction) error { t.Fatal("unexpected change from wrong source"); return nil })
		if err == nil {
			t.Fatal("backup source identity was not checked")
		}
	})
	t.Run("empty coverage must be the observed current source boundary", func(t *testing.T) {
		// Other independent suites may commit concurrently. Freeze only this
		// short observation; release the physical lock even on test failure.
		lockContext, unlockContext := context.WithTimeout(ctx, 3*time.Second)
		defer unlockContext()
		locked, err := admin.Conn(lockContext)
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		defer locked.Raw(func(any) error { return driver.ErrBadConn })
		if _, err := locked.ExecContext(lockContext, "FLUSH TABLES WITH READ LOCK"); err != nil {
			t.Fatal("empty-boundary test lock unavailable", err)
		}
		defer locked.ExecContext(lockContext, "UNLOCK TABLES")
		current := cursor(t, admin)
		contract := []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}
		consume := func(binlog.Transaction) error { t.Fatal("empty coverage emitted changes"); return nil }
		if err := reader.ReadRange(lockContext, name, contract, current, current, consume); err != nil {
			t.Fatal("current empty boundary unavailable", err)
		}
		for _, invalid := range []binlog.Position{
			{File: current.File[:strings.LastIndexByte(current.File, '.')] + ".999999", Offset: 4},
			{File: current.File, Offset: current.Offset + 1},
			{File: start.File, Offset: start.Offset + 1},
			start, // Historical empty coverage needs separate evidence, not a guessed cursor.
		} {
			if err := reader.ReadRange(lockContext, name, contract, invalid, invalid, consume); err == nil {
				t.Error("unobserved empty coordinate reported as protected")
			}
		}
	})
	t.Run("actual rotation retains both sides", func(t *testing.T) {
		from := cursor(t, admin)
		if _, err = db.Exec("INSERT INTO documents VALUES(9,?)", []byte("before rotation")); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec("FLUSH BINARY LOGS"); err != nil {
			t.Fatal("log rotation unavailable", err)
		}
		if _, err = db.Exec("UPDATE documents SET body=? WHERE id=9", []byte("after rotation")); err != nil {
			t.Fatal(err)
		}
		through := cursor(t, admin)
		if through.File == from.File {
			t.Fatal("actual source did not rotate")
		}
		var observed []binlog.Change
		err = reader.ReadRange(ctx, name, []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, from, through, func(tx binlog.Transaction) error { observed = append(observed, tx.Changes...); return nil })
		if err != nil {
			t.Fatal("actual rotated range unavailable", err)
		}
		if len(observed) != 2 || string(observed[0].After[1]) != "before rotation" || string(observed[1].After[1]) != "after rotation" {
			t.Fatal("rotation lost committed content", observed)
		}
	})
	t.Run("non-event boundary cannot emit partial data", func(t *testing.T) {
		partial := start
		partial.Offset++
		called := false
		err = reader.ReadRange(ctx, name, []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, start, partial, func(binlog.Transaction) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("partial event became a recovery point", err)
		}
	})
	t.Run("unavailable log is reported without retry or credential disclosure", func(t *testing.T) {
		missing := binlog.Position{File: start.File[:strings.LastIndexByte(start.File, '.')] + ".999999", Offset: 4}
		through := missing
		through.Offset = 100
		missingContext, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err = reader.ReadRange(missingContext, name, []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, missing, through, func(binlog.Transaction) error { t.Fatal("unavailable log emitted changes"); return nil })
		if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), config.Passwd) {
			t.Fatal("missing log did not fail safely and promptly")
		}
	})
	t.Run("wire enum set images match SQL labels including null empty and numeric labels", func(t *testing.T) {
		from := cursor(t, admin)
		if _, err := db.Exec("INSERT INTO semantics VALUES(1,'owner','read,write'),(2,'0','2'),(3,'',''),(4,NULL,NULL)"); err != nil {
			t.Fatal(err)
		}
		read := func() map[string]binlog.Row {
			rows, err := db.Query("SELECT id,role,flags FROM semantics ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[string]binlog.Row{}
			for rows.Next() {
				row := make(binlog.Row, 3)
				if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
					t.Fatal(err)
				}
				out[string(row[0])] = row
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		before := read()
		if _, err := db.Exec("UPDATE semantics SET role='editor',flags='2,read' WHERE id=1"); err != nil {
			t.Fatal(err)
		}
		after := read()
		if _, err := db.Exec("DELETE FROM semantics WHERE id=3"); err != nil {
			t.Fatal(err)
		}
		through := cursor(t, admin)
		var actual []binlog.Change
		err := reader.ReadRange(ctx, name, []binlog.TableSchema{{Name: "semantics", Columns: []string{"id", "role", "flags"}, PrimaryKey: []int{0}}}, from, through, func(tx binlog.Transaction) error { actual = append(actual, tx.Changes...); return nil })
		if err != nil || len(actual) != 6 {
			t.Fatal("enum/set wire range unavailable", err, len(actual))
		}
		assertRow := func(got, want binlog.Row) {
			t.Helper()
			if len(got) != len(want) {
				t.Fatal("enum/set image width mismatch")
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) || (got[i] == nil) != (want[i] == nil) {
					t.Fatalf("enum/set SQL image differs at column %d: got %q want %q", i, got[i], want[i])
				}
			}
		}
		for i, id := range []string{"1", "2", "3", "4"} {
			assertRow(actual[i].After, before[id])
		}
		assertRow(actual[4].Before, before["1"])
		assertRow(actual[4].After, after["1"])
		assertRow(actual[5].Before, before["3"])
	})
	t.Run("wire values preserve unsigned decimal JSON time and binary fidelity", func(t *testing.T) {
		from := cursor(t, admin)
		binaryValue := []byte{0, 255, 128, 10, 0, 39, 92}
		if _, err := db.Exec("INSERT INTO fidelity VALUES(18446744073709551615,?,?,'2026-10-25 12:34:56.123456','-120:30:45.123456',?,NULL,?)", `{"big":18446744073709551615,"nested":[1.125,true,null,"小说"]}`, "12345678901234567890123456789012345.123456789012345678901234567890", binaryValue, []byte{}); err != nil {
			t.Fatal(err)
		}
		through := cursor(t, admin)
		columns := []string{"id", "payload", "amount", "moment", "elapsed", "binary_value", "nullable_value", "empty_value"}
		// DATETIME is a SQL wall-clock value. Avoid the test DSN's parseTime
		// conversion to RFC3339; snapshots also explicitly retain SQL fields.
		rows, err := db.Query("SELECT id,payload,amount,CAST(moment AS CHAR),elapsed,binary_value,nullable_value,empty_value FROM fidelity")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		expected := make([][]byte, len(columns))
		dest := make([]any, len(columns))
		for i := range expected {
			dest[i] = &expected[i]
		}
		if !rows.Next() {
			t.Fatal("wire fixture absent")
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		var actual []binlog.Change
		err = reader.ReadRange(ctx, name, []binlog.TableSchema{{Name: "fidelity", Columns: columns, PrimaryKey: []int{0}}}, from, through, func(tx binlog.Transaction) error { actual = append(actual, tx.Changes...); return nil })
		if err != nil || len(actual) != 1 {
			t.Fatal("wire fidelity read failed", err)
		}
		for i, expectedCell := range expected {
			cell := actual[0].After[i]
			if columns[i] == "payload" {
				// Whitespace is not JSON data; compact without unmarshalling
				// into float64, preserving every numeric digit and JSON type.
				var actualJSON, expectedJSON bytes.Buffer
				if json.Compact(&actualJSON, cell) != nil || json.Compact(&expectedJSON, expectedCell) != nil {
					t.Fatal("invalid wire JSON")
				}
				cell, expectedCell = actualJSON.Bytes(), expectedJSON.Bytes()
			}
			if !bytes.Equal(cell, expectedCell) || (cell == nil) != (expectedCell == nil) {
				t.Errorf("wire column %s changed representation: got %q want %q", columns[i], cell, expectedCell)
			}
		}
	})
}
