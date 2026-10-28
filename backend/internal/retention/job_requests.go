package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"novelstudio/internal/authority"
)

func IsRequestKeyExpiry(item Expiry) bool { return requestKeyRule(item.Rule) }

// Resolves a planned reference to the exact existing source, never guessing an
// original free-text primary key from its digest. This grants no expiry rights.
// A caller changing lock order must roll back, reacquire actor/policy/project
// locks, rescan and reload this input before ApplyJobRequestKeyTx.
func LoadJobRequestKeyTx(ctx context.Context, tx *sql.Tx, project string, item Expiry, at time.Time) (RequestKeyRetirement, error) {
	fail := func(err error) (RequestKeyRetirement, error) { return RequestKeyRetirement{}, err }
	if !requestKeyRule(item.Rule) {
		return fail(ErrUnsupportedExpiryOwner)
	}
	current, err := readReferenceVersions(ctx, tx, project, at, item.ReferenceID)
	if err != nil {
		return fail(err)
	}
	v, found := current[item.ReferenceID]
	if !found || v.Reference.State != Retained || v.Reference.Revision != item.Revision || v.Reference.PayloadHash != item.PayloadHash || v.Reference.Rule != item.Rule || !v.Deadline.Equal(item.Deadline) {
		return fail(ErrInvalidInventory)
	}
	source, err := expiryOwnerSource(v)
	if v.Table == "operations" {
		var parts []string
		if json.Unmarshal([]byte(v.OwnerKey), &parts) != nil || len(parts) != 2 || !id32.MatchString(parts[0]) || parts[1] != "sha256:"+item.PayloadHash {
			return fail(ErrInvalidInventory)
		}
		rows, e := tx.QueryContext(ctx, "SELECT operation_id FROM operations WHERE actor_id=? AND SHA2(CONVERT(operation_id USING binary),256)=? LIMIT 2 FOR SHARE", parts[0], item.PayloadHash)
		if e != nil {
			return fail(e)
		}
		count := 0
		var key string
		for rows.Next() {
			if e := rows.Scan(&key); e != nil {
				rows.Close()
				return fail(e)
			}
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return fail(e)
		}
		if count != 1 {
			return fail(ErrInvalidInventory)
		}
		source = SourceKey{Table: v.Table, Primary: map[string]string{"actor_id": parts[0], "operation_id": key}}
		err = nil
	}
	if err != nil {
		return fail(err)
	}
	reader := &captureSourceReader{ctx: ctx, tx: tx}
	index, _ := NewSourceIndex(nil)
	index.loadSource = reader.read
	row, err := index.sourceRow(source.Table, source.Primary)
	if err != nil {
		return fail(err)
	}
	owner, err := index.Owner(source.Table, source.Primary)
	if err != nil || owner.Shared || owner.ProjectID != project {
		return fail(ErrSourceOwnership)
	}
	identity, err := requestIdentityFromRow(row, owner)
	if err != nil || row.Nulls["operation_id"] || financialDigest([]byte(row.Values["operation_id"])) != item.PayloadHash {
		return fail(ErrInvalidInventory)
	}
	actualOwner, err := RowOwnerKey(source.Table, row.Values)
	if err != nil || actualOwner != v.OwnerKey {
		return fail(ErrInvalidInventory)
	}
	return RequestKeyRetirement{WorkspaceID: identity.WorkspaceID, ActorID: identity.ActorID, Table: v.Table, OriginalKey: row.Values["operation_id"], SourceID: identity.SourceID, RequestHash: identity.RequestHash, Kind: identity.Kind, OriginalOwnerKey: v.OwnerKey}, nil
}

func ApplyJobRequestKeyTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project, job string, revision int64, item Expiry, input RequestKeyRetirement, proof ExpiredProjection) error {
	if err := validateJobDetachTx(ctx, tx, project, job, []Expiry{item}, []ExpiredProjection{proof}); err != nil {
		return err
	}
	if _, err := ApplyRequestKeyExpiryTx(ctx, tx, ledger, project, input, revision, item, proof); err != nil {
		return err
	}
	// The existing retry path independently checks the physical anonymous key,
	// alias, permanent admission policy and original proof without old plaintext.
	if _, err := ApplyRequestKeyExpiryTx(ctx, tx, ledger, project, input, revision, item, proof); err != nil {
		return err
	}
	return recordJobDetachTx(ctx, tx, project, job, []Expiry{item}, []ExpiredProjection{proof})
}

func verifyJobRequestKeyTx(ctx context.Context, tx *sql.Tx, project string, v ReferenceVersion, proof ExpiredProjection, at time.Time) (bool, error) {
	identity := proof.Summary.RequestIdentity
	if identity == nil {
		return false, ErrInvalidInventory
	}
	primary := map[string]string{"id": identity.SourceID}
	if v.Table == "operations" {
		primary = map[string]string{"actor_id": identity.ActorID, "operation_id": "retired-v1:" + v.Reference.PayloadHash}
	}
	reader := &captureSourceReader{ctx: ctx, tx: tx}
	index, _ := NewSourceIndex(nil)
	index.loadSource = reader.read
	if _, err := index.sourceRow(v.Table, primary); err != nil {
		return false, err
	}
	if err := index.bindExpiryVersions(ctx, tx, project, map[string]ReferenceVersion{v.Reference.ID: v}, at, true); err != nil {
		return false, err
	}
	return true, nil
}
