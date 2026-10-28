package backup

import (
	"bytes"
	"encoding/json"
	"novelstudio/internal/retrieval"
	"strings"
	"testing"
)

func TestIngestArchiveKeepsOriginalAndCheckpointButCancelsExecution(t *testing.T) {
	a := emptyArchive(t)
	w, p, b, u, id := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)
	testRow(a, "workspaces", map[string]string{"id": w})
	testRow(a, "projects", map[string]string{"id": p, "workspace_id": w})
	testRow(a, "branches", map[string]string{"id": b, "project_id": p})
	testRow(a, "users", map[string]string{"id": u, "workspace_id": w, "enabled": "0"})
	original := []byte("original scan bytes")
	hash := digest(original)
	a.Blobs["blobs/"+p+"/"+hash] = original
	progress, _ := json.Marshal(map[string]any{"plan": retrieval.ImportPlan{Parser: retrieval.DocumentParser, Count: 2}, "units": []retrieval.ImportUnit{{Locator: "page:1", Kind: "pdf_text", Status: "extracted", Text: "已完成的第一页"}}})
	testRow(a, "ingest_jobs", map[string]string{"id": id, "project_id": p, "branch_id": b, "actor_id": u, "filename": "book.pdf", "source_hash": hash, "source_size": "19", "parser_revision": retrieval.DocumentParser, "ocr_mode": "enabled", "progress": string(progress), "progress_hash": digest(progress), "state": "RUNNING", "revision": "3", "paused": "0", "cancel_requested": "0"})
	if validateData(a.Data) == nil {
		t.Fatal("restored job retains execution authority")
	}
	sanitize(a.Data)
	raw := archiveBytes(t, a)
	read, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tableCatalog {
		if s.name == "ingest_jobs" {
			row := read.Data[s.name][0]
			if s.value(row, "state") != "CANCELLED" || s.value(row, "progress") != string(progress) {
				t.Fatal("checkpoint lost or job active")
			}
			s.set(row, "progress_hash", strings.Repeat("f", 64))
			if validateData(read.Data) == nil {
				t.Fatal("corrupt checkpoint accepted")
			}
		}
	}
	delete(a.Blobs, "blobs/"+p+"/"+hash)
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("missing original accepted")
	}
}

func TestSchema32IngestConversionOnlyAddsAbsentTables(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = 32
	beforeMemory(a)
	a.Manifest.Migrations = a.Manifest.Migrations[:32]
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("unexpected future tables accepted")
	}
	for _, name := range recipeTables {
		delete(a.Data, name)
	}
	for _, name := range ingestTables {
		delete(a.Data, name)
	}
	raw = archiveBytes(t, a)
	got, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ingestTables {
		if rows, ok := got.Data[name]; !ok || len(rows) != 0 {
			t.Fatal("conversion missing", name)
		}
	}
}
