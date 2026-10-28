package backup

import (
	"errors"
	"strconv"
	"time"

	"novelstudio/internal/retention"
)

var errRetentionHistory = errors.New("archive retention history is incomplete or inconsistent")

type archiveReferenceHistory struct {
	Versions map[string]retention.ReferenceVersion
	Current  map[string]retention.ReferenceVersion
	Expiry   map[string]retention.ExpiryJournalRecord
}

func archiveVersionKey(id string, revision int64) string {
	return id + "/" + strconv.FormatInt(revision, 10)
}

func archiveClock(value []byte, expected time.Time) bool {
	if expected.IsZero() {
		return value == nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999", string(value), time.UTC)
	return err == nil && parsed.Equal(expected)
}

// This verifies journal/current/evidence relations only. Source rows, source
// slots, pinned dependency closure and protected authority are separate gates.
func readArchiveReferenceHistory(metadata Data, at time.Time) (*archiveReferenceHistory, error) {
	if at.IsZero() {
		return nil, errRetentionHistory
	}
	contracts := map[string]tableSpec{}
	for _, spec := range retentionTableCatalog() {
		contracts[spec.name] = spec
	}
	for _, name := range []string{"retention_reference_versions", "retention_references", "retention_evidence"} {
		rows, ok := metadata[name]
		if !ok || len(rows) > 250000 {
			return nil, errRetentionHistory
		}
		for _, row := range rows {
			if len(row) != len(contracts[name].cols()) {
				return nil, errRetentionHistory
			}
		}
	}
	result := &archiveReferenceHistory{Versions: map[string]retention.ReferenceVersion{}, Current: map[string]retention.ReferenceVersion{}, Expiry: map[string]retention.ExpiryJournalRecord{}}
	stored := map[string]Row{}
	counts, highest := map[string]int64{}, map[string]int64{}
	versionSpec := contracts["retention_reference_versions"]
	bytes := 0
	for _, row := range metadata[versionSpec.name] {
		raw, hash := versionSpec.value(row, "snapshot"), versionSpec.value(row, "record_hash")
		bytes += len(raw) + 512
		if bytes > 64<<20 {
			return nil, errRetentionHistory
		}
		v, err := retention.ValidateReferenceVersionRecord(raw, hash)
		if err != nil || v.RecordedAt.After(at) || versionSpec.value(row, "reference_id") != v.Reference.ID || versionSpec.value(row, "revision") != strconv.FormatInt(v.Reference.Revision, 10) || versionSpec.value(row, "project_id") != v.ProjectID || versionSpec.value(row, "source_slot_id") != v.SourceID || versionSpec.value(row, "source_payload_hash") != v.SourcePayloadHash || !archiveClock(row[versionSpec.index("recorded_at")], v.RecordedAt) {
			return nil, errRetentionHistory
		}
		key := archiveVersionKey(v.Reference.ID, v.Reference.Revision)
		if _, duplicate := result.Versions[key]; duplicate {
			return nil, errRetentionHistory
		}
		result.Versions[key], stored[key] = v, row
		counts[v.Reference.ID]++
		highest[v.Reference.ID] = max(highest[v.Reference.ID], v.Reference.Revision)
	}
	for id, count := range counts {
		if count != highest[id] {
			return nil, errRetentionHistory
		}
	}
	currentSpec := contracts["retention_references"]
	for _, row := range metadata[currentSpec.name] {
		id := currentSpec.value(row, "id")
		if _, duplicate := result.Current[id]; duplicate {
			return nil, errRetentionHistory
		}
		key := id + "/" + currentSpec.value(row, "revision")
		v, exists := result.Versions[key]
		if !exists || v.Reference.Revision != highest[id] || !archiveCurrentMatches(currentSpec, row, v) {
			return nil, errRetentionHistory
		}
		result.Current[id] = v
	}
	versions := make([]retention.ReferenceVersion, 0, len(result.Versions))
	for _, v := range result.Versions {
		versions = append(versions, v)
	}
	if retention.ValidateJournalClosure(versions, result.Current) != nil {
		return nil, errRetentionHistory
	}
	evidenceSpec := contracts["retention_evidence"]
	for _, row := range metadata[evidenceSpec.name] {
		key := evidenceSpec.value(row, "reference_id") + "/" + evidenceSpec.value(row, "reference_revision")
		v, exists := result.Versions[key]
		if !exists || v.Expiration == nil || evidenceSpec.value(row, "project_id") != v.ProjectID {
			return nil, errRetentionHistory
		}
		if _, duplicate := result.Expiry[key]; duplicate {
			return nil, errRetentionHistory
		}
		parent, exists := stored[archiveVersionKey(v.Reference.ID, v.Expiration.PriorRevision)]
		if !exists {
			return nil, errRetentionHistory
		}
		current := stored[key]
		record := retention.ExpiryJournalRecord{Version: versionSpec.value(current, "snapshot"), VersionHash: versionSpec.value(current, "record_hash"), Parent: versionSpec.value(parent, "snapshot"), ParentHash: versionSpec.value(parent, "record_hash"), EvidenceID: evidenceSpec.value(row, "id"), Evidence: evidenceSpec.value(row, "projection"), EvidenceHash: evidenceSpec.value(row, "projection_hash"), OriginalPayloadHash: evidenceSpec.value(row, "original_payload_hash")}
		if !archiveClock(row[evidenceSpec.index("expired_at")], v.Reference.ExpiredAt) {
			return nil, errRetentionHistory
		}
		record.EvidenceExpiredAt = v.Reference.ExpiredAt.Format("2006-01-02 15:04:05.000000")
		bytes += len(record.Evidence)
		if bytes > 64<<20 {
			return nil, errRetentionHistory
		}
		if _, _, err := retention.ValidateExpiryJournalRecord(record); err != nil {
			return nil, errRetentionHistory
		}
		result.Expiry[key] = record
	}
	for key, v := range result.Versions {
		if v.Expiration != nil {
			if _, ok := result.Expiry[key]; !ok {
				return nil, errRetentionHistory
			}
		}
	}
	return result, nil
}

func archiveCurrentMatches(spec tableSpec, row Row, v retention.ReferenceVersion) bool {
	r := v.Reference
	flag := func(value bool) string {
		if value {
			return "1"
		}
		return "0"
	}
	for name, expected := range map[string]string{
		"id": r.ID, "project_id": v.ProjectID, "owner_table": v.Table, "owner_key": v.OwnerKey, "field_key": v.Field, "class": string(v.Class), "storage_kind": string(v.Storage), "payload_hash": r.PayloadHash, "time_basis": string(r.TimeBasis), "state": string(r.State), "revision": strconv.FormatInt(r.Revision, 10), "content_root": flag(r.ContentRoot), "accounting_open": flag(r.AccountingOpen),
	} {
		if spec.value(row, name) != expected {
			return false
		}
	}
	object := row[spec.index("object_hash")]
	if (object != nil) != (v.Storage == retention.Blob) || string(object) != r.ObjectHash {
		return false
	}
	for name, expected := range map[string]time.Time{
		"source_created_at": r.CreatedAt, "first_verified_at": r.FirstVerifiedAt, "deadline": v.Deadline, "expired_at": r.ExpiredAt, "lease_until": r.LeaseUntil, "updated_at": v.RecordedAt,
	} {
		if !archiveClock(row[spec.index(name)], expected) {
			return false
		}
	}
	return true
}
