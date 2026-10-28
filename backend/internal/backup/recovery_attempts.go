package backup

import (
	"time"

	"novelstudio/internal/retention"
)

func validateArchiveRecoveryAttempts(business, metadata Data, at time.Time) error {
	spec, err := archiveStateTable(metadata, "retention_recovery_attempts")
	if err != nil {
		return err
	}
	owners, err := newRetentionArchiveOwners(business)
	if err != nil {
		return err
	}
	seen, size := map[string]bool{}, 0
	for _, row := range metadata[spec.name] {
		for _, cell := range row {
			if cell == nil {
				return errRetentionJob
			}
		}
		a := retention.RecoveryAttempt{Target: spec.value(row, "target_project_id"), Project: spec.value(row, "project_id"), Actor: spec.value(row, "actor_id"), Namespace: spec.value(row, "storage_namespace"), State: spec.value(row, "state")}
		var e1, e2 error
		a.Created, e1 = archiveStateClock(row[spec.index("created_at")], false)
		a.Updated, e2 = archiveStateClock(row[spec.index("updated_at")], false)
		disabled, raw := spec.value(row, "restored_disabled"), spec.value(row, "objects")
		size += len(raw)
		a.Objects, err = retention.ParseRecoveryObjects(raw)
		if size > 64<<20 || e1 != nil || e2 != nil || a.Updated.After(at) || (disabled != "0" && disabled != "1") || err != nil || retention.ValidateRecoveryAttempt(a) != nil || seen[a.Target] {
			return errRetentionJob
		}
		seen[a.Target] = true
		project, _ := retention.OwnerKey(a.Project)
		actor, _ := retention.OwnerKey(a.Actor)
		target, _ := retention.OwnerKey(a.Target)
		workspace := owners.rows["projects"][project].Values["workspace_id"]
		targetWorkspace := owners.rows["projects"][target].Values["workspace_id"]
		if workspace == "" || owners.rows["users"][actor].Values["workspace_id"] != workspace || (targetWorkspace != "" && targetWorkspace != workspace) || (a.State == "COMMITTED" && targetWorkspace == "") || ((a.State == "WRITING" || a.State == "CLEANED") && targetWorkspace != "") {
			return errRetentionJob
		}
	}
	return nil
}
