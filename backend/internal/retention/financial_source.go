package retention

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
)

// ReceiptSummary preserves settlement identity and amounts without retaining a
// provider's arbitrary reference text. OutputHash is evidence, not a live blob
// reference; its expiry is registered separately from the original receipt.
type ReceiptSummary struct {
	ID           string `json:"id"`
	CallID       string `json:"call_id"`
	Outcome      string `json:"outcome"`
	AmountMicros int64  `json:"amount_micros"`
	OutputHash   string `json:"output_hash"`
}

type ReconciliationSummary struct {
	ID                   string `json:"id"`
	CallID               string `json:"call_id"`
	Revision             int64  `json:"revision"`
	PreviousAmountMicros int64  `json:"previous_amount_micros"`
	ReleasedMicros       int64  `json:"released_micros"`
	AmountMicros         int64  `json:"amount_micros"`
	Currency             string `json:"currency"`
	ContentHash          string `json:"content_hash"`
}

func financialDigest(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func financialJSON(raw []byte, target any) error {
	return strictJSONWithLimit(raw, target, 64<<10)
}

func strictJSONWithLimit(raw []byte, target any, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return ErrInvalidInventory
	}
	shape := json.NewDecoder(bytes.NewReader(raw))
	shape.UseNumber()
	if financialJSONValue(shape, 0) != nil {
		return ErrInvalidInventory
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidInventory
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalidInventory
	}
	return nil
}

func financialJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalidInventory
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidInventory
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return ErrInvalidInventory
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] {
				return ErrInvalidInventory
			}
			keys[key] = true
		}
		if financialJSONValue(decoder, depth+1) != nil {
			return ErrInvalidInventory
		}
	}
	end, err := decoder.Token()
	if err != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return ErrInvalidInventory
	}
	return nil
}

func exactFinancialFields(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != len(fields) {
		return nil, ErrInvalidInventory
	}
	for _, field := range fields {
		value, exists := object[field]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrInvalidInventory
		}
	}
	return object, nil
}

func validReceiptSummary(summary ReceiptSummary) bool {
	return hash64.MatchString(summary.ID) && id32.MatchString(summary.CallID) &&
		slices.Contains([]string{"SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "UNKNOWN"}, summary.Outcome) &&
		summary.AmountMicros >= 0 && summary.AmountMicros <= 1000000000000 &&
		(summary.OutputHash == "" || hash64.MatchString(summary.OutputHash)) &&
		((summary.Outcome != "UNKNOWN" && summary.Outcome != "REJECTED") || summary.AmountMicros == 0)
}

// ReceiptEvidence proves the canonical receipt identity that Settle persisted.
// It has no database or accounting side effects, including for UNKNOWN calls.
func ReceiptEvidence(callID, receiptID string, raw []byte) (ReceiptSummary, error) {
	var parts []json.RawMessage
	var summary ReceiptSummary
	if financialJSON(raw, &parts) != nil || len(parts) != 4 {
		return summary, ErrInvalidInventory
	}
	summary.ID, summary.CallID = receiptID, callID
	var providerReference string
	for i, destination := range []any{&summary.Outcome, &summary.AmountMicros, &providerReference, &summary.OutputHash} {
		if bytes.Equal(bytes.TrimSpace(parts[i]), []byte("null")) || json.Unmarshal(parts[i], destination) != nil {
			return ReceiptSummary{}, ErrInvalidInventory
		}
	}
	if !validReceiptSummary(summary) || len(providerReference) > 200 {
		return ReceiptSummary{}, ErrInvalidInventory
	}
	canonical, err := json.Marshal([]any{summary.Outcome, summary.AmountMicros, providerReference, summary.OutputHash})
	if err != nil || financialDigest([]byte(callID+"\x00"+financialDigest(canonical))) != receiptID {
		return ReceiptSummary{}, ErrInvalidInventory
	}
	return summary, nil
}

// This input mirrors the persisted reconciliation v1 contract and deliberately
// never escapes this file. Free text participates only in the identity check.
type reconciliationSource struct {
	ID             string `json:"id"`
	CallID         string `json:"call_id"`
	Revision       int64  `json:"revision"`
	PreviousAmount int64  `json:"previous_amount_micros"`
	Released       int64  `json:"released_micros"`
	Input          struct {
		OperationID      string `json:"operation_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		Currency         string `json:"currency"`
		Amount           int64  `json:"amount_micros"`
		BillReference    string `json:"bill_reference"`
		Evidence         string `json:"evidence"`
		Reason           string `json:"reason"`
		Confirmed        bool   `json:"confirmed"`
	} `json:"input"`
}

func validReconciliationSummary(summary ReconciliationSummary) bool {
	if !id32.MatchString(summary.ID) || !id32.MatchString(summary.CallID) || !hash64.MatchString(summary.ContentHash) || summary.Revision < 1 || !currencyCode.MatchString(summary.Currency) {
		return false
	}
	for _, amount := range []int64{summary.PreviousAmountMicros, summary.ReleasedMicros, summary.AmountMicros} {
		if amount < 0 || amount > 1000000000000 {
			return false
		}
	}
	return true
}

func ReconciliationEvidence(id, callID string, revision int64, contentHash string, raw []byte) (ReconciliationSummary, error) {
	var source reconciliationSource
	if financialJSON(raw, &source) != nil {
		return ReconciliationSummary{}, ErrInvalidInventory
	}
	fields, err := exactFinancialFields(raw, "id", "call_id", "revision", "previous_amount_micros", "released_micros", "input")
	if err != nil {
		return ReconciliationSummary{}, err
	}
	if _, err := exactFinancialFields(fields["input"], "operation_id", "expected_revision", "currency", "amount_micros", "bill_reference", "evidence", "reason", "confirmed"); err != nil {
		return ReconciliationSummary{}, err
	}
	canonical, err := json.Marshal(source)
	if err != nil || financialDigest(canonical) != contentHash || source.ID != id || source.CallID != callID || source.Revision != revision || !source.Input.Confirmed || source.Input.ExpectedRevision < 0 || source.Input.ExpectedRevision != revision-1 {
		return ReconciliationSummary{}, ErrInvalidInventory
	}
	summary := ReconciliationSummary{ID: id, CallID: callID, Revision: revision, PreviousAmountMicros: source.PreviousAmount, ReleasedMicros: source.Released, AmountMicros: source.Input.Amount, Currency: source.Input.Currency, ContentHash: contentHash}
	if !validReconciliationSummary(summary) {
		return ReconciliationSummary{}, ErrInvalidInventory
	}
	return summary, nil
}

func validateFinancialSource(rule string, raw []byte, summary EvidenceSummary) error {
	if err := validateFinancialRule(rule, summary); err != nil {
		return err
	}
	switch rule {
	case "model_call_receipts.payload":
		proved, err := ReceiptEvidence(summary.CallID, summary.Receipt.ID, raw)
		if err != nil || proved != *summary.Receipt {
			return ErrInvalidInventory
		}
	case "model_reconciliations.payload":
		claimed := summary.Reconciliation
		proved, err := ReconciliationEvidence(claimed.ID, summary.CallID, claimed.Revision, claimed.ContentHash, raw)
		if err != nil || proved != *claimed {
			return ErrInvalidInventory
		}
	}
	return nil
}

func validateFinancialRule(rule string, summary EvidenceSummary) error {
	if rule == "model_call_receipts.payload" || rule == "model_reconciliations.payload" {
		if !currencyCode.MatchString(summary.Currency) || !hash64.MatchString(summary.InputHash) || !slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN", "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED"}, summary.State) {
			return ErrInvalidInventory
		}
	}
	switch rule {
	case "model_call_receipts.payload":
		if summary.Receipt == nil || summary.Reconciliation != nil || summary.CallID != summary.Receipt.CallID {
			return ErrInvalidInventory
		}
	case "model_reconciliations.payload":
		if summary.Reconciliation == nil || summary.Receipt != nil || summary.CallID != summary.Reconciliation.CallID {
			return ErrInvalidInventory
		}
	}
	return nil
}
