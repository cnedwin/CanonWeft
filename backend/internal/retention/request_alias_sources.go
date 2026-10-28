package retention

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// RequestAliasRecord is independent policy/alias evidence, never reconstructed
// from the expiry projection. Archive adapters also bound its clocks to snapshot.
type RequestAliasRecord struct {
	Table, OriginalKeyHash, OriginalOwnerKey, AnonymousKey string
	Identity                                               RequestIdentitySummary
	PolicyWorkspaceID                                      string
	PolicyClosed                                           bool
	PolicyRevision                                         int64
	CreatedAt, PolicyCreatedAt, PolicyClosedAt             time.Time
}

func requestAliasKey(table, actor, hash string) string { return table + "/" + actor + "/" + hash }

func validateRequestAlias(v ReferenceVersion, proof ExpiredProjection, alias RequestAliasRecord) error {
	if proof.Materialization != AnonymizedRequestKey || proof.Summary.RequestIdentity == nil || alias.Table != v.Table || alias.OriginalKeyHash != v.Reference.PayloadHash || alias.OriginalOwnerKey != v.OwnerKey || alias.AnonymousKey != "retired-v1:"+alias.OriginalKeyHash || alias.Identity != *proof.Summary.RequestIdentity || alias.PolicyWorkspaceID != alias.Identity.WorkspaceID || !alias.PolicyClosed || alias.PolicyRevision < 2 {
		return ErrSourceOwnership
	}
	for _, clock := range []time.Time{alias.CreatedAt, alias.PolicyCreatedAt, alias.PolicyClosedAt} {
		if clock.IsZero() || clock.Nanosecond()%1000 != 0 {
			return ErrSourceOwnership
		}
	}
	if alias.CreatedAt.Before(alias.PolicyCreatedAt) || alias.PolicyClosedAt.Before(alias.PolicyCreatedAt) {
		return ErrSourceOwnership
	}
	original, _, err := requestIdentityOwnerKeys(alias.Table, alias.OriginalKeyHash, alias.Identity)
	if err != nil || original != v.OwnerKey {
		return ErrSourceOwnership
	}
	return nil
}

func readRequestAliasRecord(ctx context.Context, tx *sql.Tx, table, actor, originalHash string, current bool) (RequestAliasRecord, error) {
	var record RequestAliasRecord
	if !requestKeyRule(table+".operation_id") || !id32.MatchString(actor) || !hash64.MatchString(originalHash) {
		return record, ErrSourceOwnership
	}
	lock := ""
	// Selected captures already hold project locks, while retirement orders
	// actor/policy before project. A current read must fail immediately on a
	// conflicting policy lock; waiting here could invert that lock order.
	if current {
		lock = " FOR SHARE NOWAIT"
	}
	var created, policyCreated, closed string
	err := tx.QueryRowContext(ctx, `SELECT k.workspace_id,k.actor_id,k.owner_table,k.original_key_hash,k.original_owner_key,k.anonymous_key,k.source_id,k.request_hash,k.operation_kind,CAST(k.created_at AS CHAR),p.workspace_id,p.legacy_writes_closed,p.revision,CAST(p.created_at AS CHAR),CAST(p.closed_at AS CHAR)
FROM retention_operation_keys k JOIN retention_identity_policies p ON p.workspace_id=k.workspace_id
WHERE k.actor_id=? AND k.owner_table=? AND k.original_key_hash=?`+lock, actor, table, originalHash).Scan(&record.Identity.WorkspaceID, &record.Identity.ActorID, &record.Table, &record.OriginalKeyHash, &record.OriginalOwnerKey, &record.AnonymousKey, &record.Identity.SourceID, &record.Identity.RequestHash, &record.Identity.Kind, &created, &record.PolicyWorkspaceID, &record.PolicyClosed, &record.PolicyRevision, &policyCreated, &closed)
	if err != nil {
		return record, err
	}
	for _, entry := range []struct {
		raw    string
		target *time.Time
	}{{created, &record.CreatedAt}, {policyCreated, &record.PolicyCreatedAt}, {closed, &record.PolicyClosedAt}} {
		*entry.target, err = time.ParseInLocation("2006-01-02 15:04:05.999999", entry.raw, time.UTC)
		if err != nil {
			return RequestAliasRecord{}, ErrSourceOwnership
		}
	}
	return record, nil
}

func requestIdentityFromRow(row SourceRow, owner SourceOwner) (RequestIdentitySummary, error) {
	identity := RequestIdentitySummary{WorkspaceID: owner.WorkspaceID, ActorID: row.Values["actor_id"], SourceID: row.Values["id"], RequestHash: row.Values["request_hash"]}
	if row.Table == "operations" {
		identity.SourceID, identity.Kind = row.Values["result_id"], row.Values["kind"]
	}
	if row.Table == "model_calls" {
		identity.RequestHash = row.Values["input_hash"]
	}
	if !requestKeyRule(row.Table+".operation_id") || !validRequestIdentity(identity) {
		return RequestIdentitySummary{}, ErrSourceOwnership
	}
	return identity, nil
}

// Every anonymous source in the scanned project needs its independently bound
// expiry proof. Missing history must not cause a fresh source registration.
func (index *SourceIndex) validateRetiredRequestCoverage(project string) error {
	for table, rows := range index.rows {
		if !requestKeyRule(table + ".operation_id") {
			continue
		}
		for _, row := range rows {
			if err := index.validateRetiredRequestRow(project, row); err != nil {
				return err
			}
		}
	}
	return nil
}

func (index *SourceIndex) validateRetiredRequestRow(project string, row SourceRow) error {
	if !requestKeyRule(row.Table+".operation_id") || !strings.HasPrefix(strings.ToLower(row.Values["operation_id"]), "retired-v1:") {
		return nil
	}
	owner, err := index.Owner(row.Table, row.Values)
	if err != nil {
		return err
	}
	if owner.Shared || owner.ProjectID != project {
		return nil
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	v, exists := index.replacedSources[observationID(project, row.Table, key, "operation_id")]
	if !exists || index.expiryProofs[v.Reference.ID].Materialization != AnonymizedRequestKey {
		return ErrSourceOwnership
	}
	return nil
}
