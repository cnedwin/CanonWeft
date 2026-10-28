package integration

import (
	"context"
	"database/sql"
	"testing"

	"novelstudio/internal/retention"
)

// Capture actual source rows and independently persisted current/history/proof
// records in one snapshot, close it, then exercise the offline archive binder.
func archivedCurrentMaterials(t *testing.T, db *sql.DB, project string) []retention.MaterialReference {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := retention.ScanSources(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT v.snapshot,v.record_hash FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? ORDER BY r.id", project)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]retention.ReferenceVersion{}
	proofs := map[string]retention.ExpiryJournalRecord{}
	for rows.Next() {
		var raw, hash string
		if err := rows.Scan(&raw, &hash); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		v, err := retention.ValidateReferenceVersionRecord(raw, hash)
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		versions[v.Reference.ID] = v
		if v.Reference.State == retention.Expired {
			proofs[v.Reference.ID] = retention.ExpiryJournalRecord{Version: raw, VersionHash: hash}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for id, record := range proofs {
		v := versions[id]
		if err := tx.QueryRowContext(ctx, "SELECT snapshot,record_hash FROM retention_reference_versions WHERE project_id=? AND reference_id=? AND revision=?", project, id, v.Expiration.PriorRevision).Scan(&record.Parent, &record.ParentHash); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT id,projection,projection_hash,original_payload_hash,CAST(expired_at AS CHAR) FROM retention_evidence WHERE project_id=? AND reference_id=? AND reference_revision=?", project, id, v.Reference.Revision).Scan(&record.EvidenceID, &record.Evidence, &record.EvidenceHash, &record.OriginalPayloadHash, &record.EvidenceExpiredAt); err != nil {
			t.Fatal(err)
		}
		proofs[id] = record
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	index, err := retention.NewSourceIndex(snapshot.Index.Records())
	if err != nil {
		t.Fatal(err)
	}
	materials, err := index.ArchivedMaterials(project, versions, proofs)
	if err != nil {
		t.Fatal("offline source binding rejected real SQL snapshot", err)
	}
	for id, v := range versions {
		if v.Reference.State != retention.Expired || v.Storage != retention.Inline {
			continue
		}
		altered := snapshot.Index.Records()
		for i, row := range altered {
			key, _ := retention.RowOwnerKey(row.Table, row.Values)
			if row.Table == v.Table && key == v.OwnerKey {
				altered[i].Values[v.Field] = "altered"
			}
		}
		bad, err := retention.NewSourceIndex(altered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.ArchivedMaterials(project, versions, proofs); err == nil {
			t.Fatal("offline source binding accepted changed owner body", id)
		}
		nulls := snapshot.Index.Records()
		for i, row := range nulls {
			key, _ := retention.RowOwnerKey(row.Table, row.Values)
			if row.Table == v.Table && key == v.OwnerKey {
				nulls[i].Values[v.Field] = ""
				nulls[i].Nulls[v.Field] = true
			}
		}
		bad, err = retention.NewSourceIndex(nulls)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.ArchivedMaterials(project, versions, proofs); err == nil {
			t.Fatal("archive substituted SQL NULL for an expired materialized value", id)
		}
		break
	}
	return materials
}
