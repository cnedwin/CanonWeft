package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func TestIndexedRetentionFinancialOwnerPreparesWholeAndNestedExpiryTogether(t *testing.T) {
	w, p, u, call := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	output := strings.Repeat("e", 64)
	payload := `["SUCCEEDED",37,"PRIVATE_PROVIDER_REFERENCE","` + output + `"]`
	receipt := digest(call + "\x00" + digest(payload))
	row := func(table string, values map[string]string) retention.SourceRow {
		spec, _ := retention.OwnerSpec(table)
		result := retention.SourceRow{Table: table, Values: map[string]string{}}
		for _, column := range spec.Columns {
			result.Values[column.Column] = ""
		}
		for key, value := range values {
			result.Values[key] = value
		}
		return result
	}
	index, err := retention.NewSourceIndex([]retention.SourceRow{
		row("workspaces", map[string]string{"id": w}), row("projects", map[string]string{"id": p, "workspace_id": w}), row("users", map[string]string{"id": u, "workspace_id": w}),
		row("model_calls", map[string]string{"id": call, "project_id": p, "workspace_id": w, "actor_id": u, "input_hash": strings.Repeat("f", 64), "output_hash": output, "state": "SUCCEEDED", "reserved_micros": "80", "charged_micros": "37"}),
		row("model_call_accounting", map[string]string{"call_id": call, "currency": "USD", "revision": "1"}),
		row("model_call_receipts", map[string]string{"id": receipt, "call_id": call, "payload": payload}),
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := map[string]string{"id": receipt}
	key, _ := retention.RowOwnerKey("model_call_receipts", primary)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	items := []retention.Expiry{}
	for _, value := range []struct {
		field, raw string
		storage    retention.StorageKind
	}{{"payload", payload, retention.Inline}, {"payload.output_hash", output, retention.Blob}} {
		id, _ := retention.ReferenceID(p, "model_call_receipts", key, value.field)
		items = append(items, retention.Expiry{ReferenceID: id, Rule: "model_call_receipts." + value.field, PayloadHash: digest(value.raw), Revision: 1, Deadline: now, Storage: value.storage})
	}
	prepared, err := prepareIndexedRetentionOwner(index, p, "model_call_receipts", primary, items, now)
	if err != nil || len(prepared.Evidence) != 2 || len(prepared.Updates) != 1 || prepared.OriginalRowHash == "" {
		t.Fatal("incomplete atomic preparation", err)
	}
	if strings.Contains(prepared.Updates["payload"], "PRIVATE_") {
		t.Fatal("raw receipt survived")
	}
	// The real coordinator must preserve input ordering even though the typed
	// owner preparer sorts its independent evidence by reference identity.
	for range 2 {
		batch := retention.ExpiryOwnerBatch{Index: index, Source: retention.SourceKey{Table: "model_call_receipts", Primary: primary}, Items: items}
		proofs, err := prepareCleanupBatch(batch, p, now)
		if err != nil || len(proofs) != 2 {
			t.Fatal("coordinator lost structured owner proofs", err)
		}
		for i, proof := range proofs {
			if proof.ReferenceID != items[i].ReferenceID {
				t.Fatal("proof/input order mismatch")
			}
		}
		items[0], items[1] = items[1], items[0]
	}
	var saved retention.ExpiredProjection
	if json.Unmarshal([]byte(prepared.Updates["payload"]), &saved) != nil || saved.Summary.Receipt == nil || saved.Summary.Receipt.OutputHash != output || saved.Summary.ChargedMicros != 37 {
		t.Fatal("nested original reference or fee was lost")
	}
	items[1].PayloadHash = strings.Repeat("1", 64)
	bad, err := prepareIndexedRetentionOwner(index, p, "model_call_receipts", primary, items, now)
	if err == nil || len(bad.Updates) != 0 || len(bad.Evidence) != 0 {
		t.Fatal("partial update returned after one invalid reference")
	}
	if _, err := prepareIndexedRetentionOwner(index, strings.Repeat("2", 32), "model_call_receipts", primary, items[:1], now); err == nil {
		t.Fatal("wrong project claimed financial owner")
	}
}
