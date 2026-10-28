package backup

import (
	"encoding/json"
	"novelstudio/internal/retention"
	"strconv"
	"strings"
	"testing"
)

func TestRetentionArchiveCarriesReferencedProvenanceWithoutExecutableRows(t *testing.T) {
	for _, mode := range []string{"valid", "orphan", "unmarked", "missing current", "missing pin", "promoted to current"} {
		t.Run(mode, func(t *testing.T) {
			data, at := retentionHistoryFixture(t)
			var spec tableSpec
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_reference_versions" {
					spec = s
				}
			}
			var parent, expired retention.ReferenceVersion
			if err := json.Unmarshal([]byte(spec.value(data[spec.name][0], "snapshot")), &parent); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(spec.value(data[spec.name][1], "snapshot")), &expired); err != nil {
				t.Fatal(err)
			}
			ancestor := parent
			ancestor.OwnerKey, _ = retention.OwnerKey(strings.Repeat("c", 32))
			ancestor.Reference.ID, _ = retention.ReferenceID(parent.ProjectID, parent.Table, ancestor.OwnerKey, parent.Field)
			ancestor.SourceID = ancestor.Reference.ID
			ancestor.ProvenanceOnly = mode != "unmarked"
			raw, _ := json.Marshal(ancestor)
			retentionMetadataRow(t, data, spec.name, map[string]string{"reference_id": ancestor.Reference.ID, "revision": "1", "project_id": ancestor.ProjectID, "source_slot_id": ancestor.SourceID, "source_payload_hash": ancestor.SourcePayloadHash, "record_hash": digest(raw), "snapshot": string(raw), "recorded_at": ancestor.RecordedAt.Format("2006-01-02 15:04:05.000000")})
			stamp := ancestor.Reference.FirstVerifiedAt.Format("2006-01-02 15:04:05.000000")
			retentionMetadataRow(t, data, "retention_source_slots", map[string]string{"id": ancestor.SourceID, "project_id": ancestor.ProjectID, "owner_table": ancestor.Table, "owner_key": ancestor.OwnerKey, "field_key": ancestor.Field, "time_basis": "LEGACY_REGISTERED", "first_verified_at": stamp})
			retentionMetadataRow(t, data, "retention_source_values", map[string]string{"source_id": ancestor.SourceID, "payload_hash": ancestor.SourcePayloadHash, "is_null": "0", "first_verified_at": stamp})
			if mode != "orphan" {
				pin := retention.ReferenceSource{ReferenceID: ancestor.Reference.ID, Revision: 1, PayloadHash: ancestor.Reference.PayloadHash}
				parent.Reference.Sources = []retention.ReferenceSource{pin}
				parent.LineageState = "PARTIAL"
				parentRaw, _ := json.Marshal(parent)
				expired.Reference.Sources = parent.Reference.Sources
				expired.LineageState = parent.LineageState
				expired.Expiration.PriorHash = digest(parentRaw)
				for i, v := range []retention.ReferenceVersion{parent, expired} {
					body, _ := json.Marshal(v)
					spec.set(data[spec.name][i], "snapshot", string(body))
					spec.set(data[spec.name][i], "record_hash", digest(body))
					spec.set(data[spec.name][i], "revision", strconv.FormatInt(v.Reference.Revision, 10))
				}
			}
			if mode == "missing current" {
				data["retention_references"] = nil
			}
			if mode == "missing pin" {
				data[spec.name] = data[spec.name][:2]
			}
			if mode == "promoted to current" {
				for _, s := range retentionTableCatalog() {
					if s.name != "retention_references" {
						continue
					}
					row := append(Row{}, data[s.name][0]...)
					for column, value := range map[string]string{"id": ancestor.Reference.ID, "owner_key": ancestor.OwnerKey, "revision": "1", "state": "RETAINED", "updated_at": stamp} {
						s.set(row, column, value)
					}
					row[s.index("expired_at")] = nil
					data[s.name] = append(data[s.name], row)
				}
			}
			history, err := readArchiveReferenceHistory(data, at)
			if mode == "valid" {
				if err != nil || len(history.Current) != 1 || len(history.Versions) != 3 {
					t.Fatal("referenced provenance rejected", err)
				}
				if err = validateArchiveSourceHistory(data, history, at); err != nil {
					t.Fatal("historical source clock rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid detached history accepted", mode)
			}
		})
	}
}
