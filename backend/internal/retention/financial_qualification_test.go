package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func qualificationFinancialRows() ([]SourceRow, string) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	rows := sourceOwnershipRows()
	payload := `["UNKNOWN",0,"PRIVATE_RECEIPT",""]`
	hash := func(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
	receipt := hash(id("a") + "\x00" + hash(payload))
	rows = append(rows,
		inventoryRow("model_calls", map[string]string{"id": id("a"), "project_id": id("2"), "workspace_id": id("1"), "actor_id": id("4"), "state": "UNKNOWN", "input_hash": strings.Repeat("f", 64), "reserved_micros": "80", "charged_micros": "0"}),
		inventoryRow("model_call_accounting", map[string]string{"call_id": id("a"), "currency": "USD", "revision": "1"}),
		inventoryRow("model_call_receipts", map[string]string{"id": receipt, "call_id": id("a"), "payload": payload}),
		inventoryRow("qualification_runs", map[string]string{"id": id("b"), "project_id": id("2"), "branch_id": id("5"), "actor_id": id("4")}),
		inventoryRow("qualification_steps", map[string]string{"id": id("c"), "project_id": id("2"), "run_id": id("b")}),
		inventoryRow("qualification_schedules", map[string]string{"id": id("d"), "project_id": id("2"), "actor_id": id("4")}),
		inventoryRow("qualification_week_grants", map[string]string{"id": id("e"), "project_id": id("2"), "schedule_id": id("d"), "schedule_revision": "2", "week_key": "2026-W43", "currency": "USD", "cap_micros": "500", "reserved_micros": "80", "spent_micros": "20"}),
		inventoryRow("qualification_calls", map[string]string{"call_id": id("a"), "project_id": id("2"), "run_id": id("b"), "step_id": id("c"), "grant_id": id("e"), "reserved_micros": "80", "held_micros": "80", "charged_micros": "0", "state": "UNKNOWN", "origin": "NATIVE_30"}),
	)
	return rows, receipt
}

func TestFinancialSummaryPreservesQualificationHoldAndCurrentWeeklyBalance(t *testing.T) {
	rows, receipt := qualificationFinancialRows()
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := index.FinancialSummary("model_call_receipts", map[string]string{"id": receipt})
	if err != nil {
		t.Fatal(err)
	}
	accounting := summary.QualificationAccounting
	if accounting == nil || accounting.HeldMicros != 80 || accounting.ChargedMicros != 0 || accounting.RunID != strings.Repeat("b", 32) || accounting.StepID != strings.Repeat("c", 32) {
		t.Fatal("qualification accounting disappeared from receipt evidence")
	}
	if accounting.Weekly == nil || accounting.Weekly.Revision != 2 || accounting.Weekly.ReservedMicros != 80 || accounting.Weekly.SpentMicros != 20 || accounting.Weekly.CapMicros != 500 {
		t.Fatal("current weekly balance disappeared")
	}
	if summary.WeeklyGrant != nil {
		t.Fatal("current weekly authority was misrepresented as the original frozen grant")
	}
	rows[len(rows)-1].Values["grant_id"] = ""
	index, _ = NewSourceIndex(rows)
	summary, err = index.FinancialSummary("model_call_receipts", map[string]string{"id": receipt})
	if err != nil || summary.QualificationAccounting == nil || summary.QualificationAccounting.Weekly != nil {
		t.Fatal("manual qualification without weekly grant was rejected", err)
	}
}

func TestFinancialSummaryRejectsUnrelatedOrInconsistentQualificationAccounting(t *testing.T) {
	for _, change := range []string{"cross-project", "wrong-run", "missing-step", "missing-grant", "missing-schedule", "currency", "hold", "charge", "state", "week-hold", "week-spend", "origin", "null-amount"} {
		t.Run(change, func(t *testing.T) {
			rows, receipt := qualificationFinancialRows()
			call, grant, step := &rows[len(rows)-1], &rows[len(rows)-2], &rows[len(rows)-4]
			switch change {
			case "cross-project":
				grant.Values["project_id"] = strings.Repeat("3", 32)
			case "wrong-run":
				other := inventoryRow("qualification_runs", map[string]string{"id": strings.Repeat("9", 32), "project_id": strings.Repeat("2", 32), "branch_id": strings.Repeat("5", 32)})
				step.Values["run_id"] = other.Values["id"]
				rows = append(rows, other)
			case "missing-step":
				call.Values["step_id"] = strings.Repeat("9", 32)
			case "missing-grant":
				call.Values["grant_id"] = strings.Repeat("9", 32)
			case "missing-schedule":
				grant.Values["schedule_id"] = strings.Repeat("9", 32)
			case "currency":
				grant.Values["currency"] = "CNY"
			case "hold":
				call.Values["held_micros"] = "0"
			case "charge":
				call.Values["charged_micros"] = "1"
			case "state":
				call.Values["state"] = "SUCCEEDED"
			case "week-hold":
				grant.Values["reserved_micros"] = "79"
			case "week-spend":
				call.Values["charged_micros"] = "21"
			case "origin":
				call.Values["origin"] = "PRIVATE_FREE_TEXT"
			case "null-amount":
				call.Nulls = map[string]bool{"held_micros": true}
			}
			index, err := NewSourceIndex(rows)
			if err == nil {
				_, err = index.FinancialSummary("model_call_receipts", map[string]string{"id": receipt})
			}
			if err == nil {
				t.Fatal("inconsistent financial ancestry accepted")
			}
		})
	}
}
