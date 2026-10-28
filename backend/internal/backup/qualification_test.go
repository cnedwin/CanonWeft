package backup

import (
	"bytes"
	"strings"
	"testing"
)

func beforeQualifications(a *Archive) {
	beforeQualificationBudget(a)
	for _, name := range []string{"qualification_runs", "qualification_steps", "capability_qualifications", "capability_bindings", "capability_binding_heads", "qualification_approvals", "qualification_schedules"} {
		delete(a.Data, name)
	}
}

func beforeQualificationBudget(a *Archive) {
	beforeMemory(a)
	delete(a.Data, "patch_drafts")
	for _, name := range recipeTables {
		delete(a.Data, name)
	}
	for _, name := range ingestTables {
		delete(a.Data, name)
	}
	for _, name := range semanticReviewTables {
		delete(a.Data, name)
	}
	delete(a.Data, "qualification_week_grants")
	delete(a.Data, "qualification_calls")
}

func TestSchema28ArchiveExplicitlyUpgradesWeeklyBudgetTables(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = 28
	a.Manifest.Migrations = a.Manifest.Migrations[:28]
	beforeQualificationBudget(a)
	raw := archiveBytes(t, a)
	got, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"qualification_week_grants", "qualification_calls"} {
		if rows, ok := got.Data[name]; !ok || len(rows) != 0 {
			t.Fatal("legacy28 weekly authority must be explicitly empty", name)
		}
	}
}

func TestArchiveRejectsWeeklyBalancesThatDoNotMatchCallHistory(t *testing.T) {
	a := weeklyBudgetArchive(t)
	archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "49")
	if err := validateData(a.Data); err == nil {
		t.Fatal("accepted corrupted weekly held balance")
	}
	for _, spec := range tableCatalog {
		if spec.name == "qualification_week_grants" {
			spec.set(a.Data[spec.name][0], "reserved_micros", "50")
		}
	}
	if err := validateData(a.Data); err != nil {
		t.Fatal("valid weekly financial history rejected", err)
	}
	for _, spec := range tableCatalog {
		if spec.name == "qualification_week_grants" {
			spec.set(a.Data[spec.name][0], "reserved_micros", "0")
		}
		if spec.name == "qualification_calls" {
			spec.set(a.Data[spec.name][0], "held_micros", "0")
		}
	}
	if err := validateData(a.Data); err == nil {
		t.Fatal("unresolved model call hold disappeared by changing both weekly sums")
	}
}

func TestRestoreDisablesAllQualificationAuthority(t *testing.T) {
	a := emptyArchive(t)
	if _, exists := a.Data["qualification_runs"]; !exists {
		t.Fatal("qualification jobs are missing from the backup/restore authority contract")
	}
	testRow(a, "qualification_runs", map[string]string{"id": strings.Repeat("a", 32), "state": "RUNNING", "frozen": "{}"})
	testRow(a, "capability_qualifications", map[string]string{"id": strings.Repeat("b", 32), "enabled": "1"})
	testRow(a, "capability_bindings", map[string]string{"id": strings.Repeat("c", 32), "enabled": "1"})
	testRow(a, "qualification_schedules", map[string]string{"id": strings.Repeat("d", 32), "enabled": "1"})
	sanitize(a.Data)
	for _, spec := range tableCatalog {
		for _, row := range a.Data[spec.name] {
			if spec.name == "qualification_runs" && spec.value(row, "state") != "CANCELLED" {
				t.Fatal("restored evaluation can resume")
			}
			if strings.HasPrefix(spec.name, "capability_") || spec.name == "qualification_schedules" {
				if spec.index("enabled") >= 0 && spec.value(row, "enabled") != "0" {
					t.Fatal("restored qualification authority active", spec.name)
				}
			}
		}
	}
}

func TestSchema27ArchiveExplicitlyUpgradesQualificationTables(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = 27
	a.Manifest.Migrations = a.Manifest.Migrations[:27]
	beforeQualificationBudget(a)
	for _, name := range []string{"qualification_runs", "qualification_steps", "capability_qualifications", "capability_bindings", "capability_binding_heads", "qualification_approvals", "qualification_schedules"} {
		delete(a.Data, name)
	}
	raw := archiveBytes(t, a)
	got, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Data["qualification_runs"]; !ok {
		t.Fatal("schema27 compatibility not explicit")
	}
}
