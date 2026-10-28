package backup

import (
	"strings"
	"testing"

	"novelstudio/internal/retention"
)

func TestRetentionArchiveIncludesProjectsBeforeFirstRegistration(t *testing.T) {
	a := emptyArchive(t)
	project, workspace := strings.Repeat("a", 32), strings.Repeat("f", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace, "name": "not registered yet"})
	history := &archiveReferenceHistory{Current: map[string]retention.ReferenceVersion{}, Versions: map[string]retention.ReferenceVersion{}, Expiry: map[string]retention.ExpiryJournalRecord{}}
	materials, err := archiveRetentionMaterials(a.Data, history)
	if err != nil {
		t.Fatal(err)
	}
	for _, material := range materials[project] {
		if material.Table == "projects" && material.Field == "name" && material.PayloadHash == digest([]byte("not registered yet")) {
			return
		}
	}
	t.Fatal("unregistered project content disappeared from backup material selection")
}
