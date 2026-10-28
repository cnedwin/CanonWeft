package service

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"novelstudio/internal/retention"
)

// prepareIndexedRetentionOwner is the source-bound entry point for the cleanup
// transaction coordinator. It prepares one owner update and all of its proofs from
// a single immutable snapshot; it never writes SQL or acquires deletion rights.
func prepareIndexedRetentionOwner(index *retention.SourceIndex, project, table string, primary map[string]string, expiries []retention.Expiry, at time.Time) (retentionOwnerProjection, error) {
	owner, err := index.Owner(table, primary)
	if err != nil || owner.Shared || owner.ProjectID != project {
		return retentionOwnerProjection{}, retention.ErrSourceOwnership
	}
	row, err := index.Record(table, primary)
	if err != nil {
		return retentionOwnerProjection{}, err
	}
	var prepared retentionOwnerProjection
	if table == "model_call_receipts" || table == "model_reconciliations" {
		prepared, err = prepareFinancialRetentionOwner(index, project, table, row, expiries, at)
	} else {
		prepared, err = prepareRetentionOwner(project, table, row.Values, expiries, at)
	}
	if err != nil {
		return retentionOwnerProjection{}, err
	}
	// Bind every original column and its NULL-ness, not only the replaced body.
	encoded, err := json.Marshal(row)
	if err != nil {
		return retentionOwnerProjection{}, retention.ErrInvalidInventory
	}
	prepared.OriginalRowHash = digest(string(encoded))
	return prepared, nil
}

func prepareFinancialRetentionOwner(index *retention.SourceIndex, project, table string, row retention.SourceRow, expiries []retention.Expiry, at time.Time) (retentionOwnerProjection, error) {
	fail := func() (retentionOwnerProjection, error) {
		return retentionOwnerProjection{}, retention.ErrInvalidInventory
	}
	if len(expiries) == 0 {
		return fail()
	}
	summary, err := index.FinancialSummary(table, row.Values)
	if err != nil {
		return fail()
	}
	key, err := retention.RowOwnerKey(table, row.Values)
	if err != nil {
		return fail()
	}
	values := map[string]string{"payload": row.Values["payload"]}
	if summary.Receipt != nil {
		values["payload.output_hash"] = summary.Receipt.OutputHash
	}
	items := append([]retention.Expiry(nil), expiries...)
	sort.Slice(items, func(i, j int) bool { return items[i].ReferenceID < items[j].ReferenceID })
	prepared := retentionOwnerProjection{OwnerKey: key, Updates: map[string]string{}, Evidence: []retention.ExpiredProjection{}}
	for i, item := range items {
		if (i > 0 && items[i-1].ReferenceID == item.ReferenceID) || !strings.HasPrefix(item.Rule, table+".") {
			return fail()
		}
		field := strings.TrimPrefix(item.Rule, table+".")
		value, exists := values[field]
		id, err := retention.ReferenceID(project, table, key, field)
		if !exists || err != nil || id != item.ReferenceID {
			return fail()
		}
		proofSummary := summary
		if field != "payload" {
			proofSummary = retention.EvidenceSummary{ValueHash: value}
		}
		proof, err := retention.ProjectExpired(item, []byte(value), proofSummary, at)
		if err != nil {
			return fail()
		}
		prepared.Evidence = append(prepared.Evidence, proof)
		if field == "payload" {
			encoded, err := json.Marshal(proof)
			if err != nil {
				return fail()
			}
			prepared.Updates[field] = string(encoded)
		}
	}
	return prepared, nil
}
