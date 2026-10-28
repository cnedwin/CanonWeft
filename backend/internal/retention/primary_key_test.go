package retention

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestPrimaryKeyCatalogMatchesEveryBusinessPrimaryKey(t *testing.T) {
	paths, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	create := regexp.MustCompile(`(?is)CREATE TABLE IF NOT EXISTS (\w+)\s*\((.*?);`)
	composite := regexp.MustCompile(`(?i)PRIMARY KEY\s*\(([^)]+)\)`)
	inline := regexp.MustCompile(`(?i)(\w+)\s+(?:CHAR\(\d+\)|BIGINT)(?: NOT NULL)?(?: AUTO_INCREMENT)? PRIMARY KEY`)
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range create.FindAllStringSubmatch(string(raw), -1) {
			table, body := match[1], match[2]
			if !slices.Contains(KnownTables(), table) {
				continue
			}
			seen[table] = true
			var expected []string
			if m := composite.FindStringSubmatch(body); m != nil {
				for _, name := range strings.Split(m[1], ",") {
					expected = append(expected, strings.TrimSpace(name))
				}
			} else if m := inline.FindStringSubmatch(body); m != nil {
				expected = []string{m[1]}
			}
			pk := PrimaryKey(table)
			var got []string
			for _, part := range pk {
				got = append(got, part.Name)
			}
			if len(expected) == 0 || !slices.Equal(expected, got) {
				t.Fatalf("%s primary key: got %v want %v", table, got, expected)
			}
		}
	}
	if len(seen) != len(KnownTables()) {
		t.Fatal("incomplete schema coverage", len(seen))
	}
	if PrimaryKey("future_table") != nil {
		t.Fatal("unclassified table accepted")
	}
}

func TestRowOwnerKeyUsesColumnTypeRatherThanValueShape(t *testing.T) {
	id := strings.Repeat("a", 32)
	got, err := RowOwnerKey("capability_heads", map[string]string{"project_id": id, "capability_key": id})
	want, _ := OwnerKey(id, DigestKeyPart(id))
	if err != nil || got != want {
		t.Fatal("hex-shaped free text must still be digested", got, err)
	}
	numeric, err := RowOwnerKey("model_connection_versions", map[string]string{"connection_id": id, "revision": "2"})
	expected, _ := OwnerKey(id, "2")
	if err != nil || numeric != expected {
		t.Fatal("numeric PK encoding mismatch", numeric, err)
	}
	for _, row := range []map[string]string{{"connection_id": id}, {"connection_id": id, "revision": "02"}, {"connection_id": "2", "revision": "2"}} {
		if _, err := RowOwnerKey("model_connection_versions", row); err == nil {
			t.Fatal("invalid typed primary key accepted", row)
		}
	}
	if _, err := RowOwnerKey("unknown", map[string]string{"id": id}); err == nil {
		t.Fatal("unknown owner table accepted")
	}
	pk := PrimaryKey("capability_heads")
	pk[0].Name = "changed"
	if PrimaryKey("capability_heads")[0].Name != "project_id" {
		t.Fatal("callers can mutate shared catalog")
	}
}
