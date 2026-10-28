package backup

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/retention"
)

var errRetentionState = errors.New("retention archive lifecycle or object state is inconsistent")

type archiveLifecycle struct {
	retention.RegisteredLifecycle
	State     string
	ScannedAt time.Time
	UpdatedAt time.Time
}

// These checks establish archive consistency, never current deletion authority.
// Restore must separately authenticate the original ledger and current events.
func readArchiveLifecycles(business, metadata Data, manifest Manifest) (map[string]archiveLifecycle, error) {
	if manifest.Authority == nil || !hex32.MatchString(manifest.Authority.LedgerID) || manifest.Authority.Protocol != authority.Protocol || manifest.SnapshotAt.IsZero() {
		return nil, errRetentionState
	}
	projects, err := archiveProjectIDs(business)
	if err != nil {
		return nil, err
	}
	spec, err := archiveStateTable(metadata, "project_lifecycle")
	if err != nil {
		return nil, err
	}
	result := map[string]archiveLifecycle{}
	sequences := map[uint64]bool{}
	for _, row := range metadata[spec.name] {
		project := spec.value(row, "project_id")
		catalog := spec.value(row, "catalog_version")
		if !projects[project] || spec.value(row, "source_ledger_id") != manifest.Authority.LedgerID || (catalog != "1" && !(manifest.Schema >= 37 && manifest.Schema <= 38 && catalog == "2") && !(manifest.Schema == 38 && catalog == "3")) {
			return nil, errRetentionState
		}
		if _, duplicate := result[project]; duplicate {
			return nil, errRetentionState
		}
		r := archiveLifecycle{RegisteredLifecycle: retention.RegisteredLifecycle{ProjectID: project}, State: spec.value(row, "state")}
		r.Revision, err = archiveRevision(spec.value(row, "inventory_revision"))
		if err != nil || !slices.Contains([]string{"UNSCANNED", "CURRENT", "PURGING", "PURGED", "OVERDUE", "BLOCKED"}, r.State) {
			return nil, errRetentionState
		}
		for column, target := range map[string]*time.Time{"first_verified_at": &r.FirstVerifiedAt, "updated_at": &r.UpdatedAt, "scanned_at": &r.ScannedAt} {
			*target, err = archiveStateClock(row[spec.index(column)], column == "scanned_at")
			if err != nil || target.After(manifest.SnapshotAt) {
				return nil, errRetentionState
			}
		}
		if r.UpdatedAt.Before(r.FirstVerifiedAt) || (!r.ScannedAt.IsZero() && (r.ScannedAt.Before(r.FirstVerifiedAt) || r.ScannedAt.After(r.UpdatedAt))) || (r.State == "CURRENT" && r.ScannedAt.IsZero()) || (r.State == "UNSCANNED" && !r.ScannedAt.IsZero()) {
			return nil, errRetentionState
		}
		sequence := row[spec.index("delete_sequence")]
		fields := []string{"source_deleted_at", "deleted_at", "recover_until", "cleanup_eligible_at", "online_deadline", "backup_deadline"}
		if sequence == nil {
			if spec.value(row, "time_basis") != string(retention.SourceUTC) || r.State == "PURGED" {
				return nil, errRetentionState
			}
			for _, name := range fields {
				if row[spec.index(name)] != nil {
					return nil, errRetentionState
				}
			}
		} else {
			n, parseErr := strconv.ParseUint(string(sequence), 10, 64)
			if parseErr != nil || n == 0 || strconv.FormatUint(n, 10) != string(sequence) || sequences[n] {
				return nil, errRetentionState
			}
			sequences[n] = true
			source, sourceErr := archiveStateClock(row[spec.index("source_deleted_at")], false)
			deleted, deletedErr := archiveStateClock(row[spec.index("deleted_at")], false)
			if sourceErr != nil || deletedErr != nil || deleted.After(r.UpdatedAt) {
				return nil, errRetentionState
			}
			switch retention.TimeBasis(spec.value(row, "time_basis")) {
			case retention.SourceUTC:
				r.Lifecycle, err = retention.DeletedLifecycle(manifest.Authority.LedgerID, n, deleted)
			case retention.LegacyRegistered:
				if deleted.Before(r.FirstVerifiedAt) {
					return nil, errRetentionState
				}
				r.Lifecycle, err = retention.LegacyDeletedLifecycle(manifest.Authority.LedgerID, n, source, deleted)
			default:
				return nil, errRetentionState
			}
			if err != nil {
				return nil, errRetentionState
			}
			l := r.Lifecycle
			clocks := []time.Time{l.SourceDeletedAt, l.DeletedAt, l.RecoverUntil, l.CleanupEligibleAt, l.OnlineDeadline, l.BackupDeadline}
			for i, column := range fields {
				if !archiveClock(row[spec.index(column)], clocks[i]) {
					return nil, errRetentionState
				}
			}
		}
		result[project] = r
	}
	return result, nil
}

func validateArchiveLifecycleHistory(metadata Data, history *archiveReferenceHistory, lifecycles map[string]archiveLifecycle) error {
	if history == nil {
		return errRetentionState
	}
	for _, version := range history.Versions {
		registered, exists := lifecycles[version.ProjectID]
		if !exists || (version.Lifecycle != (retention.Lifecycle{}) && version.Lifecycle != registered.Lifecycle) {
			return errRetentionState
		}
	}
	spec, err := archiveStateTable(metadata, "retention_source_slots")
	if err != nil {
		return err
	}
	for _, row := range metadata[spec.name] {
		if _, exists := lifecycles[spec.value(row, "project_id")]; !exists {
			return errRetentionState
		}
	}
	return nil
}

func validateArchiveObjects(business, metadata Data, materials map[string][]retention.MaterialReference, at time.Time) error {
	projects, err := archiveProjectIDs(business)
	if err != nil || at.IsZero() {
		return errRetentionState
	}
	spec, err := archiveStateTable(metadata, "retention_objects")
	if err != nil {
		return err
	}
	retained := map[string]bool{}
	for project, entries := range materials {
		for _, material := range entries {
			if material.Storage == retention.Blob {
				retained[project+"/"+material.ObjectHash] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, row := range metadata[spec.name] {
		project, hash, state := spec.value(row, "project_id"), spec.value(row, "object_hash"), spec.value(row, "state")
		key := project + "/" + hash
		_, revisionErr := archiveRevision(spec.value(row, "revision"))
		if !projects[project] || !hex64.MatchString(hash) || seen[key] || revisionErr != nil || !slices.Contains([]string{"PRESENT", "DELETING", "DELETED", "BLOCKED"}, state) {
			return errRetentionState
		}
		seen[key] = true
		first, firstErr := archiveStateClock(row[spec.index("first_seen_at")], false)
		lease, leaseErr := archiveStateClock(row[spec.index("lease_until")], true)
		deleted, deletedErr := archiveStateClock(row[spec.index("deleted_at")], true)
		if firstErr != nil || leaseErr != nil || deletedErr != nil || first.After(at) || (!lease.IsZero() && lease.Before(first)) || deleted.After(at) {
			return errRetentionState
		}
		if state == "DELETED" {
			if deleted.IsZero() || deleted.Before(first) || lease.After(deleted) {
				return errRetentionState
			}
		} else if !deleted.IsZero() {
			return errRetentionState
		}
		if retained[key] && (state == "DELETING" || state == "DELETED") {
			return errRetentionState
		}
	}
	// Legacy files can predate registration. Absence of an object row must not
	// exclude their bytes or certify that a complete inventory has been made.
	return nil
}

func archiveEffectiveDeadlines(history *archiveReferenceHistory, lifecycles map[string]archiveLifecycle) (*retention.ArchivedDeadlines, error) {
	if history == nil {
		return nil, errRetentionHistory
	}
	versions := make([]retention.ReferenceVersion, 0, len(history.Versions))
	for _, v := range history.Versions {
		versions = append(versions, v)
	}
	registered := make(map[string]retention.Lifecycle, len(lifecycles))
	for project, lifecycle := range lifecycles {
		registered[project] = lifecycle.Lifecycle
	}
	result, err := retention.ResolveArchivedDeadlines(versions, registered)
	if err != nil {
		return nil, errRetentionHistory
	}
	return result, nil
}

func archiveStateTable(data Data, name string) (tableSpec, error) {
	for _, spec := range retentionTableCatalog() {
		if spec.name != name {
			continue
		}
		rows, exists := data[name]
		if !exists || len(rows) > 250000 {
			return tableSpec{}, errRetentionState
		}
		for _, row := range rows {
			if len(row) != len(spec.cols()) {
				return tableSpec{}, errRetentionState
			}
		}
		return spec, nil
	}
	return tableSpec{}, errRetentionState
}

func archiveProjectIDs(data Data) (map[string]bool, error) {
	for _, spec := range tableCatalog {
		if spec.name != "projects" {
			continue
		}
		rows, exists := data[spec.name]
		if !exists || len(rows) > 250000 {
			return nil, errRetentionState
		}
		ids := map[string]bool{}
		for _, row := range rows {
			if len(row) != len(spec.cols()) {
				return nil, errRetentionState
			}
			id := spec.value(row, "id")
			if !hex32.MatchString(id) || ids[id] {
				return nil, errRetentionState
			}
			ids[id] = true
		}
		return ids, nil
	}
	return nil, errRetentionState
}

func archiveStateClock(raw []byte, nullable bool) (time.Time, error) {
	if raw == nil && nullable {
		return time.Time{}, nil
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", string(raw), time.UTC)
	if err != nil || at.IsZero() || len(raw) > 26 || at.Nanosecond()%1000 != 0 {
		return time.Time{}, errRetentionState
	}
	return at, nil
}

func archiveRevision(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != raw {
		return 0, errRetentionState
	}
	return value, nil
}
