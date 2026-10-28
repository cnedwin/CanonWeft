package backup

import (
	"bytes"
	"novelstudio/internal/binlog"
	"strings"
	"testing"
	"time"
)

func rowContracts() []LogTable {
	var result []LogTable
	for _, spec := range logTableCatalog() {
		table := LogTable{Layout: binlog.TableSchema{Name: spec.name, Columns: spec.cols(), PrimaryKey: []int{0}}}
		for _, name := range spec.cols() {
			table.Columns = append(table.Columns, ColumnContract{Name: name, Type: "varchar", SQLType: "varchar(255)", Charset: "utf8mb4", Collation: "utf8mb4_bin"})
		}
		result = append(result, table)
	}
	return result
}

func logRow(values ...string) binlog.Row {
	row := make(binlog.Row, len(values))
	for i, value := range values {
		row[i] = []byte(value)
	}
	return row
}

func emptyLogData() Data {
	result := Data{}
	for _, spec := range tableCatalog {
		result[spec.name] = nil
	}
	return result
}

func TestLogTransactionSanitizesBeforeDurableSerialization(t *testing.T) {
	user, workspace, connection := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	tx := binlog.Transaction{Start: binlog.Position{File: "binlog.000001", Offset: 100}, End: binlog.Position{File: "binlog.000001", Offset: 200}, CommittedAt: time.Now().UTC(), Changes: []binlog.Change{
		{Table: "users", Kind: "insert", After: logRow(user, workspace, "owner", "PASSWORD_MARKER", "owner", "1", "2026-10-25 12:00:00")},
		{Table: "sessions", Kind: "insert", After: logRow("SESSION_MARKER", user, "2026-10-26 12:00:00")},
		{Table: "model_connection_versions", Kind: "insert", After: logRow(connection, "1", "{}", "CREDENTIAL_MARKER", "2026-10-25 12:00:00")},
	}}
	clean, err := prepareLogTransaction(rowContracts(), tx)
	if err != nil || len(clean.Changes) != 2 {
		t.Fatal("safe transaction preparation failed", err)
	}
	if len(clean.Changes[0].After[3]) != 0 || string(clean.Changes[0].After[5]) != "0" || len(clean.Changes[1].After[3]) != 0 {
		t.Fatal("secret or enabled authority remained in journal row")
	}
	if string(tx.Changes[0].After[3]) != "PASSWORD_MARKER" || string(tx.Changes[1].After[0]) != "SESSION_MARKER" {
		t.Fatal("preparing journal mutated source memory")
	}
	clean.Changes[0].After[2][0] = 'X'
	if string(tx.Changes[0].After[2]) != "owner" {
		t.Fatal("journal and source row still share memory")
	}
}

func TestLogReplayIsAtomicAndRejectsDuplicateGapAndBeforeImageMismatch(t *testing.T) {
	contracts := rowContracts()
	w, p := strings.Repeat("a", 32), strings.Repeat("b", 32)
	data := emptyLogData()
	data["workspaces"] = []Row{Row(logRow(w, "before", "2026-10-25 12:00:00"))}
	start := binlog.Position{File: "binlog.000001", Offset: 100}
	middle := binlog.Position{File: "binlog.000001", Offset: 200}
	end := binlog.Position{File: "binlog.000001", Offset: 300}
	project := logRow(p, w, "novel", "1", "2026-10-25 12:00:00", "")
	project[5] = nil
	first := binlog.Transaction{Start: start, End: middle, CommittedAt: time.Now().UTC(), Changes: []binlog.Change{
		{Table: "workspaces", Kind: "update", Before: binlog.Row(data["workspaces"][0]), After: logRow(w, "after", "2026-10-25 12:00:00")},
		{Table: "projects", Kind: "insert", After: project},
	}}
	second := binlog.Transaction{Start: middle, End: end, CommittedAt: first.CommittedAt, Changes: []binlog.Change{{Table: "projects", Kind: "delete", Before: project}}}
	replayed, err := applyLogRows(data, contracts, start, middle, []binlog.Transaction{first})
	if err != nil || len(replayed["projects"]) != 1 || string(replayed["workspaces"][0][1]) != "after" {
		t.Fatal("complete middle commit was not replayed", err)
	}
	if string(data["workspaces"][0][1]) != "before" || len(data["projects"]) != 0 {
		t.Fatal("replay mutated its baseline")
	}
	final, err := applyLogRows(replayed, contracts, middle, end, []binlog.Transaction{second})
	if err != nil || len(final["projects"]) != 0 {
		t.Fatal("delete was not replayed", err)
	}
	if _, err := applyLogRows(replayed, contracts, middle, end, []binlog.Transaction{first}); err == nil {
		t.Fatal("duplicate/out-of-order transaction accepted")
	}
	bad := first
	bad.End = end
	bad.Changes = append([]binlog.Change{}, first.Changes...)
	bad.Changes = append(bad.Changes, binlog.Change{Table: "projects", Kind: "delete", Before: logRow(p, w, "wrong before image", "1", "2026-10-25 12:00:00", "")})
	if _, err := applyLogRows(data, contracts, start, end, []binlog.Transaction{bad}); err == nil {
		t.Fatal("wrong before image accepted")
	}
	if !bytes.Equal(data["workspaces"][0][1], []byte("before")) || len(data["projects"]) != 0 {
		t.Fatal("failed transaction partially changed baseline")
	}
	gap := first
	gap.Start.Offset++
	if _, err := applyLogRows(data, contracts, start, middle, []binlog.Transaction{gap}); err == nil {
		t.Fatal("source gap accepted")
	}
}

func TestLogReplayPreservesKeyChangesNullBinaryAndExactJSON(t *testing.T) {
	contracts := rowContracts()
	oldID, newID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	data := emptyLogData()
	data["workspaces"] = []Row{Row(logRow(oldID, "old", "2026-10-25 12:00:00"))}
	start, end := binlog.Position{File: "binlog.000001", Offset: 100}, binlog.Position{File: "binlog.000001", Offset: 200}
	tx := binlog.Transaction{Start: start, End: end, CommittedAt: time.Now().UTC(), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: binlog.Row(data["workspaces"][0]), After: logRow(newID, "new", "2026-10-25 12:00:00")}}}
	got, err := applyLogRows(data, contracts, start, end, []binlog.Transaction{tx})
	if err != nil || len(got["workspaces"]) != 1 || string(got["workspaces"][0][0]) != newID {
		t.Fatal("primary key change lost", err)
	}
	data["workspaces"] = append(data["workspaces"], Row(logRow(newID, "existing", "2026-10-25 12:00:00")))
	if _, err := applyLogRows(data, contracts, start, end, []binlog.Transaction{tx}); err == nil {
		t.Fatal("primary key collision accepted")
	}
	table := LogTable{Columns: []ColumnContract{{Type: "json"}, {Type: "blob"}, {Type: "blob"}, {Type: "blob"}, {Type: "datetime"}}}
	row := binlog.Row{[]byte(`{ "number":18446744073709551615, "decimal":0.1234567890123456789 }`), nil, {}, {0, 255, 128, 0}, []byte("2026-10-25 12:00:00.120000")}
	normalized, err := normalizeLogRow(table, row)
	if err != nil || string(normalized[0]) != `{"number":18446744073709551615,"decimal":0.1234567890123456789}` || normalized[1] != nil || normalized[2] == nil || !bytes.Equal(normalized[3], row[3]) || string(normalized[4]) != "2026-10-25 12:00:00.12" {
		t.Fatal("canonical row changed actual content", err)
	}
}

func TestLogReplayRejectsUntrustedAuthorityAndSchemaChanges(t *testing.T) {
	contracts := rowContracts()
	start, end := binlog.Position{File: "binlog.000001", Offset: 100}, binlog.Position{File: "binlog.000001", Offset: 200}
	for _, change := range []binlog.Change{
		{Table: "schema_version", Kind: "update", Before: logRow("1", "29", "checksum", "1"), After: logRow("1", "30", "checksum", "1")},
		{Table: "missing_table", Kind: "insert", After: logRow("sensitive")},
		{Table: "users", Kind: "insert", After: logRow(strings.Repeat("a", 32), strings.Repeat("b", 32), "owner", "secret", "owner", "1", "2026-10-25 12:00:00")},
		{Table: "sessions", Kind: "insert", After: logRow("token", strings.Repeat("a", 32), "2026-10-25 12:00:00")},
		{Table: "workspaces", Kind: "insert", After: logRow(strings.Repeat("a", 32), "partial")},
	} {
		tx := binlog.Transaction{Start: start, End: end, CommittedAt: time.Now().UTC(), Changes: []binlog.Change{change}}
		if _, err := applyLogRows(emptyLogData(), contracts, start, end, []binlog.Transaction{tx}); err == nil {
			t.Fatal("untrusted persisted change accepted", change.Table)
		}
	}
}
