package backup

import (
	"bytes"
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

func patchDraftArchive(t *testing.T) *Archive {
	a := emptyArchive(t)
	w, p, b, u := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	testRow(a, "workspaces", map[string]string{"id": w})
	testRow(a, "projects", map[string]string{"id": p, "workspace_id": w})
	testRow(a, "branches", map[string]string{"id": b, "project_id": p})
	testRow(a, "users", map[string]string{"id": u, "workspace_id": w, "enabled": "0"})
	content := domain.PatchDraftContent{OperationID: "private-edit", Group: domain.TextPatchGroup{ID: strings.Repeat("e", 32)}, Snippet: "未保存成候选的修订🌙"}
	raw, _ := json.Marshal(content)
	testRow(a, "patch_drafts", map[string]string{"branch_id": b, "actor_id": u, "project_id": p, "revision": "3", "payload": string(raw), "content_hash": digest(raw)})
	return a
}

func TestPrivatePatchDraftBackupRoundTripAndCorruption(t *testing.T) {
	a := patchDraftArchive(t)
	raw := archiveBytes(t, a)
	got, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data["patch_drafts"]) != 1 {
		t.Fatal("private work lost")
	}
	for key, value := range map[string]string{"payload": "{}", "revision": "0", "actor_id": strings.Repeat("f", 32), "branch_id": strings.Repeat("f", 32), "project_id": strings.Repeat("f", 32)} {
		t.Run(key, func(t *testing.T) {
			a := patchDraftArchive(t)
			for _, s := range tableCatalog {
				if s.name == "patch_drafts" {
					s.set(a.Data[s.name][0], key, value)
				}
			}
			if validateData(a.Data) == nil {
				t.Fatal("invalid private work accepted")
			}
		})
	}
}

func TestSchema31PrivateDraftConversionRejectsFutureTable(t *testing.T) {
	a := emptyArchive(t)
	for _, name := range recipeTables {
		delete(a.Data, name)
	}
	for _, name := range ingestTables {
		delete(a.Data, name)
	}
	a.Manifest.Schema = 31
	beforeMemory(a)
	a.Manifest.Migrations = a.Manifest.Migrations[:31]
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("future table accepted in historical archive")
	}
	delete(a.Data, "patch_drafts")
	raw = archiveBytes(t, a)
	got, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if rows, ok := got.Data["patch_drafts"]; !ok || len(rows) != 0 {
		t.Fatal("explicit empty conversion missing")
	}
}
