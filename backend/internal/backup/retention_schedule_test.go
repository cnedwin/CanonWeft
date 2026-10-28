package backup

import (
	"novelstudio/internal/retention"
	"strings"
	"testing"
)

// This tests the accounting consumer after independent archive verification;
// an unverified envelope alone must never supply a historical budget ceiling.
func TestExpiredScheduleKeepsLatestWeeklyCapAndUnknownHold(t *testing.T) {
	a := weeklyBudgetArchive(t)
	archiveQualificationCell(a, "qualification_schedules", 0, "config", `{"state":"EXPIRED"}`)
	if validateQualificationBudget(a.Data) == nil {
		t.Fatal("unproved expired config accepted")
	}
	id := strings.Repeat("f", 32)
	proof := retention.ExpiredProjection{Summary: retention.EvidenceSummary{Schedule: &retention.ScheduleExpirySummary{BudgetLimit: 100}}}
	evidence := qualificationArchiveEvidence{transients: map[string]map[string]retention.ExpiredProjection{"qualification_schedules.config": {id: proof}}}
	if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
		t.Fatal(err)
	}
	proof.Summary.Schedule.BudgetLimit = 101
	if validateQualificationBudgetWithEvidence(a.Data, evidence) == nil {
		t.Fatal("changed latest ceiling accepted")
	}
	proof.Summary.Schedule.BudgetLimit = 100
	archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
	if validateQualificationBudgetWithEvidence(a.Data, evidence) == nil {
		t.Fatal("expired config erased unknown hold")
	}
}
