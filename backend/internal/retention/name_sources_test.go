package retention

import (
	"strings"
	"testing"
)

func TestNameCatalogKeepsCitedOriginalWithoutAnImportDraft(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	for _, missing := range []bool{false, true} {
		rows := []SourceRow{}
		for _, row := range authorContentFixture() {
			if row.Table != "import_drafts" {
				rows = append(rows, row)
			}
		}
		original := id("7")
		if missing {
			original = id("e")
		}
		body := `{"schema":"name-material-catalog-v1","actor_id":"` + id("4") + `","materials":{"records":[{"source_import_id":"` + original + `"}]}}`
		rows = append(rows, inventoryRow("resources", map[string]string{"id": id("d"), "project_id": id("2"), "branch_id": id("5"), "kind": "note"}), inventoryRow("resource_drafts", map[string]string{"resource_id": id("d"), "actor_id": id("4"), "revision": "1", "body": body}))
		index, err := NewSourceIndex(rows)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := index.AuthorContentReferences(id("2"))
		if missing {
			if err == nil {
				t.Fatal("missing cited original was silently ignored")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		foundRaw, foundReport := false, false
		for _, ref := range refs {
			if ref.Table == "source_imports" {
				foundRaw = foundRaw || ref.Field == "source_hash"
				foundReport = foundReport || ref.Field == "report"
			}
		}
		if !foundRaw || !foundReport {
			t.Fatal("saved name catalog lost original source protection")
		}
	}
}
