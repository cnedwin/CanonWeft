package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"slices"
	"strings"
	"time"
)

type ReconciliationInput struct {
	OperationID      string `json:"operation_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Currency         string `json:"currency"`
	Amount           int64  `json:"amount_micros"`
	BillReference    string `json:"bill_reference"`
	Evidence         string `json:"evidence"`
	Reason           string `json:"reason"`
	Confirmed        bool   `json:"confirmed"`
}
type Reconciliation struct {
	ID             string              `json:"id"`
	CallID         string              `json:"call_id"`
	Revision       int64               `json:"revision"`
	PreviousAmount int64               `json:"previous_amount_micros"`
	Released       int64               `json:"released_micros"`
	Input          ReconciliationInput `json:"input"`
	RetentionState string              `json:"retention_state,omitempty"`
	ExpiredAt      *time.Time          `json:"expired_at,omitempty"`
}
type AccountedCall struct {
	Call
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	Currency        string `json:"currency"`
	Revision        int64  `json:"revision"`
	ProviderReceipt string `json:"provider_receipt"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	CanReconcile    bool   `json:"can_reconcile"`
	ReceiptCount    int    `json:"receipt_count"`
}

func reconciliationTx(ctx context.Context, tx *sql.Tx, workspace, id string) (Reconciliation, error) {
	var item Reconciliation
	source := financialSourceRecord{ID: id}
	err := tx.QueryRowContext(ctx, "SELECT r.payload,r.content_hash,r.call_id,r.revision,c.project_id FROM model_reconciliations r JOIN model_calls c ON c.id=r.call_id WHERE r.id=? AND c.workspace_id=?", id, workspace).Scan(&source.Raw, &source.Hash, &source.CallID, &source.Revision, &source.ProjectID)
	if err == sql.ErrNoRows {
		return item, domain.ErrForbidden
	}
	if err != nil {
		return item, err
	}
	return readReconciliationTx(ctx, tx, source)
}

// Reconciliation records the owner's invoice evidence. It neither contacts the
// provider nor creates a generation, refunds money, or adopts model output.
func (s *Budgets) Reconcile(ctx context.Context, actorID, id string, input ReconciliationInput) (Reconciliation, error) {
	var result Reconciliation
	if !input.Confirmed || !validName(input.OperationID, 120) || input.ExpectedRevision < 0 || input.Amount < 0 || input.Amount > 1000000000000 || !validName(input.BillReference, 200) || !validName(input.Reason, 1000) || len(strings.TrimSpace(input.Evidence)) < 10 || len(input.Evidence) > 8000 {
		return result, domain.ErrValidation
	}
	raw, _ := json.Marshal(input)
	fingerprint := digest("model.reconcile\x00" + id + "\x00" + string(raw))
	tx, a, err := (&Models{Workspace: s.Workspace}).owner(ctx, actorID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, resultID string
	prior, resultID, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if prior != fingerprint {
			return result, domain.ErrConflict
		}
		return reconciliationTx(ctx, tx, a.WorkspaceID, resultID)
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	var project string
	if err = tx.QueryRowContext(ctx, "SELECT project_id FROM model_calls WHERE id=? AND workspace_id=?", id, a.WorkspaceID).Scan(&project); err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	} else if err != nil {
		return result, err
	}
	if err = lockReceiptProjectTx(ctx, tx, project); err != nil {
		return result, err
	}
	budget, err := budgetTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return result, err
	}
	if err = lockQualificationCallTx(ctx, tx, id); err != nil {
		return result, err
	}
	call, err := callTx(ctx, tx, a.WorkspaceID, project, id)
	if err != nil {
		return result, err
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return result, err
	}
	var revision int64
	var currency, settlement string
	if err = tx.QueryRowContext(ctx, "SELECT a.currency,a.revision,c.settlement_hash FROM model_call_accounting a JOIN model_calls c ON c.id=a.call_id WHERE a.call_id=? FOR UPDATE", id).Scan(&currency, &revision, &settlement); err != nil {
		return result, err
	}
	if revision != input.ExpectedRevision {
		return result, domain.ErrConflict
	}
	if currency != input.Currency || currency != budget.Currency {
		return result, domain.ErrValidation
	}
	if !slices.Contains([]string{"UNKNOWN", "RECONCILED", "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED"}, call.State) || (call.State == "CANCELLED" && settlement == "") {
		return result, domain.ErrConflict
	}
	release := int64(0)
	if call.State == "UNKNOWN" {
		release = call.Reserved
	}
	if budget.Reserved < release || budget.Spent < call.Charged {
		return result, domain.ErrNotReady
	}
	result = Reconciliation{ID: NewID(), CallID: id, Revision: revision + 1, PreviousAmount: call.Charged, Released: release, Input: input}
	payload, _ := json.Marshal(result)
	hash := digest(string(payload))
	if _, err = tx.ExecContext(ctx, "INSERT INTO model_reconciliations(id,call_id,actor_id,revision,payload,content_hash) VALUES(?,?,?,?,?,?)", result.ID, id, a.ID, result.Revision, payload, hash); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE model_budgets SET spent_micros=spent_micros+?,reserved_micros=reserved_micros-? WHERE workspace_id=?", input.Amount-call.Charged, release, a.WorkspaceID); err != nil {
		return result, err
	}
	state := call.State
	if state == "UNKNOWN" {
		state = "RECONCILED"
	}
	if settlement == "" {
		settlement = digest("manual-reconciliation\x00" + result.ID)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE model_calls SET state=?,charged_micros=?,settlement_hash=? WHERE id=?", state, input.Amount, settlement, id); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE model_call_accounting SET revision=? WHERE call_id=?", result.Revision, id); err != nil {
		return result, err
	}
	if err = accountQualificationCallTx(ctx, tx, id, state, input.Amount, true); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'model.reconcile',?,?)", a.ID, input.OperationID, fingerprint, result.ID); err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "model.reconcile", result.ID); err != nil {
		return result, err
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Budgets) Calls(ctx context.Context, actorID, after string) ([]AccountedCall, error) {
	tx, a, err := (&Models{Workspace: s.Workspace}).owner(ctx, actorID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	afterTime := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	if after != "" {
		err = tx.QueryRowContext(ctx, "SELECT created_at FROM model_calls WHERE id=? AND workspace_id=?", after, a.WorkspaceID).Scan(&afterTime)
		if err == sql.ErrNoRows {
			return nil, domain.ErrForbidden
		}
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.state,c.reserved_micros,c.charged_micros,c.cancel_requested,c.output_hash,c.project_id,p.name,a.currency,a.revision,c.provider_receipt,c.settlement_hash,
 COALESCE((SELECT JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.config.provider')) FROM generation_tasks g WHERE g.call_id=c.id LIMIT 1),''),
 COALESCE((SELECT JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.config.model')) FROM generation_tasks g WHERE g.call_id=c.id LIMIT 1),''),
 (SELECT COUNT(*) FROM model_call_receipts r WHERE r.call_id=c.id)
 FROM model_calls c JOIN projects p ON p.id=c.project_id JOIN model_call_accounting a ON a.call_id=c.id
 WHERE c.workspace_id=? AND (?='' OR c.created_at<? OR (c.created_at=? AND c.id<?)) ORDER BY c.created_at DESC,c.id DESC LIMIT 50`, a.WorkspaceID, after, afterTime, afterTime, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AccountedCall{}
	for rows.Next() {
		var item AccountedCall
		var settlement string
		if err = rows.Scan(&item.ID, &item.State, &item.Reserved, &item.Charged, &item.CancelRequested, &item.OutputHash, &item.ProjectID, &item.ProjectName, &item.Currency, &item.Revision, &item.ProviderReceipt, &settlement, &item.Provider, &item.Model, &item.ReceiptCount); err != nil {
			return nil, err
		}
		item.CanReconcile = slices.Contains([]string{"UNKNOWN", "RECONCILED", "SUCCEEDED", "INVALID_OUTPUT", "REJECTED"}, item.State) || (item.State == "CANCELLED" && settlement != "")
		result = append(result, item)
	}
	return result, rows.Err()
}

type ReconciliationHistory struct {
	Adjustments []Reconciliation  `json:"adjustments"`
	Receipts    []json.RawMessage `json:"receipts"`
}

func (s *Budgets) History(ctx context.Context, actorID, id string) (ReconciliationHistory, error) {
	result := ReconciliationHistory{Adjustments: []Reconciliation{}, Receipts: []json.RawMessage{}}
	tx, a, err := (&Models{Workspace: s.Workspace}).owner(ctx, actorID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var project string
	if err = tx.QueryRowContext(ctx, "SELECT project_id FROM model_calls WHERE id=? AND workspace_id=?", id, a.WorkspaceID).Scan(&project); err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	} else if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,payload,content_hash FROM model_reconciliations WHERE call_id=? ORDER BY revision", id)
	if err != nil {
		return result, err
	}
	adjustments := []financialSourceRecord{}
	for rows.Next() {
		item := financialSourceRecord{CallID: id, ProjectID: project}
		if err = rows.Scan(&item.ID, &item.Revision, &item.Raw, &item.Hash); err != nil {
			rows.Close()
			return result, err
		}
		adjustments = append(adjustments, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, source := range adjustments {
		item, err := readReconciliationTx(ctx, tx, source)
		if err != nil {
			return result, err
		}
		result.Adjustments = append(result.Adjustments, item)
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,payload FROM model_call_receipts WHERE call_id=? ORDER BY created_at,id", id)
	if err != nil {
		return result, err
	}
	receipts := []financialSourceRecord{}
	for rows.Next() {
		item := financialSourceRecord{CallID: id, ProjectID: project}
		if err = rows.Scan(&item.ID, &item.Raw); err != nil {
			rows.Close()
			return result, err
		}
		receipts = append(receipts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, source := range receipts {
		item, err := readReceiptTx(ctx, tx, source)
		if err != nil {
			return result, err
		}
		result.Receipts = append(result.Receipts, item)
	}
	return result, nil
}
