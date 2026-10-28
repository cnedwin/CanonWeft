package retention

import (
	"slices"
	"strconv"
)

func (index *SourceIndex) sourceRow(table string, primary map[string]string) (SourceRow, error) {
	key, err := RowOwnerKey(table, primary)
	if err != nil || index == nil {
		return SourceRow{}, ErrSourceOwnership
	}
	row, exists := index.rows[table][key]
	if !exists && index.loadSource != nil {
		row, err = index.loadSource(table, primary)
		if err != nil {
			return SourceRow{}, err
		}
		validated, err := NewSourceIndex([]SourceRow{row})
		if err != nil {
			return SourceRow{}, err
		}
		row, exists = validated.rows[table][key]
		if exists {
			index.rows[table][key] = row
		}
	}
	if !exists {
		return SourceRow{}, ErrSourceOwnership
	}
	return row, nil
}

func financialInteger(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != raw {
		return 0, ErrInvalidInventory
	}
	return value, nil
}

// FinancialSummary derives evidence from the frozen source snapshot, including
// the real call and its currency/accounting revision. A caller cannot supply
// its own amounts or infer a currency from the workspace's current defaults.
func (index *SourceIndex) FinancialSummary(table string, primary map[string]string) (EvidenceSummary, error) {
	var summary EvidenceSummary
	if !slices.Contains([]string{"model_calls", "model_call_accounting", "model_call_receipts", "model_reconciliations", "qualification_calls"}, table) {
		return summary, ErrInvalidInventory
	}
	row, err := index.sourceRow(table, primary)
	if err != nil {
		return summary, err
	}
	owner, err := index.Owner(table, primary)
	if err != nil || owner.Shared {
		return summary, ErrSourceOwnership
	}
	callID := row.Values["call_id"]
	if table == "model_calls" {
		callID = row.Values["id"]
	}
	call, err := index.sourceRow("model_calls", map[string]string{"id": callID})
	if err != nil {
		return summary, err
	}
	account, err := index.sourceRow("model_call_accounting", map[string]string{"call_id": callID})
	if err != nil {
		return summary, err
	}
	accountOwner, err := index.Owner("model_call_accounting", account.Values)
	if err != nil || accountOwner != owner {
		return summary, ErrSourceOwnership
	}
	summary.CallID = callID
	summary.Currency = account.Values["currency"]
	summary.State = call.Values["state"]
	summary.InputHash = call.Values["input_hash"]
	summary.OutputHash = call.Values["output_hash"]
	if !hash64.MatchString(summary.InputHash) || !slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN", "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED"}, summary.State) {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	for raw, destination := range map[string]*int64{
		"reserved_micros": &summary.ReservedMicros, "charged_micros": &summary.ChargedMicros,
	} {
		*destination, err = financialInteger(call.Values[raw])
		if err != nil || *destination > 1000000000000 {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	}
	summary.AccountingRevision, err = financialInteger(account.Values["revision"])
	if err != nil {
		return EvidenceSummary{}, err
	}
	if table == "model_call_receipts" {
		if proof, expired := index.expiredPayload(row); expired {
			summary.Receipt = proof.Summary.Receipt
		} else {
			receipt, err := ReceiptEvidence(callID, row.Values["id"], []byte(row.Values["payload"]))
			if err != nil {
				return EvidenceSummary{}, err
			}
			summary.Receipt = &receipt
		}
	} else if table == "model_reconciliations" {
		revision, err := financialInteger(row.Values["revision"])
		if err != nil || revision > summary.AccountingRevision {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		if proof, expired := index.expiredPayload(row); expired {
			summary.Reconciliation = proof.Summary.Reconciliation
		} else {
			reconciliation, err := ReconciliationEvidence(row.Values["id"], callID, revision, row.Values["content_hash"], []byte(row.Values["payload"]))
			if err != nil {
				return EvidenceSummary{}, err
			}
			summary.Reconciliation = &reconciliation
		}
	}
	if err := index.qualificationAccounting(&summary, owner); err != nil {
		return EvidenceSummary{}, err
	}
	if table == "qualification_calls" && summary.QualificationAccounting == nil {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	if err := ValidateSummary(summary, Inline); err != nil {
		return EvidenceSummary{}, err
	}
	return summary, nil
}
