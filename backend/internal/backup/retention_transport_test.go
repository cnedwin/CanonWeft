package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"novelstudio/internal/retention"
	"novelstudio/migrations"
)

func TestRetentionArchiveTransportKeepsAllMetadataAndExactCells(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = retentionSchemaVersion
	migration, sql, err := retentionMigration()
	if err != nil || migration.Name != retention.MigrationName || migration.Version != 36 || digest([]byte(sql)) != migration.SHA256 {
		t.Fatal("future migration identity is not exact", err)
	}
	if _, err := migrations.Files.ReadFile(retention.MigrationName); err == nil {
		t.Fatal("pending contract activated the normal migration runner")
	}
	migration.Origin = "EXECUTED"
	a.Manifest.Migrations = append(a.Manifest.Migrations, migration)
	if err := validateManifest(a.Manifest); err != nil {
		t.Fatal("exact pending archive contract not recognized", err)
	}
	wrong := a.Manifest
	wrong.Migrations = append([]Migration(nil), a.Manifest.Migrations...)
	wrong.Migrations[len(wrong.Migrations)-1].SHA256 = strings.Repeat("0", 64)
	if err := validateManifest(wrong); err == nil {
		t.Fatal("unqualified migration contract accepted")
	}
	catalog := retentionTableCatalog()
	if len(catalog) != 12 {
		t.Fatal("incomplete retention table catalog", len(catalog))
	}
	for _, table := range catalog {
		a.Data[table.name] = nil
		if !reflect.DeepEqual(table.cols(), retention.RegistrationMetadata()[table.name]) {
			t.Fatal("live/archive column order diverged", table.name)
		}
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table.name+" (") {
			t.Fatal("catalog table absent from pending SQL", table.name)
		}
	}
	// These are transport fixtures, not a semantically valid source graph. NULL,
	// empty text and non-UTF8 bytes must remain distinct before typed validation.
	a.Data["retention_source_values"] = []Row{{[]byte(strings.Repeat("a", 64)), []byte(strings.Repeat("b", 64)), []byte("0"), []byte("2026-10-26 01:02:03.123456")}}
	a.Data["retention_objects"] = []Row{{[]byte(strings.Repeat("c", 32)), []byte(strings.Repeat("d", 64)), nil, []byte{}, []byte{0xff, 0x00}, []byte("1"), nil}}
	var output bytes.Buffer
	if err := writeArchive(&output, a); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Data
	for _, entry := range zr.File {
		if entry.Name != "data.json" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
	}
	business, metadata, err := splitRetentionData(36, decoded)
	if err != nil || len(metadata) != 12 || len(business) != len(tableCatalog) {
		t.Fatal("zip transport lost table contract", err)
	}
	if !reflect.DeepEqual(metadata["retention_objects"], a.Data["retention_objects"]) || !reflect.DeepEqual(metadata["retention_source_values"], a.Data["retention_source_values"]) {
		t.Fatal("transport normalized immutable cells")
	}
	delete(business, "projects")
	if _, ok := decoded["projects"]; !ok {
		t.Fatal("partition mutated original archive")
	}
	if _, err := ValidateArchive(bytes.NewReader(output.Bytes()), int64(output.Len())); err != errRetentionHistory {
		t.Fatal("transport-only graph bypassed source identity validation", err)
	}
}

func TestRetentionArchiveTransportRejectsSchemaAndShapeConfusion(t *testing.T) {
	a := emptyArchive(t)
	for _, table := range retentionTableCatalog() {
		a.Data[table.name] = nil
	}
	for _, schema := range []int{23, 35, 37} {
		if _, _, err := splitRetentionData(schema, a.Data); err == nil {
			t.Fatal("metadata smuggled into unsupported schema", schema)
		}
	}
	delete(a.Data, "retention_evidence")
	if _, _, err := splitRetentionData(36, a.Data); err == nil {
		t.Fatal("missing metadata accepted")
	}
	a.Data["retention_evidence"] = []Row{{[]byte("short")}}
	if _, _, err := splitRetentionData(36, a.Data); err == nil {
		t.Fatal("short row accepted")
	}
	a.Data["retention_evidence"] = nil
	a.Data["unknown_retention_table"] = nil
	if _, _, err := splitRetentionData(36, a.Data); err == nil {
		t.Fatal("unknown table accepted")
	}
}
