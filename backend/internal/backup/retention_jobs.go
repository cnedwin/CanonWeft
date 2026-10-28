package backup

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"novelstudio/internal/retention"
)

var errRetentionJob = errors.New("retention archive cleanup job or receipt is inconsistent")

type archiveCleanupJob struct {
	plan             retention.Plan
	state            string
	created, updated time.Time
	items            map[string]bool // true for objects, false for reference detachment
	receipts         map[string]archiveCleanupReceipt
}

type archiveCleanupReceipt struct {
	result, evidence string
	at               time.Time
}

// Validates historical intent and receipts, not present execution authority or
// proof of physical erasure. A restored job must remain disabled until a new
// trusted inventory is built; receipt hashes cannot replace filesystem checks.
func validateArchiveCleanupJobs(business, metadata Data, history *archiveReferenceHistory, lifecycles map[string]archiveLifecycle, deadlines *retention.ArchivedDeadlines, at time.Time) error {
	if history == nil || deadlines == nil || at.IsZero() {
		return errRetentionJob
	}
	jobsSpec, err := archiveStateTable(metadata, "retention_jobs")
	if err != nil {
		return errRetentionJob
	}
	receiptSpec, err := archiveStateTable(metadata, "retention_receipts")
	if err != nil {
		return errRetentionJob
	}
	objectSpec, err := archiveStateTable(metadata, "retention_objects")
	if err != nil {
		return errRetentionJob
	}
	owners, err := newRetentionArchiveOwners(business)
	if err != nil {
		return errRetentionJob
	}
	objects := map[string]bool{}
	for _, row := range metadata[objectSpec.name] {
		objects[objectSpec.value(row, "project_id")+"/"+objectSpec.value(row, "object_hash")] = true
	}
	jobs, operations := map[string]*archiveCleanupJob{}, map[string]bool{}
	byteCount, itemCount := 0, 0
	for _, row := range metadata[jobsSpec.name] {
		for i, cell := range row {
			if cell == nil && jobsSpec.cols()[i] != "actor_id" {
				return errRetentionJob
			}
		}
		id, project, operation := jobsSpec.value(row, "id"), jobsSpec.value(row, "project_id"), jobsSpec.value(row, "operation_id")
		lifecycle, exists := lifecycles[project]
		if !hex32.MatchString(id) || jobs[id] != nil || !exists || !utf8.ValidString(operation) || utf8.RuneCountInString(operation) > 120 || strings.TrimSpace(operation) == "" || operations[project+"/"+operation] || !hex64.MatchString(jobsSpec.value(row, "request_hash")) {
			return errRetentionJob
		}
		projectKey, _ := retention.OwnerKey(project)
		workspace := owners.rows["projects"][projectKey].Values["workspace_id"]
		actor := row[jobsSpec.index("actor_id")]
		if workspace == "" {
			return errRetentionJob
		}
		if actor != nil {
			actorKey, _ := retention.OwnerKey(string(actor))
			if !hex32.MatchString(string(actor)) || owners.rows["users"][actorKey].Values["workspace_id"] != workspace {
				return errRetentionJob
			}
		}
		raw := jobsSpec.value(row, "plan")
		byteCount += len(raw)
		if byteCount > 64<<20 {
			return errRetentionJob
		}
		plan, planErr := retention.ValidatePlanRecord(raw)
		if planErr != nil || plan.ProjectID != project || plan.InventoryRevision > lifecycle.Revision || jobsSpec.value(row, "inventory_revision") != strconv.FormatInt(plan.InventoryRevision, 10) || jobsSpec.value(row, "inventory_hash") != plan.InventoryHash || jobsSpec.value(row, "plan_hash") != plan.Hash {
			return errRetentionJob
		}
		itemCount += len(plan.Expire) + len(plan.DeleteObjects) + len(plan.Blocked)
		if itemCount > 250000 {
			return errRetentionJob
		}
		created, createdErr := archiveStateClock(row[jobsSpec.index("created_at")], false)
		updated, updatedErr := archiveStateClock(row[jobsSpec.index("updated_at")], false)
		state := jobsSpec.value(row, "state")
		attempt := jobsSpec.value(row, "attempt")
		n, attemptErr := strconv.ParseUint(attempt, 10, 32)
		disabled, reason := jobsSpec.value(row, "restored_disabled"), jobsSpec.value(row, "reason_code")
		if createdErr != nil || updatedErr != nil || created.Before(plan.Cutoff) || plan.Cutoff.Before(lifecycle.FirstVerifiedAt) || updated.Before(created) || updated.After(at) || attemptErr != nil || strconv.FormatUint(n, 10) != attempt || (disabled != "0" && disabled != "1") || !cleanupReasonCode(reason) || !slices.Contains([]string{"PLANNED", "DETACHING", "DELETING", "VERIFYING", "COMPLETE", "RETRY_PENDING", "OVERDUE", "BLOCKED", "CANCELLED"}, state) {
			return errRetentionJob
		}
		job := &archiveCleanupJob{plan: plan, state: state, created: created, updated: updated, items: map[string]bool{}, receipts: map[string]archiveCleanupReceipt{}}
		for _, e := range plan.Expire {
			v, exists := history.Versions[archiveVersionKey(e.ReferenceID, e.Revision)]
			if !exists || v.ProjectID != project || v.Reference.State != retention.Retained || v.Reference.Rule != e.Rule || v.Reference.PayloadHash != e.PayloadHash || v.Storage != e.Storage || v.RecordedAt.After(plan.Cutoff) {
				return errRetentionJob
			}
			deadline, err := deadlines.At(project, e.ReferenceID, e.Revision, plan.Cutoff)
			if err != nil || !deadline.Equal(e.Deadline) {
				return errRetentionJob
			}
			job.items[e.ReferenceID] = false
		}
		for _, hash := range plan.DeleteObjects {
			if !objects[project+"/"+hash] {
				return errRetentionJob
			}
			job.items["object:"+hash] = true
		}
		for _, key := range plan.Blocked {
			if strings.HasPrefix(key, "object:") {
				if !objects[project+"/"+strings.TrimPrefix(key, "object:")] {
					return errRetentionJob
				}
			} else if v, ok := history.Current[key]; !ok || v.ProjectID != project {
				return errRetentionJob
			}
		}
		cursor := jobsSpec.value(row, "cursor_key")
		if _, ok := job.items[cursor]; cursor != "" && !ok {
			return errRetentionJob
		}
		jobs[id], operations[project+"/"+operation] = job, true
	}
	for _, row := range metadata[receiptSpec.name] {
		for _, cell := range row {
			if cell == nil {
				return errRetentionJob
			}
		}
		job := jobs[receiptSpec.value(row, "job_id")]
		if job == nil {
			return errRetentionJob
		}
		key, phase := receiptSpec.value(row, "item_key"), receiptSpec.value(row, "phase")
		object, exists := job.items[key]
		result, evidence := receiptSpec.value(row, "result"), receiptSpec.value(row, "evidence_hash")
		recorded, clockErr := archiveStateClock(row[receiptSpec.index("recorded_at")], false)
		_, duplicate := job.receipts[key+"/"+phase]
		if !exists || duplicate || !hex64.MatchString(evidence) || clockErr != nil || recorded.Before(job.created) || recorded.After(job.updated) || !slices.Contains([]string{"SUCCEEDED", "ABSENT", "BLOCKED"}, result) || (phase != "VERIFIED" && phase != "DETACHED" && phase != "REMOVED") || (phase == "DETACHED" && object) || (phase == "REMOVED" && !object) || (!object && result == "ABSENT") {
			return errRetentionJob
		}
		job.receipts[key+"/"+phase] = archiveCleanupReceipt{result: result, evidence: evidence, at: recorded}
	}
	for _, job := range jobs {
		if job.state == "PLANNED" && len(job.receipts) != 0 {
			return errRetentionJob
		}
		if job.state == "COMPLETE" && len(job.plan.Blocked) != 0 {
			return errRetentionJob
		}
		for key, object := range job.items {
			phase := "DETACHED"
			if object {
				phase = "REMOVED"
			}
			performed, hasPerformed := job.receipts[key+"/"+phase]
			verified, hasVerified := job.receipts[key+"/VERIFIED"]
			if hasVerified && (!hasPerformed || performed.result == "BLOCKED" || verified.at.Before(performed.at)) {
				return errRetentionJob
			}
			if job.state == "COMPLETE" && (!hasPerformed || !hasVerified || performed.result == "BLOCKED" || verified.result == "BLOCKED") {
				return errRetentionJob
			}
		}
		for _, e := range job.plan.Expire {
			for _, phase := range []string{"DETACHED", "VERIFIED"} {
				r, exists := job.receipts[e.ReferenceID+"/"+phase]
				if !exists || r.result == "BLOCKED" {
					continue
				}
				key := archiveVersionKey(e.ReferenceID, e.Revision+1)
				v, exists := history.Versions[key]
				if !exists || v.Expiration == nil || v.Expiration.PriorRevision != e.Revision || v.Reference.PayloadHash != e.PayloadHash || !v.Deadline.Equal(e.Deadline) || v.Reference.ExpiredAt.Before(job.created) || v.Reference.ExpiredAt.After(r.at) || history.Expiry[key].EvidenceHash != r.evidence {
					return errRetentionJob
				}
			}
		}
	}
	return nil
}

func cleanupReasonCode(value string) bool {
	if len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' {
			return false
		}
	}
	return true
}
