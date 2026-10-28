package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionMaterialCollectionBindsOriginalClockAndPresetRoots(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "MATERIAL_PRIVATE_ORIGINAL", "material-clock")
	branch(b, project, "正史")
	b.request("POST", "/api/projects/"+project+"/capabilities/presets/research", map[string]any{"operation_id": "material-clock-presets"}, 201)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	seedProjectNameObservation(t, db, project)
	ctx := context.Background()
	w := &service.Workspace{DB: db}
	first, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || first.MaterialReferences < 22 || first.ContentRoots < 20 || first.BlobReferences < 4 || first.DeletionReady {
		t.Fatal("real preset material references missing", first, err)
	}
	if _, err := db.Exec("UPDATE projects SET name='MATERIAL_PRIVATE_EDIT' WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	second, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || second.NewValues != 1 {
		t.Fatal(second, err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := retention.ScanSources(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := retention.ObservedMaterialReferences(ctx, tx, project, snapshot.Index, snapshot.CapturedAt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, material := range materials {
		if material.Table == "projects" && material.Field == "name" {
			found = true
			if material.TimeBasis != retention.LegacyRegistered || material.FirstVerifiedAt.Format("2006-01-02 15:04:05.000000") != "2020-01-02 03:04:05.000006" || !material.ValueFirstVerifiedAt.Equal(second.CapturedAt) || material.SourcePayloadHash != material.PayloadHash {
				t.Fatal("edited value replaced original material age", material)
			}
		}
	}
	if !found {
		t.Fatal("project material was omitted")
	}
	raw, _ := json.Marshal(materials)
	if strings.Contains(string(raw), "MATERIAL_PRIVATE_") {
		t.Fatal("material descriptors contain private body")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	preview := b.request("GET", "/api/operations/retention-inventory", nil, 200)
	if preview["deletion_ready"] != false {
		t.Fatal("metadata compatibility enabled deletion")
	}
}

func TestRetentionMaterialCollectionRejectsMissingOrMismatchedObservation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "材料证据", "material-proof")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	ctx := context.Background()
	w := &service.Workspace{DB: db}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	if err := db.QueryRow("SELECT id FROM retention_source_slots WHERE project_id=? AND owner_table='projects' AND field_key='name'", project).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"owner", "value-missing", "future-clock"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			commands := map[string]string{
				"owner":         "UPDATE retention_source_slots SET owner_table='branches' WHERE id=?",
				"value-missing": "DELETE FROM retention_source_values WHERE source_id=?",
				"future-clock":  "UPDATE retention_source_values SET first_verified_at='2099-01-01' WHERE source_id=?",
			}
			if _, err := tx.Exec(commands[mode], sourceID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := retention.ScanSources(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			if materials, err := retention.ObservedMaterialReferences(ctx, tx, project, snapshot.Index, time.Now().UTC()); err == nil || materials != nil {
				t.Fatal("invalid observation produced a material inventory", mode, err)
			}
		})
	}
}

func TestRetentionMaterialCollectionInvalidSourceRollsBackRegistration(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "损坏来源登记回滚", "material-invalid-source")
	branch(b, project, "正史")
	b.request("POST", "/api/projects/"+project+"/capabilities/presets/research", map[string]any{"operation_id": "material-invalid-presets"}, 201)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	if _, err := db.Exec("UPDATE skill_packages SET source=JSON_OBJECT('kind','builtin','hidden_archive_hash',REPEAT('f',64)) WHERE project_id=? LIMIT 1", project); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	if _, err := w.RegisterRetentionSources(context.Background(), actor, project); err == nil {
		t.Fatal("unparsed package source became a registered material")
	}
	for _, table := range []string{"retention_source_slots", "retention_source_values", "project_lifecycle"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("invalid material left partial registration", table, count, err)
		}
	}
}
