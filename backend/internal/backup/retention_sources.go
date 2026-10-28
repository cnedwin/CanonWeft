package backup

import (
	"strings"
	"time"

	"novelstudio/internal/retention"
)

type archiveSourceSlot struct {
	Project, Table, Owner, Field string
	First                        time.Time
}

// Source identities and original observation clocks survive expiry and restore.
// The actual business owner/body is checked by the later source binding gate.
func validateArchiveSourceHistory(metadata Data, history *archiveReferenceHistory, at time.Time) error {
	if history == nil || at.IsZero() {
		return errRetentionHistory
	}
	specs := map[string]tableSpec{}
	for _, spec := range retentionTableCatalog() {
		specs[spec.name] = spec
	}
	for _, name := range []string{"retention_source_slots", "retention_source_values"} {
		rows, ok := metadata[name]
		if !ok || len(rows) > 250000 {
			return errRetentionHistory
		}
		for _, row := range rows {
			if len(row) != len(specs[name].cols()) {
				return errRetentionHistory
			}
		}
	}
	slots := map[string]archiveSourceSlot{}
	slotSpec := specs["retention_source_slots"]
	for _, row := range metadata[slotSpec.name] {
		slot := archiveSourceSlot{Project: slotSpec.value(row, "project_id"), Table: slotSpec.value(row, "owner_table"), Owner: slotSpec.value(row, "owner_key"), Field: slotSpec.value(row, "field_key")}
		id, err := retention.SourceSlotID(slot.Project, slot.Table, slot.Owner, slot.Field)
		if err != nil || id != slotSpec.value(row, "id") || slotSpec.value(row, "time_basis") != string(retention.LegacyRegistered) {
			return errRetentionHistory
		}
		if _, duplicate := slots[id]; duplicate {
			return errRetentionHistory
		}
		slot.First, err = time.ParseInLocation("2006-01-02 15:04:05.999999", slotSpec.value(row, "first_verified_at"), time.UTC)
		if err != nil || slot.First.IsZero() || slot.First.After(at) {
			return errRetentionHistory
		}
		slots[id] = slot
	}
	values := map[string]time.Time{}
	firstSeen := map[string]bool{}
	valueSpec := specs["retention_source_values"]
	for _, row := range metadata[valueSpec.name] {
		id, hash, null := valueSpec.value(row, "source_id"), valueSpec.value(row, "payload_hash"), valueSpec.value(row, "is_null")
		slot, exists := slots[id]
		if !exists || !hex64.MatchString(hash) || (null != "0" && null != "1") || (null == "1" && hash != digest(nil)) {
			return errRetentionHistory
		}
		clock, err := time.ParseInLocation("2006-01-02 15:04:05.999999", valueSpec.value(row, "first_verified_at"), time.UTC)
		if err != nil || clock.Before(slot.First) || clock.After(at) {
			return errRetentionHistory
		}
		key := id + "/" + hash + "/" + null
		if _, duplicate := values[key]; duplicate {
			return errRetentionHistory
		}
		values[key] = clock
		firstSeen[id] = firstSeen[id] || clock.Equal(slot.First)
	}
	for id := range slots {
		if !firstSeen[id] {
			return errRetentionHistory
		}
	}
	versions := make([]retention.ReferenceVersion, 0, len(history.Versions))
	for _, v := range history.Versions {
		slot, exists := slots[v.SourceID]
		if !exists || slot.Project != v.ProjectID || slot.Table != v.Table || slot.Owner != v.OwnerKey || slot.Field != strings.SplitN(v.Field, ".", 2)[0] || !slot.First.Equal(v.Reference.FirstVerifiedAt) {
			return errRetentionHistory
		}
		first, exists := values[v.SourceID+"/"+v.SourcePayloadHash+"/0"]
		if !exists || !first.Equal(v.ValueFirstVerifiedAt) {
			return errRetentionHistory
		}
		versions = append(versions, v)
	}
	if err := retention.ValidateArchivedPins(versions); err != nil {
		return errRetentionHistory
	}
	return nil
}
