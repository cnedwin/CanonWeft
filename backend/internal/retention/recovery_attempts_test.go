package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecoveryJournalRejectsUnboundedOrAuthorPayloads(t *testing.T) {
	now := time.Now().UTC()
	base := RecoveryAttempt{Target: strings.Repeat("a", 32), Project: strings.Repeat("b", 32), Actor: strings.Repeat("c", 32), Namespace: strings.Repeat("d", 64), State: "WRITING", Created: now, Updated: now, Objects: []PhysicalObject{{Hash: strings.Repeat("e", 64), Size: 12}}}
	if err := ValidateRecoveryAttempt(base); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(base.Objects)
	if _, err := ParseRecoveryObjects(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"null", `[ {"Hash":"x","Size":0} ]`, `[{"Hash":"x","Size":0,"prompt":"erased author text"}]`, `[{"Hash":"x","Size":0,"Size":1}]`} {
		if _, err := ParseRecoveryObjects(bad); err == nil {
			t.Fatal("noncanonical or extra payload accepted", bad)
		}
	}
	for _, mutate := range []func(*RecoveryAttempt){
		func(a *RecoveryAttempt) { a.Target = a.Project },
		func(a *RecoveryAttempt) { a.State = "DELETE_ANYTHING" },
		func(a *RecoveryAttempt) { a.Updated = a.Created.Add(-time.Second) },
		func(a *RecoveryAttempt) { a.Objects = append(a.Objects, a.Objects[0]) },
		func(a *RecoveryAttempt) {
			a.Objects = []PhysicalObject{{Hash: strings.Repeat("e", 64), Size: 33 << 20}}
		},
	} {
		copy := base
		mutate(&copy)
		if ValidateRecoveryAttempt(copy) == nil {
			t.Fatal("unsafe recovery journal accepted", copy)
		}
	}
}
