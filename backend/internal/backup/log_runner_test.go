package backup

import (
	"testing"
	"time"
)

func TestLogRunnerCadenceUsesSourceObservationAndDailyBaseline(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		baseline, point time.Time
		force           bool
		want            string
	}{
		{"empty", time.Time{}, time.Time{}, false, "baseline"},
		{"fresh", now.Add(-time.Hour), now.Add(-time.Minute), false, ""},
		{"ten_minutes", now.Add(-time.Hour), now.Add(-10 * time.Minute), false, "collect"},
		{"manual", now.Add(-time.Hour), now.Add(-time.Minute), true, "collect"},
		{"daily", now.Add(-24 * time.Hour), now.Add(-time.Minute), false, "rollover"},
		{"future", now.Add(time.Hour), now.Add(time.Hour), false, "clock_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logCaptureAction(tc.baseline, tc.point, now, tc.force); got != tc.want {
				t.Fatalf("action=%s want=%s", got, tc.want)
			}
		})
	}
}
