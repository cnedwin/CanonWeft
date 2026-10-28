package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccountingMaterialsPreserveWeeklyHoldWithoutPrivateText(t *testing.T) {
	rows, _ := qualificationFinancialRows()
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AccountingReferences(strings.Repeat("2", 32))
	if err != nil || len(refs) != 5 {
		t.Fatal(refs, err)
	}
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for _, ref := range refs {
		if ref.Class != Audit || ref.Storage != Metadata || ref.Accounting == nil || !ref.Accounting.open() || ref.ObjectHash != "" || ref.ContentRoot {
			t.Fatal("incorrect accounting policy", ref)
		}
		v, err := materialReferenceVersion(strings.Repeat("2", 32), ObservedMaterial{MaterialReference: ref, TimeBasis: LegacyRegistered, FirstVerifiedAt: now, ValueFirstVerifiedAt: now}, Lifecycle{}, now)
		if err != nil || !v.Reference.AccountingOpen || !v.Deadline.Equal(now.Add(365*day)) {
			t.Fatal("raw TTL leaked into accounting", v, err)
		}
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "PRIVATE_") {
			t.Fatal("source text in accounting record")
		}
		if _, err := decodeReferenceVersion(string(raw), financialDigest(raw)); err != nil {
			t.Fatal(err)
		}
		v.Reference.AccountingOpen = false
		if validReferenceVersion(v) {
			t.Fatal("unknown hold could be silently released")
		}
	}
}

func TestAccountingAuditWallTimeStableAcrossDriverLocations(t *testing.T) {
	rows := sourceOwnershipRows()
	project := strings.Repeat("2", 32)
	row := inventoryRow("audit_events", map[string]string{"id": "1", "workspace_id": strings.Repeat("1", 32), "actor_id": strings.Repeat("4", 32), "resource_id": project, "action": "project.create", "created_at": "2026-10-25 01:02:03.000004"})
	var original string
	for _, clock := range []string{"2026-10-25 01:02:03.000004", "2026-10-25T01:02:03.000004Z", "2026-10-25T01:02:03.000004+08:00"} {
		row.Values["created_at"] = clock
		index, err := NewSourceIndex(append(rows, row))
		if err != nil {
			t.Fatal(err)
		}
		refs, err := index.AccountingReferences(project)
		if err != nil || len(refs) != 1 {
			t.Fatal(refs, err)
		}
		if refs[0].Accounting.Audit.SourceWallTime != "2026-10-25 01:02:03.000004" {
			t.Fatal("legacy clock converted to invented UTC")
		}
		if original != "" && original != refs[0].PayloadHash {
			t.Fatal("driver location created a new accounting version")
		}
		original = refs[0].PayloadHash
	}
	row.Values["action"] = "PRIVATE_UNREVIEWED_ACTION"
	index, _ := NewSourceIndex(append(rows, row))
	if _, err := index.AccountingReferences(project); err == nil {
		t.Fatal("free-text audit action accepted")
	}
}
