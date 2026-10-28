package backup

import (
	"bytes"
	"strings"
	"testing"
)

func TestSchema33RecipeConversionRejectsFutureTables(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = 33
	beforeMemory(a)
	a.Manifest.Migrations = a.Manifest.Migrations[:33]
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("future recipe tables accepted under schema33")
	}
	delete(a.Data, "recipe_runs")
	delete(a.Data, "recipe_steps")
	raw = archiveBytes(t, a)
	read, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recipe_runs", "recipe_steps"} {
		if rows, ok := read.Data[name]; !ok || len(rows) != 0 {
			t.Fatal("missing empty upgrade", name)
		}
	}
}

func TestRecipeArchiveCancelsExecutionAndRetainsEveryAttemptObject(t *testing.T) {
	a := emptyArchive(t)
	p := strings.Repeat("b", 32)
	testRow(a, "recipe_runs", map[string]string{"id": strings.Repeat("a", 32), "project_id": p, "state": "WAITING_CONFIRMATION", "paused": "0", "cancel_requested": "0", "restored_disabled": "0"})
	input, output := digest([]byte("input")), digest([]byte("output"))
	testRow(a, "recipe_steps", map[string]string{"id": strings.Repeat("c", 32), "project_id": p, "input_hash": input, "output_hash": output, "confirmed_hash": output, "credential_ref": "private-vault-reference"})
	sanitize(a.Data)
	for _, spec := range tableCatalog {
		if spec.name == "recipe_runs" {
			r := a.Data[spec.name][0]
			if spec.value(r, "state") != "CANCELLED" || spec.value(r, "restored_disabled") != "1" || spec.value(r, "cancel_requested") != "1" {
				t.Fatal("recipe restored active")
			}
		}
		if spec.name == "recipe_steps" && spec.value(a.Data[spec.name][0], "credential_ref") != "" {
			t.Fatal("credential reference leaked")
		}
	}
	refs, err := blobReferences(a.Data)
	if err != nil || !refs["blobs/"+p+"/"+input] || !refs["blobs/"+p+"/"+output] || len(refs) != 2 {
		t.Fatal("recipe attempt content lost", refs, err)
	}
	if validateData(a.Data) == nil {
		t.Fatal("orphan recipe execution accepted")
	}
}
