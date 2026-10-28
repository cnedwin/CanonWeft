package retention

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceObservationsIncludePrivateFieldsAndActualBlobsOnly(t *testing.T) {
	rows := sourceOwnershipRows()
	project, actor, branch := strings.Repeat("2", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	rows[2].Values["name"] = "PRIVATE_SOURCE_SENTINEL"
	rows[6].Values["operation_id"] = "PRIVATE_REQUEST_SENTINEL"
	rows = append(rows, inventoryRow("generation_tasks", map[string]string{
		"id": strings.Repeat("a", 32), "workspace_id": strings.Repeat("1", 32), "project_id": project, "actor_id": actor, "branch_id": branch,
		"input_hash": strings.Repeat("d", 64), "credential_ref": "PRIVATE_CREDENTIAL_SENTINEL", "frozen": `{"input_hash":"` + strings.Repeat("e", 64) + `"}`,
	}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := index.SourceObservations(project)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, source := range observed {
		found[source.Table+"."+source.Field] = true
		if source.ID != observationID(project, source.Table, source.OwnerKey, source.Field) || !hash64.MatchString(source.PayloadHash) {
			t.Fatal("invalid source identity")
		}
	}
	for _, required := range []string{"projects.name", "branches.name", "operations.operation_id", "generation_tasks.frozen", "generation_tasks.operation_id"} {
		if !found[required] {
			t.Error("missing real source", required)
		}
	}
	for _, excluded := range []string{"users.password_hash", "generation_tasks.credential_ref", "generation_tasks.input_hash", "projects.created_at"} {
		if found[excluded] {
			t.Error("credential/structural fingerprint copied to source journal", excluded)
		}
	}
	raw, _ := json.Marshal(observed)
	for _, private := range []string{"PRIVATE_SOURCE_SENTINEL", "PRIVATE_REQUEST_SENTINEL", "PRIVATE_CREDENTIAL_SENTINEL"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("source journal contains original private text")
		}
	}
}

func TestSourceObservationsDistinguishNullAndEmptyWithoutChangingSlot(t *testing.T) {
	rows := sourceOwnershipRows()
	project := strings.Repeat("2", 32)
	collect := func() SourceObservation {
		t.Helper()
		index, err := NewSourceIndex(rows)
		if err != nil {
			t.Fatal(err)
		}
		observations, err := index.SourceObservations(project)
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range observations {
			if source.Table == "projects" && source.Field == "name" {
				return source
			}
		}
		t.Fatal("missing source")
		return SourceObservation{}
	}
	empty := collect()
	rows[2].Nulls = map[string]bool{"name": true}
	null := collect()
	if empty.ID != null.ID || empty.Null || !null.Null || empty.PayloadHash != null.PayloadHash {
		t.Fatal("NULL/empty observation identity is not explicit")
	}
	// SourceIndex is a value snapshot, not a SQL nullability validator; the SQL
	// scanner itself can only produce NULL for fields the database permits.
}
