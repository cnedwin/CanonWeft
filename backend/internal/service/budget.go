package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/storage"
)

var (
	ErrBudgetClosed   = errors.New("付费调用尚未开启")
	ErrBudgetExceeded = errors.New("可用预算不足或超过单次调用上限")
)

// All amounts are integer millionths of the selected currency, never floating point.
type Budget struct {
	CurrencyLocked bool   `json:"currency_locked"`
	Currency       string `json:"currency"`
	Limit          int64  `json:"limit_micros"`
	PerCall        int64  `json:"per_call_micros"`
	Spent          int64  `json:"spent_micros"`
	Reserved       int64  `json:"reserved_micros"`
	Enabled        bool   `json:"enabled"`
	Revision       int64  `json:"revision"`
}
type Call struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Reserved        int64  `json:"reserved_micros"`
	Charged         int64  `json:"charged_micros"`
	CancelRequested bool   `json:"cancel_requested"`
	OutputHash      string `json:"output_hash"`
}
type Budgets struct{ Workspace *Workspace }

func budgetTx(ctx context.Context, tx *sql.Tx, workspace string) (Budget, error) {
	var b Budget
	err := tx.QueryRowContext(ctx, "SELECT currency,limit_micros,per_call_micros,spent_micros,reserved_micros,enabled,revision FROM model_budgets WHERE workspace_id=? FOR UPDATE", workspace).Scan(&b.Currency, &b.Limit, &b.PerCall, &b.Spent, &b.Reserved, &b.Enabled, &b.Revision)
	if err == sql.ErrNoRows {
		return Budget{Currency: "USD"}, nil
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM model_calls WHERE workspace_id=?)", workspace).Scan(&b.CurrencyLocked)
	}
	return b, err
}
func (s *Budgets) Read(ctx context.Context, actorID string) (Budget, error) {
	tx, a, err := (&Models{Workspace: s.Workspace}).owner(ctx, actorID)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	return budgetTx(ctx, tx, a.WorkspaceID)
}
func (s *Budgets) Configure(ctx context.Context, actorID string, input Budget) (Budget, error) {
	tx, a, err := (&Models{Workspace: s.Workspace}).owner(ctx, actorID)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	current, err := budgetTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return current, err
	}
	if input.Revision != current.Revision {
		return current, domain.ErrConflict
	}
	if (input.Currency != "USD" && input.Currency != "CNY") || input.Limit < 0 || input.Limit > 1000000000000 || input.PerCall < 0 || input.PerCall > input.Limit || (input.Enabled && input.PerCall == 0) {
		return current, domain.ErrValidation
	}
	if input.Limit < current.Spent+current.Reserved || (current.CurrencyLocked && input.Currency != current.Currency) {
		return current, domain.ErrConflict
	}
	input.Spent = current.Spent
	input.Reserved = current.Reserved
	input.CurrencyLocked = current.CurrencyLocked
	input.Revision++
	_, err = tx.ExecContext(ctx, `INSERT INTO model_budgets(workspace_id,currency,limit_micros,per_call_micros,enabled,revision) VALUES(?,?,?,?,?,?)
 ON DUPLICATE KEY UPDATE currency=VALUES(currency),limit_micros=VALUES(limit_micros),per_call_micros=VALUES(per_call_micros),enabled=VALUES(enabled),revision=VALUES(revision)`, a.WorkspaceID, input.Currency, input.Limit, input.PerCall, input.Enabled, input.Revision)
	if err != nil {
		return current, err
	}
	if err = audit(ctx, tx, a, "budget.configure", a.WorkspaceID); err != nil {
		return current, err
	}
	return input, tx.Commit()
}

func (s *Budgets) Reserve(ctx context.Context, a domain.Actor, project, operation string, amount int64, currency, fingerprint string) (Call, error) {
	var call Call
	if !validName(operation, 120) || amount <= 0 || amount > 1000000000000 || fingerprint == "" {
		return call, domain.ErrValidation
	}
	tx, a, _, err := (&IP{Workspace: s.Workspace}).begin(ctx, a, project, true)
	if err != nil {
		return call, err
	}
	defer tx.Rollback()
	call, err = reserveRequestTx(ctx, tx, a, project, operation, amount, currency, fingerprint, false)
	if err != nil {
		return call, err
	}
	return call, tx.Commit()
}

func modelCallFingerprint(project string, amount int64, currency, fingerprint string) string {
	return models.CallFingerprint(project, amount, currency, fingerprint)
}

func reserveTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, operation string, amount int64, currency, fingerprint string) (Call, error) {
	return reserveRequestTx(ctx, tx, a, project, operation, amount, currency, fingerprint, true)
}

func reserveRequestTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, operation string, amount int64, currency, fingerprint string, internal bool) (Call, error) {
	var call Call
	budget, err := budgetTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return call, err
	}
	hash := modelCallFingerprint(project, amount, currency, fingerprint)
	prior, err := requestIdentityTx(ctx, tx, a, "model_calls", operation, internal)
	if err == nil {
		if prior.Hash != hash {
			return call, domain.ErrConflict
		}
		return callTx(ctx, tx, a.WorkspaceID, project, prior.ID)
	}
	if err != sql.ErrNoRows {
		return call, err
	}
	if !budget.Enabled {
		return call, ErrBudgetClosed
	}
	if currency != budget.Currency {
		return call, domain.ErrValidation
	}
	if amount > budget.PerCall || amount > budget.Limit-budget.Spent-budget.Reserved {
		return call, ErrBudgetExceeded
	}
	call = Call{ID: NewID(), State: "RESERVED", Reserved: amount}
	_, err = tx.ExecContext(ctx, "INSERT INTO model_calls(id,workspace_id,project_id,actor_id,operation_id,input_hash,reserved_micros,state) VALUES(?,?,?,?,?,?,?,'RESERVED')", call.ID, a.WorkspaceID, project, a.ID, operation, hash, amount)
	if err != nil {
		return call, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros+? WHERE workspace_id=?", amount, a.WorkspaceID)
	if err != nil {
		return call, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO model_call_accounting(call_id,currency) VALUES(?,?)", call.ID, currency)
	if err != nil {
		return call, err
	}
	return call, captureCallSourcesTx(ctx, tx, project, call.ID)
}

func callTx(ctx context.Context, tx *sql.Tx, workspace, project, id string) (Call, error) {
	var call Call
	err := tx.QueryRowContext(ctx, "SELECT id,state,reserved_micros,charged_micros,cancel_requested,output_hash FROM model_calls WHERE workspace_id=? AND project_id=? AND id=? FOR UPDATE", workspace, project, id).Scan(&call.ID, &call.State, &call.Reserved, &call.Charged, &call.CancelRequested, &call.OutputHash)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	return call, err
}
func (s *Budgets) Dispatch(ctx context.Context, a domain.Actor, project, id string) error {
	tx, a, _, err := (&IP{Workspace: s.Workspace}).begin(ctx, a, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	budget, err := budgetTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return err
	}
	if !budget.Enabled {
		return ErrBudgetClosed
	}
	var evaluation int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM qualification_calls WHERE call_id=?", id).Scan(&evaluation); err != nil {
		return err
	}
	if evaluation != 0 {
		return domain.ErrForbidden
	} // Evaluation dispatch must pass the generation's frozen authority.
	call, err := callTx(ctx, tx, a.WorkspaceID, project, id)
	if err != nil {
		return err
	}
	if call.State != "RESERVED" || call.CancelRequested {
		return domain.ErrConflict
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='DISPATCHED' WHERE id=?", id)
	if err != nil {
		return err
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Budgets) Cancel(ctx context.Context, a domain.Actor, project, id string) error {
	tx, a, _, err := (&IP{Workspace: s.Workspace}).begin(ctx, a, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = budgetTx(ctx, tx, a.WorkspaceID); err != nil {
		return err
	}
	if err = lockQualificationCallTx(ctx, tx, id); err != nil {
		return err
	}
	call, err := callTx(ctx, tx, a.WorkspaceID, project, id)
	if err != nil {
		return err
	}
	if call.State == "RESERVED" {
		if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
			return err
		}
		if err = accountQualificationCallTx(ctx, tx, id, "CANCELLED", 0, true); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros-? WHERE workspace_id=?", call.Reserved, a.WorkspaceID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='CANCELLED',cancel_requested=TRUE WHERE id=?", id)
	} else if call.State == "DISPATCHED" || call.State == "UNKNOWN" {
		_, err = tx.ExecContext(ctx, "UPDATE model_calls SET cancel_requested=TRUE WHERE id=?", id)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "model.cancel", id); err != nil {
		return err
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Settle is an internal receipt operation: fees remain recordable after access revocation.
// It does not adopt or publish the returned object.
type settlementOutput struct {
	prepare func(context.Context, *sql.Tx) ([]byte, error)
	store   storage.BlobStore
	data    []byte
	outcome string
}

func (s *Budgets) Settle(ctx context.Context, id, outcome string, amount int64, receipt, outputHash string) error {
	return s.settle(ctx, id, outcome, amount, receipt, outputHash, nil)
}

// Output persistence shares the receipt transaction. A storage failure must
// not discard known usage or commit a false PRESENT file registration.
func (s *Budgets) settle(ctx context.Context, id, outcome string, amount int64, receipt, outputHash string, output *settlementOutput) error {
	if amount < 0 || amount > 1000000000000 || len(receipt) > 200 || len(outputHash) > 64 {
		return domain.ErrValidation
	}
	if outcome != "SUCCEEDED" && outcome != "INVALID_OUTPUT" && outcome != "REJECTED" && outcome != "UNKNOWN" {
		return domain.ErrValidation
	}
	if (outcome == "REJECTED" || outcome == "UNKNOWN") && amount != 0 {
		return domain.ErrValidation
	}
	var workspace, project string
	err := s.Workspace.DB.QueryRowContext(ctx, "SELECT workspace_id,project_id FROM model_calls WHERE id=?", id).Scan(&workspace, &project)
	if err != nil {
		return err
	}
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, project); err != nil {
		return err
	}
	if _, err = budgetTx(ctx, tx, workspace); err != nil {
		return err
	}
	if err = lockQualificationCallTx(ctx, tx, id); err != nil {
		return err
	}
	call, err := callTx(ctx, tx, workspace, project, id)
	if err != nil {
		return err
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return err
	}
	if output != nil {
		if outcome != "SUCCEEDED" && outcome != "INVALID_OUTPUT" {
			return domain.ErrValidation
		}
		if _, err := tx.ExecContext(ctx, "SAVEPOINT output_material"); err != nil {
			return err
		}
		data := output.data
		var writeErr error
		if output.prepare != nil {
			data, writeErr = output.prepare(ctx, tx)
		}
		var savedHash string
		if writeErr == nil {
			object, objectErr := objectwriter.Write(ctx, tx, output.store, project, data)
			writeErr, savedHash = objectErr, object.Hash
		}
		if writeErr != nil {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT output_material"); err != nil {
				return err
			}
			outcome, outputHash = "INVALID_OUTPUT", ""
		} else {
			outputHash = savedHash
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT output_material"); err != nil {
			return err
		}
		output.outcome = outcome
	}
	encoded, _ := json.Marshal([]any{outcome, amount, receipt, outputHash})
	hash := digest(string(encoded))
	recorded, err := tx.ExecContext(ctx, "INSERT IGNORE INTO model_call_receipts(id,call_id,payload) VALUES(?,?,?)", digest(id+"\x00"+hash), id, encoded)
	if err != nil {
		return err
	}
	var previous string
	if err = tx.QueryRowContext(ctx, "SELECT settlement_hash FROM model_calls WHERE id=?", id).Scan(&previous); err != nil {
		return err
	}
	if previous != "" {
		if previous == hash {
			if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
				return err
			}
			return tx.Commit()
		}
		added, countErr := recorded.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if added > 0 {
			if _, err = tx.ExecContext(ctx, "UPDATE model_call_accounting SET revision=revision+1 WHERE call_id=?", id); err != nil {
				return err
			}
			if err = qualificationCallReceiptOutboxTx(ctx, tx, id); err != nil {
				return err
			}
		}
		if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return domain.ErrConflict
	}
	if call.State != "DISPATCHED" && call.State != "UNKNOWN" {
		return domain.ErrConflict
	}
	if outcome == "UNKNOWN" {
		_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='UNKNOWN' WHERE id=?", id)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros-?,spent_micros=spent_micros+? WHERE workspace_id=?", call.Reserved, amount, workspace)
		if err != nil {
			return err
		}
		if call.CancelRequested {
			outcome = "CANCELLED"
			outputHash = ""
		}
		_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state=?,charged_micros=?,provider_receipt=?,output_hash=?,settlement_hash=? WHERE id=?", outcome, amount, receipt, outputHash, hash, id)
	}
	if err != nil {
		return err
	}
	if err = accountQualificationCallTx(ctx, tx, id, outcome, amount, outcome != "UNKNOWN"); err != nil {
		return err
	}
	if outcome != "UNKNOWN" || call.State != "UNKNOWN" {
		if _, err = tx.ExecContext(ctx, "UPDATE model_call_accounting SET revision=revision+1 WHERE call_id=?", id); err != nil {
			return err
		}
	}
	if err = captureCallSourcesTx(ctx, tx, project, id); err != nil {
		return err
	}
	return tx.Commit()
}
