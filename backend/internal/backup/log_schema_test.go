package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

func TestLogJournalHistoricalSchemasRemainRecoverableAfter35Upgrade(t *testing.T) {
	for _, schema := range []int{30, 31, 32, 33, 34} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			a := emptyArchive(t)
			a.Manifest.Schema = schema
			beforeMemory(a)
			a.Manifest.Migrations = a.Manifest.Migrations[:schema]
			for _, name := range append([]string{"recipe_runs", "recipe_steps", "patch_drafts", "ingest_jobs", "import_drafts"}, semanticReviewTables...) {
				if logSchemaFutureTable(schema, name) {
					delete(a.Data, name)
				}
			}
			w := strings.Repeat("a", 32)
			a.Data["workspaces"] = []Row{Row(logRow(w, "before", "2026-10-25 12:00:00"))}
			raw := archiveBytes(t, a)
			var tables []LogTable
			for _, table := range rowContracts() {
				if !logSchemaFutureTable(schema, table.Layout.Name) {
					tables = append(tables, table)
				}
			}
			b := LogBaseline{Format: 1, Schema: schema, ArchiveSHA256: digest(raw), Source: a.Manifest.Source, ObservedAt: a.Manifest.SnapshotAt, Position: binlog.Position{File: "binlog.000001", Offset: 100}, Tables: tables}
			r, err := OpenLogReplay(b, raw)
			if err != nil {
				t.Fatal("retained schema30 baseline unavailable after additive upgrade", err)
			}
			oldData, _ := json.Marshal(a.Data)
			bounded := *r
			bounded.capacity.data = int64(len(oldData))
			if _, err := bounded.PrepareSegment(context.Background(), nil, b.Position, b.ObservedAt.Add(time.Second), nil); err != nil {
				t.Fatal("empty additive tables consumed historical data capacity", err)
			}
			end := binlog.Position{File: b.Position.File, Offset: 200}
			tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "recovered", "2026-10-25 12:00:00")}}}
			segment, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{tx})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.ApplySegment(segment, digest(segment)); err != nil {
				t.Fatal(err)
			}
			selected, err := r.ArchiveBytes()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
			if err != nil || restored.Manifest.Schema != schema || len(restored.Manifest.Migrations) != schema || string(restored.Data["workspaces"][0][1]) != "recovered" {
				t.Fatal("historical schema/hash binding lost", err)
			}
			for _, name := range semanticReviewTables {
				if rows, ok := restored.Data[name]; !ok || len(rows) != 0 {
					t.Fatal("trusted conversion did not add exactly empty new table", name)
				}
			}
			// A schema30 marker cannot authorize a schema31 table, even when empty.
			b.Tables = rowContracts()
			if _, err = OpenLogReplay(b, raw); err == nil {
				t.Fatal("mixed version row contract accepted")
			}
			var decoded LogSegment
			if err = json.Unmarshal(segment, &decoded); err != nil {
				t.Fatal(err)
			}
			decoded.Schema = 35
			changed, _ := json.Marshal(decoded)
			b.Tables = tables
			r, err = OpenLogReplay(b, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.ApplySegment(changed, digest(changed)); err == nil {
				t.Fatal("cross version segment accepted")
			}
		})
	}
}
