package qualification

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQualificationScheduleReferenceIsServerOnly(t *testing.T) {
	input := QualificationInput{OperationID: "operation", Schedule: &ScheduleRef{ID: "server-only-grant", Revision: 3, Week: "2026-W43"}}
	raw, err := json.Marshal(input)
	if err != nil || strings.Contains(string(raw), "Schedule") || strings.Contains(string(raw), "server-only-grant") {
		t.Fatal("internal schedule leaked into the wire contract", err)
	}
	input.Schedule = nil
	for _, field := range []string{"schedule", "Schedule"} {
		decoder := json.NewDecoder(strings.NewReader(`{"` + field + `":{"ID":"forged","Revision":3,"Week":"2026-W43"}}`))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) == nil || input.Schedule != nil {
			t.Fatal("JSON installed a server-side schedule reference", field)
		}
	}
}
