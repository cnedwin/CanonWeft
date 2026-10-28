package backup

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func TestRetentionArchiveSanitizerPreservesSourcesAndStopsExecution(t *testing.T) {
	for _, reason := range []string{"", "original diagnostic"} {
		a := emptyArchive(t)
		for _, table := range []string{"recipe_runs", "qualification_runs", "tool_trials", "skill_discovery_runs", "index_jobs"} {
			testRow(a, table, map[string]string{"state": "RUNNING", "reason": reason, "paused": "0", "cancel_requested": "0", "cancelled": "0", "restored_disabled": "0", "lease_id": strings.Repeat("a", 32)})
		}
		testRow(a, "users", map[string]string{"password_hash": "SECRET_PASSWORD", "enabled": "1"})
		testRow(a, "sessions", map[string]string{"token_hash": "SECRET_SESSION"})
		testRow(a, "generation_tasks", map[string]string{"state": "RUNNING", "credential_ref": "SECRET_KEY", "frozen": "{}"})
		for _, state := range []string{"RESERVED", "DISPATCHED", "UNKNOWN", "RECONCILED"} {
			testRow(a, "model_calls", map[string]string{"state": state, "reserved_micros": "50", "charged_micros": "0"})
			testRow(a, "qualification_calls", map[string]string{"state": state, "reserved_micros": "50", "held_micros": "50", "charged_micros": "0"})
		}
		sanitizeArchiveData(a.Data, retentionSchemaVersion)
		for _, spec := range tableCatalog {
			for i, row := range a.Data[spec.name] {
				switch spec.name {
				case "recipe_runs", "qualification_runs", "tool_trials", "skill_discovery_runs", "index_jobs":
					if spec.value(row, "reason") != reason || spec.value(row, "state") != "CANCELLED" {
						t.Fatal("history rewritten or execution retained", spec.name)
					}
				case "model_calls", "qualification_calls":
					if spec.value(row, "state") != []string{"RESERVED", "DISPATCHED", "UNKNOWN", "RECONCILED"}[i] || spec.value(row, "reserved_micros") != "50" || spec.value(row, "charged_micros") != "0" {
						t.Fatal("original accounting changed", spec.name)
					}
					if spec.name == "model_calls" && spec.value(row, "cancel_requested") != "1" {
						t.Fatal("call not cancelled")
					}
				case "generation_tasks":
					if spec.value(row, "state") != "CANCELLED" || spec.value(row, "paused") != "1" || spec.value(row, "cancel_requested") != "1" {
						t.Fatal("generation remains runnable")
					}
				case "users":
					if spec.value(row, "enabled") != "0" {
						t.Fatal("restored identity enabled")
					}
				}
			}
		}
		first, _ := json.Marshal(a.Data)
		if bytes.Contains(first, []byte("SECRET_")) {
			t.Fatal("credential or session retained")
		}
		sanitizeArchiveData(a.Data, retentionSchemaVersion)
		second, _ := json.Marshal(a.Data)
		if !bytes.Equal(first, second) {
			t.Fatal("sanitization not idempotent")
		}
	}
}

func TestRetentionArchiveSanitizerKeepsRegisteredDiagnosticBinding(t *testing.T) {
	a, _ := archiveCleanupFixture(t)
	project, run, branch := strings.Repeat("a", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)
	testRow(a, "branches", map[string]string{"id": branch, "project_id": project})
	testRow(a, "qualification_runs", map[string]string{"id": run, "project_id": project, "workspace_id": strings.Repeat("b", 32), "actor_id": strings.Repeat("c", 32), "branch_id": branch, "frozen": "{}", "state": "RUNNING", "reason": "original diagnostic"})
	owner, _ := retention.RowOwnerKey("qualification_runs", map[string]string{"id": run})
	id, _ := retention.ReferenceID(project, "qualification_runs", owner, "reason")
	born := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	deadline, err := retention.Deadline(retention.Debug, born, retention.Lifecycle{})
	if err != nil {
		t.Fatal(err)
	}
	hash := digest([]byte("original diagnostic"))
	v := retention.ReferenceVersion{Schema: 1, ProjectID: project, Table: "qualification_runs", OwnerKey: owner, Field: "reason", Class: retention.Debug, Storage: retention.Inline, SourceID: id, SourcePayloadHash: hash, ValueFirstVerifiedAt: born, LineageState: "PENDING", Deadline: deadline, RecordedAt: born, Reference: retention.Reference{ID: id, Rule: "qualification_runs.reason", PayloadHash: hash, CreatedAt: born, FirstVerifiedAt: born, TimeBasis: retention.LegacyRegistered, Revision: 1, State: retention.Retained}}
	history := &archiveReferenceHistory{Current: map[string]retention.ReferenceVersion{id: v}, Versions: map[string]retention.ReferenceVersion{}, Expiry: map[string]retention.ExpiryJournalRecord{}}
	before, err := archiveRetentionMaterials(a.Data, history)
	if err != nil {
		t.Fatal("invalid fixture", err)
	}
	sanitizeArchiveData(a.Data, retentionSchemaVersion)
	after, err := archiveRetentionMaterials(a.Data, history)
	if err != nil {
		t.Fatal("sanitizer invalidated original source proof", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source materials changed during runtime disabling")
	}
}

func TestRetentionArchiveSanitizerKeepsAccountingSourceHashes(t *testing.T) {
	a := weeklyBudgetArchive(t)
	project, workspace, actor, branch := strings.Repeat("a", 32), strings.Repeat("9", 32), strings.Repeat("1", 32), strings.Repeat("8", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "users", map[string]string{"id": actor, "workspace_id": workspace})
	testRow(a, "branches", map[string]string{"id": branch, "project_id": project})
	for _, table := range []string{"projects", "model_calls", "qualification_runs", "generation_tasks"} {
		archiveQualificationCell(a, table, 0, "workspace_id", workspace)
	}
	for _, table := range []string{"qualification_runs", "generation_tasks"} {
		archiveQualificationCell(a, table, 0, "branch_id", branch)
	}
	for _, table := range []string{"model_calls", "qualification_calls"} {
		archiveQualificationCell(a, table, 0, "state", "DISPATCHED")
	}
	archiveQualificationCell(a, "model_calls", 0, "input_hash", strings.Repeat("3", 64))
	archiveQualificationCell(a, "model_call_accounting", 0, "revision", "0")
	read := func() []retention.MaterialReference {
		owners, err := newRetentionArchiveOwners(a.Data)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := owners.index.AccountingReferences(project)
		if err != nil || len(refs) != 4 {
			t.Fatal("invalid financial fixture", len(refs), err)
		}
		return refs
	}
	before := read()
	sanitizeArchiveData(a.Data, retentionSchemaVersion)
	after := read()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("runtime sanitization changed independent financial source hashes")
	}
	if err := validateQualificationBudget(a.Data); err != nil {
		t.Fatal("sanitization broke budget history", err)
	}
}
