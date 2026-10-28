package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type financialSourceRecord struct {
	ID, CallID, ProjectID, Hash string
	Revision                    int64
	Raw                         []byte
}

// Expired history is readable only with the independently stored reference and
// evidence. A client-created object containing state=EXPIRED is never enough.
func expiredFinancialPayloadTx(ctx context.Context, tx *sql.Tx, table string, source financialSourceRecord) (*retention.ExpiredProjection, error) {
	var header struct {
		State string `json:"state"`
	}
	if json.Unmarshal(source.Raw, &header) != nil || header.State != "EXPIRED" {
		return nil, nil
	}
	key, err := retention.RowOwnerKey(table, map[string]string{"id": source.ID})
	if err != nil {
		return nil, domain.ErrNotReady
	}
	owner := retention.StoredOwner{ProjectID: source.ProjectID, Table: table, Key: key, Field: "payload", CallID: source.CallID, ContentHash: source.Hash, SourceRevision: strconv.FormatInt(source.Revision, 10), Value: source.Raw}
	return storedExpiredProjectionTx(ctx, tx, owner)
}

func storedExpiredProjectionTx(ctx context.Context, tx *sql.Tx, owner retention.StoredOwner) (*retention.ExpiredProjection, error) {
	id, err := retention.ReferenceID(owner.ProjectID, owner.Table, owner.Key, owner.Field)
	if err != nil {
		return nil, domain.ErrNotReady
	}
	var ref retention.StoredReference
	var evidence []byte
	var evidenceProject, originalHash string
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.project_id,r.owner_table,r.owner_key,r.field_key,r.storage_kind,r.state,r.revision,r.payload_hash,COALESCE(r.object_hash,''),r.deadline,r.expired_at,e.reference_revision,e.projection_hash,e.expired_at,e.projection,e.project_id,e.original_payload_hash
 FROM retention_references r JOIN retention_evidence e ON e.reference_id=r.id AND e.reference_revision=r.revision WHERE r.id=?`, id).Scan(
		&ref.ID, &ref.ProjectID, &ref.Table, &ref.OwnerKey, &ref.Field, &ref.Storage, &ref.State, &ref.Revision, &ref.PayloadHash, &ref.ObjectHash, &ref.Deadline, &ref.ExpiredAt, &ref.EvidenceRevision, &ref.ProjectionHash, &ref.EvidenceExpiredAt, &evidence, &evidenceProject, &originalHash)
	if err != nil || evidenceProject != owner.ProjectID || originalHash != ref.PayloadHash {
		return nil, domain.ErrNotReady
	}
	projection, err := retention.ValidateStoredProjection(ref, owner, evidence)
	if err != nil {
		return nil, domain.ErrNotReady
	}
	return &projection, nil
}

func readReconciliationTx(ctx context.Context, tx *sql.Tx, source financialSourceRecord) (Reconciliation, error) {
	projection, err := expiredFinancialPayloadTx(ctx, tx, "model_reconciliations", source)
	if err != nil {
		return Reconciliation{}, err
	}
	if projection != nil {
		proof := projection.Summary.Reconciliation
		return Reconciliation{ID: proof.ID, CallID: proof.CallID, Revision: proof.Revision, PreviousAmount: proof.PreviousAmountMicros, Released: proof.ReleasedMicros,
			Input: ReconciliationInput{ExpectedRevision: proof.Revision - 1, Currency: proof.Currency, Amount: proof.AmountMicros, Confirmed: true}, RetentionState: "EXPIRED", ExpiredAt: &projection.ExpiredAt}, nil
	}
	if _, err := retention.ReconciliationEvidence(source.ID, source.CallID, source.Revision, source.Hash, source.Raw); err != nil {
		return Reconciliation{}, domain.ErrNotReady
	}
	var result Reconciliation
	if json.Unmarshal(source.Raw, &result) != nil {
		return Reconciliation{}, domain.ErrNotReady
	}
	return result, nil
}

type expiredReceiptView struct {
	RetentionState string    `json:"retention_state"`
	ID             string    `json:"id"`
	Outcome        string    `json:"outcome"`
	AmountMicros   int64     `json:"amount_micros"`
	ExpiredAt      time.Time `json:"expired_at"`
}

func readReceiptTx(ctx context.Context, tx *sql.Tx, source financialSourceRecord) (json.RawMessage, error) {
	projection, err := expiredFinancialPayloadTx(ctx, tx, "model_call_receipts", source)
	if err != nil {
		return nil, err
	}
	if projection != nil {
		proof := projection.Summary.Receipt
		return json.Marshal(expiredReceiptView{RetentionState: "EXPIRED", ID: proof.ID, Outcome: proof.Outcome, AmountMicros: proof.AmountMicros, ExpiredAt: projection.ExpiredAt})
	}
	if _, err := retention.ReceiptEvidence(source.CallID, source.ID, source.Raw); err != nil {
		return nil, domain.ErrNotReady
	}
	return source.Raw, nil
}
