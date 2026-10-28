package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLabelExpiryPreservesUniqueIdentityAndBindsScope(t *testing.T) {
	keys := []string{"branches.name", "recipe_steps.step_key", "capability_versions.capability_key", "capability_heads.capability_key", "skill_discovery_candidates.source_path"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			parts := strings.SplitN(key, ".", 2)
			values := map[string]string{}
			for _, col := range TransientColumns(key) {
				values[col] = strings.Repeat("a", 32)
			}
			values[parts[1]] = "PRIVATE_CHARACTER_CANARY"
			row := inventoryRow(parts[0], values)
			summary, err := TransientEvidence(row, parts[1])
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			e := Expiry{ReferenceID: strings.Repeat("b", 64), Rule: key, Revision: 1, PayloadHash: financialDigest([]byte(values[parts[1]])), Storage: Inline, Deadline: at}
			p, err := ProjectAnonymizedLabel(e, []byte(values[parts[1]]), summary, at)
			if err != nil {
				t.Fatal(err)
			}
			oldOwner, _ := RowOwnerKey(row.Table, row.Values)
			row.Values[parts[1]] = retiredLabelPrefix + p.OriginalPayloadHash
			owner, _ := RowOwnerKey(row.Table, row.Values)
			if oldOwner != owner || validateTransientOwner(row, parts[1], p) != nil {
				t.Fatal("identity or proof changed")
			}
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), "PRIVATE_CHARACTER") {
				t.Fatal("label leaked")
			}
			row.Values[parts[1]] = retiredLabelPrefix + strings.Repeat("c", 64)
			if validateTransientOwner(row, parts[1], p) == nil {
				t.Fatal("wrong marker accepted")
			}
		})
	}
}

func TestEventExpiryPreservesOnlySubjectAndRejectsPendingDelivery(t *testing.T) {
	row := inventoryRow("outbox", map[string]string{"id": strings.Repeat("a", 32), "project_id": strings.Repeat("b", 32), "resource_id": strings.Repeat("a", 32), "kind": "qualification.receipt", "created_at": "2026-01-01 00:00:00", "processed_at": "2026-01-01 00:01:00", "payload": `{"run_id":"cccccccccccccccccccccccccccccccc"}`})
	summary, err := TransientEvidence(row, "payload")
	if err != nil || summary.Event == nil || summary.Event.SubjectID != strings.Repeat("c", 32) {
		t.Fatal(summary, err)
	}
	row.Values["processed_at"] = ""
	if _, err := TransientEvidence(row, "payload"); err == nil {
		t.Fatal("pending event erased")
	}
}
