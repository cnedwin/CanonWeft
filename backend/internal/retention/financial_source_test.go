package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sourceDigest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func TestFinancialReceiptProjectionPreservesIdentityWithoutProviderText(t *testing.T) {
	call := strings.Repeat("a", 32)
	output := strings.Repeat("b", 64)
	raw := `["SUCCEEDED",31,"PRIVATE_PROVIDER_REFERENCE","` + output + `"]`
	id := sourceDigest(call + "\x00" + sourceDigest(raw))
	evidence, err := ReceiptEvidence(call, id, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ID != id || evidence.CallID != call || evidence.AmountMicros != 31 || evidence.OutputHash != output || evidence.Outcome != "SUCCEEDED" {
		t.Fatal("receipt identity changed", evidence)
	}
	saved, _ := json.Marshal(evidence)
	if strings.Contains(string(saved), "PRIVATE_PROVIDER_REFERENCE") {
		t.Fatal("provider text retained")
	}
	// MySQL JSON storage may add whitespace. Verify the receipt's original
	// canonical identity, not a checksum of the database's formatting.
	spaced := strings.ReplaceAll(raw, ",", ", ")
	again, err := ReceiptEvidence(call, id, []byte(spaced))
	if err != nil || again != evidence {
		t.Fatal("SQL JSON formatting changed receipt identity", err)
	}
	for _, bad := range []string{`["UNKNOWN",31,"private",""]`, `["UNKNOWN",0,"private",""]`, `["SUCCEEDED",31.5,"private",""]`, `["SUCCEEDED",31,"private","bad"]`, `["SUCCEEDED",31,"private","",{}]`} {
		if _, err := ReceiptEvidence(call, id, []byte(bad)); err == nil {
			t.Fatal("malformed or substituted receipt accepted")
		}
	}
}

func TestFinancialReconciliationProjectionRetainsLedgerAmountsOnly(t *testing.T) {
	id, call := strings.Repeat("a", 32), strings.Repeat("b", 32)
	raw := `{"id":"` + id + `","call_id":"` + call + `","revision":2,"previous_amount_micros":0,"released_micros":100,"input":{"operation_id":"PRIVATE_OP","expected_revision":1,"currency":"USD","amount_micros":37,"bill_reference":"PRIVATE_BILL","evidence":"PRIVATE_EVIDENCE","reason":"PRIVATE_REASON","confirmed":true}}`
	evidence, err := ReconciliationEvidence(id, call, 2, sourceDigest(raw), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.AmountMicros != 37 || evidence.PreviousAmountMicros != 0 || evidence.ReleasedMicros != 100 || evidence.Revision != 2 || evidence.Currency != "USD" {
		t.Fatal("ledger amounts changed", evidence)
	}
	saved, _ := json.Marshal(evidence)
	if strings.Contains(string(saved), "PRIVATE_") {
		t.Fatal("free text retained")
	}
	for _, change := range []struct{ before, after string }{{`"revision":2`, `"revision":3`}, {`"confirmed":true`, `"confirmed":false`}, {`"amount_micros":37`, `"amount_micros":-1`}, {`"currency":"USD"`, `"currency":"private"`}, {`"expected_revision":1`, `"expected_revision":0`}} {
		bad := strings.Replace(raw, change.before, change.after, 1)
		if _, err := ReconciliationEvidence(id, call, 2, sourceDigest(bad), []byte(bad)); err == nil {
			t.Fatal("unproven reconciliation accepted", change)
		}
	}
	for _, altered := range []string{
		strings.Replace(raw, `"id":`, `"ID":`, 1),
		strings.Replace(raw, `"revision":2`, `"revision":2,"revision":2`, 1),
	} {
		if _, err := ReconciliationEvidence(id, call, 2, sourceDigest(raw), []byte(altered)); err == nil {
			t.Fatal("ambiguous financial JSON accepted")
		}
	}
}

func TestFinancialSummaryRemainsBoundToItsCall(t *testing.T) {
	call := strings.Repeat("a", 32)
	receipt := &ReceiptSummary{ID: strings.Repeat("b", 64), CallID: call, Outcome: "UNKNOWN"}
	if ValidateSummary(EvidenceSummary{CallID: call, Receipt: receipt}, Inline) != nil {
		t.Fatal("valid unknown evidence rejected")
	}
	if ValidateSummary(EvidenceSummary{CallID: strings.Repeat("c", 32), Receipt: receipt}, Inline) == nil {
		t.Fatal("receipt moved between calls")
	}
}

func TestFinancialSourceSummaryBindsActualCallAndAccounting(t *testing.T) {
	rows := sourceOwnershipRows()
	call := strings.Repeat("7", 32)
	raw := `["UNKNOWN",0,"PRIVATE_PROVIDER_REFERENCE",""]`
	receipt := sourceDigest(call + "\x00" + sourceDigest(raw))
	rows = append(rows,
		inventoryRow("model_calls", map[string]string{"id": call, "project_id": strings.Repeat("2", 32), "workspace_id": strings.Repeat("1", 32), "actor_id": strings.Repeat("4", 32), "reserved_micros": "80", "charged_micros": "0", "state": "UNKNOWN", "input_hash": strings.Repeat("b", 64)}),
		inventoryRow("model_call_accounting", map[string]string{"call_id": call, "currency": "USD", "revision": "1"}),
		inventoryRow("model_call_receipts", map[string]string{"id": receipt, "call_id": call, "payload": raw}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	key := map[string]string{"id": receipt}
	summary, err := index.FinancialSummary("model_call_receipts", key)
	if err != nil || summary.CallID != call || summary.Receipt == nil || summary.State != "UNKNOWN" || summary.ReservedMicros != 80 || summary.ChargedMicros != 0 || summary.Currency != "USD" || summary.AccountingRevision != 1 {
		t.Fatal("source accounting changed", summary, err)
	}
	ownerKey, _ := RowOwnerKey("model_call_receipts", key)
	refID, _ := ReferenceID(strings.Repeat("2", 32), "model_call_receipts", ownerKey, "payload")
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	projection, err := ProjectExpired(Expiry{ReferenceID: refID, Rule: "model_call_receipts.payload", PayloadHash: sourceDigest(raw), Revision: 1, Deadline: now, Storage: Inline}, []byte(raw), summary, now)
	if err != nil {
		t.Fatal("source cannot produce an expiry proof", err)
	}
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("private receipt leaked through expiry proof")
	}
	for _, badSummary := range []EvidenceSummary{{}, {CallID: call, Receipt: &ReceiptSummary{ID: receipt, CallID: call, Outcome: "SUCCEEDED", AmountMicros: 99}}} {
		if _, err := ProjectExpired(Expiry{ReferenceID: refID, Rule: "model_call_receipts.payload", PayloadHash: sourceDigest(raw), Revision: 1, Deadline: now, Storage: Inline}, []byte(raw), badSummary, now); err == nil {
			t.Fatal("financial source erased with missing or forged receipt proof")
		}
	}
	rows[len(rows)-2].Values["currency"] = "unrecognized-private-currency"
	bad, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.FinancialSummary("model_call_receipts", key); err == nil {
		t.Fatal("untyped accounting promoted to evidence")
	}
}
