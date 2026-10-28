package backup

import (
	"bytes"
	"strings"
	"testing"
)

// Construct an actual historical catalog: old archives cannot contain new tables
// or columns just because the current test helper knows about them.
func beforePrivateIndexes(a *Archive) {
	beforeQualifications(a)
	delete(a.Data, "working_indexes")
	delete(a.Data, "working_chunks")
	for i, row := range a.Data["index_jobs"] {
		a.Data["index_jobs"][i] = row[:13]
	}
}

func TestPrivateIndexArchiveIsDisabledAndLegacyCatalogsUpgrade(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "working_indexes", map[string]string{"id": strings.Repeat("a", 64), "project_id": strings.Repeat("b", 32), "branch_id": strings.Repeat("c", 32), "actor_id": strings.Repeat("d", 32), "manifest": "{}", "chunks": "1", "enabled": "1"})
	testRow(a, "working_chunks", map[string]string{"id": strings.Repeat("e", 64), "index_id": strings.Repeat("a", 64), "source_key": strings.Repeat("f", 64), "ref_id": strings.Repeat("b", 32), "ref_kind": "resource_draft", "ref_version": "7", "source_hash": strings.Repeat("1", 64), "vector_hash": strings.Repeat("2", 64), "content": "private original"})
	testRow(a, "index_jobs", map[string]string{"id": strings.Repeat("a", 32), "index_id": strings.Repeat("a", 64), "kind": "working", "state": "READY"})
	sanitize(a.Data)
	raw := archiveBytes(t, a)
	restored, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tableCatalog {
		for _, r := range restored.Data[s.name] {
			if s.name == "working_indexes" && s.value(r, "enabled") != "0" {
				t.Fatal("restored private index enabled")
			}
			if s.name == "index_jobs" && s.value(r, "state") != "CANCELLED" {
				t.Fatal("restored private job eligible")
			}
		}
	}
	for _, version := range []int{24, 25, 26} {
		a = emptyArchive(t)
		testRow(a, "index_jobs", map[string]string{"id": strings.Repeat("a", 32), "kind": "published", "state": "CANCELLED"})
		a.Manifest.Schema = version
		a.Manifest.Migrations = a.Manifest.Migrations[:version]
		beforePrivateIndexes(a)
		if version < 26 {
			delete(a.Data, "generation_previews")
		}
		raw = archiveBytes(t, a)
		converted, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(version, err)
		}
		if string(converted.Data["index_jobs"][0][13]) != "published" {
			t.Fatal("historical job not upgraded")
		}
		a.Data["working_indexes"] = nil
		raw = archiveBytes(t, a)
		if _, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Fatal("injected private table accepted", version)
		}
		delete(a.Data, "working_indexes")
		a.Data["index_jobs"][0] = append(a.Data["index_jobs"][0], []byte("working"))
		raw = archiveBytes(t, a)
		if _, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Fatal("injected private kind accepted", version)
		}
	}
}
