package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/authority"
)

// ApplySourceExpiryTx is an internal coordinator primitive, not an HTTP action
// or permission to delete files. The caller owns an authorized transaction and
// MUST roll it back on error. Only prevalidated typed-owner projections belong
// here; the coordinator must retain its plan/proofs for exact retries.
func ApplySourceExpiryTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, source SourceKey, inventoryRevision int64, expiries []Expiry, proofs []ExpiredProjection) error {
	if tx == nil || !id32.MatchString(project) || inventoryRevision < 1 || len(expiries) == 0 || len(expiries) > 32 || len(expiries) != len(proofs) || source.IfPresent || source.ObserveOnly || source.DiagnosticsOnly {
		return ErrInvalidInventory
	}
	ownerKey, err := RowOwnerKey(source.Table, source.Primary)
	if err != nil {
		return err
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	if err := verifyRegistrationCatalog(ctx, tx); err != nil {
		return err
	}
	lifecycle, err := RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil {
		return err
	}
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return err
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return err
	}
	ids, byID := make([]string, 0, len(expiries)), map[string]ExpiredProjection{}
	for i, expiry := range expiries {
		field := strings.TrimPrefix(expiry.Rule, source.Table+".")
		id, err := ReferenceID(project, source.Table, ownerKey, field)
		if err != nil || id != expiry.ReferenceID || !supportedExpiryField(source.Table, field) || proofs[i].ReferenceID != id || proofs[i].ExpiredAt.After(now) || proofs[i].ExpiredAt.Before(expiry.Deadline) || proofs[i].ExpiredAt.IsZero() || !proofs[i].ExpiredAt.Equal(proofs[0].ExpiredAt) {
			return ErrInvalidInventory
		}
		if _, exists := byID[id]; exists {
			return ErrInvalidInventory
		}
		ids, byID[id] = append(ids, id), proofs[i]
	}
	current, err := readReferenceVersions(ctx, tx, project, now, ids...)
	if err != nil || len(current) != len(ids) {
		return ErrInvalidInventory
	}
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index, _ := NewSourceIndex(nil)
	index.loadSource = reader.read
	row, err := index.sourceRow(source.Table, source.Primary)
	if err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := validateRecipeProjectionOwner(row, proof); err != nil {
			return err
		}
	}
	for _, expiry := range expiries {
		field := strings.TrimPrefix(expiry.Rule, source.Table+".")
		if err := validateTransientOwner(row, field, byID[expiry.ReferenceID]); err != nil {
			return err
		}
	}
	allExpired := true
	for _, v := range current {
		allExpired = allExpired && v.Reference.State == Expired
	}
	if allExpired {
		return verifyExpiryReplay(ctx, tx, project, row, expiries, byID, current, now)
	}
	if source.Table == "generation_previews" {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM generation_tasks WHERE id=? AND project_id=? FOR UPDATE", row.Values["generation_id"], project).Scan(&state); err != nil {
			return err
		}
		if slices.Contains([]string{"RUNNING", "QUEUED", "CANCELLING"}, state) {
			return ErrLiveExpiryConsumers
		}
	}
	if source.Table == "qualification_steps" {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM qualification_runs WHERE id=? AND project_id=? FOR UPDATE", row.Values["run_id"], project).Scan(&state); err != nil {
			return err
		}
		if slices.Contains([]string{"RUNNING", "QUEUED", "PAUSED"}, state) {
			return ErrLiveExpiryConsumers
		}
	}
	if source.Table == "recipe_steps" {
		var parentState string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM recipe_runs WHERE id=? AND project_id=? FOR UPDATE", row.Values["run_id"], project).Scan(&parentState); err != nil {
			return err
		}
		if slices.Contains([]string{"RUNNING", "QUEUED", "CANCELLING"}, parentState) {
			return ErrLiveExpiryConsumers
		}
	}
	live, err := ExpiryHasLiveConsumersTx(ctx, tx, project, expiries[0], expiries, now)
	if err != nil {
		return err
	}
	if live {
		return ErrLiveExpiryConsumers
	}
	if source.Table == "context_packages" {
		for _, expiry := range expiries {
			if expiry.Rule != "context_packages.payload" {
				continue
			}
			binding := byID[expiry.ReferenceID].Summary.Context
			if binding == nil || binding.ID != row.Values["id"] || binding.IndexID != row.Values["index_id"] || binding.ContentHash != row.Values["content_hash"] {
				return ErrInvalidInventory
			}
			live, err := ContextHasLiveConsumersTx(ctx, tx, project, expiry.ReferenceID, now)
			if err != nil || live {
				return ErrInvalidInventory
			}
		}
	}
	if lifecycle.Revision != inventoryRevision {
		return ErrInvalidInventory
	}
	var scanState string
	var scanned sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT state,CAST(scanned_at AS CHAR) FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&scanState, &scanned); err != nil {
		return err
	}
	scannedAt, scanErr := time.ParseInLocation("2006-01-02 15:04:05.999999", scanned.String, time.UTC)
	if scanState != "CURRENT" || !scanned.Valid || scanErr != nil || scannedAt.After(now) {
		return ErrInvalidInventory
	}
	owner, err := index.Owner(row.Table, row.Values)
	if err != nil || owner.Shared || owner.ProjectID != project || slices.Contains([]string{"RUNNING", "QUEUED", "CANCELLING", "RESERVED", "DISPATCHED"}, row.Values["state"]) {
		return ErrInvalidInventory
	}
	history, err := readPinnedReferenceHistory(ctx, tx, project, current, lifecycle.Lifecycle, now)
	if err != nil {
		return err
	}
	var roots, ancestors []Reference
	for _, v := range current {
		roots = append(roots, v.Reference)
	}
	for key, v := range history {
		if head, ok := current[key.ID]; !ok || head.Reference.Revision != key.Revision {
			ancestors = append(ancestors, v.Reference)
		}
	}
	deadlines, err := referenceDeadlinesWithHistory(roots, ancestors, lifecycle.Lifecycle, now)
	if err != nil {
		return err
	}
	updates, versions := map[string]string{}, make([]ReferenceVersion, 0, len(expiries))
	for _, expiry := range expiries {
		v, proof := current[expiry.ReferenceID], byID[expiry.ReferenceID]
		r := v.Reference
		if r.State != Retained || r.Revision != expiry.Revision || r.PayloadHash != expiry.PayloadHash || r.Rule != expiry.Rule || v.Storage != expiry.Storage || v.OwnerKey != ownerKey || v.Table != source.Table || v.Lifecycle != lifecycle.Lifecycle || !deadlines[r.ID].Equal(expiry.Deadline) || r.LeaseUntil.After(proof.ExpiredAt) || v.RecordedAt.After(scannedAt) {
			return ErrInvalidInventory
		}
		value, err := expirySourceValue(row, v.Field)
		if err != nil {
			return err
		}
		expected, err := ProjectExpired(expiry, []byte(value), proof.Summary, proof.ExpiredAt)
		if AnonymizedLabelAllowed(expiry.Rule) {
			expected, err = ProjectAnonymizedLabel(expiry, []byte(value), proof.Summary, proof.ExpiredAt)
		}
		if ClearScalarAllowed(expiry.Rule) {
			expected, err = ProjectClearedScalar(expiry, []byte(value), proof.Summary, proof.ExpiredAt)
		}
		if err != nil || stableHash(expected) != stableHash(proof) {
			return ErrInvalidInventory
		}
		next, err := expiredReferenceVersion(v, expiry.Deadline, proof)
		if err != nil {
			return err
		}
		versions = append(versions, next)
		if v.Storage == Inline {
			if proof.Materialization == AnonymizedLabel {
				updates[v.Field] = retiredLabelPrefix + proof.OriginalPayloadHash
				continue
			}
			if proof.Materialization == ClearScalar {
				updates[v.Field] = ""
				continue
			}
			encoded, err := json.Marshal(proof)
			if err != nil {
				return err
			}
			updates[v.Field] = string(encoded)
		}
	}
	// Every validation above observes the original row. Never replace frozen
	// JSON before extracting and proving its nested object references.
	if len(updates) > 0 {
		columns := make([]string, 0, len(updates))
		for column := range updates {
			columns = append(columns, column)
		}
		slices.Sort(columns)
		sets, args := []string{}, []any{}
		for _, column := range columns {
			sets, args = append(sets, "`"+column+"`=?"), append(args, updates[column])
		}
		spec, _ := OwnerSpec(source.Table)
		where := []string{}
		for _, key := range spec.PrimaryKey {
			where, args = append(where, "`"+key.Name+"`=?"), append(args, source.Primary[key.Name])
		}
		result, err := tx.ExecContext(ctx, "UPDATE `"+spec.Table+"` SET "+strings.Join(sets, ",")+" WHERE "+strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrInvalidInventory
		}
	}
	for _, next := range versions {
		if err := persistExpiryVersionTx(ctx, tx, project, next, byID[next.Reference.ID]); err != nil {
			return err
		}
	}
	return invalidateCapturedInventory(ctx, tx, project)
}

// Match the existing typed owner preparation and verified runtime readers.
// No caller-controlled SQL path or unreviewed document shape is admitted.
func supportedExpiryField(table, field string) bool {
	if DirectSkillObjectRule(table + "." + field) {
		return true
	}
	if DirectPublicationObjectRule(table + "." + field) {
		return true
	}
	if len(TransientColumns(table+"."+field)) > 0 {
		return true
	}
	if ClearScalarAllowed(table+"."+field) || DirectExecutionObjectRule(table+"."+field) || DirectImportObjectRule(table+"."+field) || DirectKnowledgeObjectRule(table+"."+field) {
		return true
	}
	switch table {
	case "model_calls":
		return field == "output_hash"
	case "recipe_runs":
		return field == "frozen"
	case "recipe_steps":
		return field == "frozen" || field == "input_hash" || field == "output_hash"
	case "generation_tasks", "tool_trials", "semantic_review_runs":
		return field == "frozen" || field == "frozen.input_hash"
	case "qualification_runs":
		return field == "frozen"
	case "model_call_receipts":
		return field == "payload" || field == "payload.output_hash"
	case "model_reconciliations", "context_packages":
		return field == "payload"
	}
	return false
}

// Only explicitly catalogued execution files share this materializer. Author
// content and nested payloads require their own retention and source contracts.
func DirectExecutionObjectRule(key string) bool {
	rule, known := ConcreteRule(key)
	return known && rule.Storage == Blob && rule.Class == RawRun && strings.Count(key, ".") == 1
}

// Import originals follow the catalogued candidate lifetime and promotion to
// author Content. Finishing ingestion never assigns a raw-execution TTL.
func DirectImportObjectRule(key string) bool {
	return key == "source_imports.source_hash" || key == "ingest_jobs.source_hash"
}

// The immutable hash remains structural evidence. Other releases and chunks
// continue to protect the physical object through their own live references.
func DirectKnowledgeObjectRule(key string) bool {
	return key == "knowledge_chunks.source_hash"
}

func expirySourceValue(row SourceRow, field string) (string, error) {
	if row.Table == "artifact_releases" && strings.HasPrefix(field, "manifest.artifacts.") {
		s, err := releaseExpirySummary(row)
		if err != nil {
			return "", err
		}
		return releaseArtifactValue(s, field)
	}
	if row.Table == "model_call_receipts" && field == "payload.output_hash" {
		summary, err := ReceiptEvidence(row.Values["call_id"], row.Values["id"], []byte(row.Values["payload"]))
		if err != nil || !hash64.MatchString(summary.OutputHash) {
			return "", ErrInvalidInventory
		}
		return summary.OutputHash, nil
	}
	parts := strings.Split(field, ".")
	value, ok := row.Values[parts[0]]
	if !ok || row.Nulls[parts[0]] {
		return "", ErrInvalidInventory
	}
	if len(parts) == 1 {
		return value, nil
	}
	if len(parts) != 2 {
		return "", ErrInvalidInventory
	}
	var document map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &document) != nil {
		return "", ErrInvalidInventory
	}
	var nested string
	if json.Unmarshal(document[parts[1]], &nested) != nil {
		return "", ErrInvalidInventory
	}
	return nested, nil
}

func verifyExpiryReplay(ctx context.Context, tx *sql.Tx, project string, row SourceRow, expiries []Expiry, proofs map[string]ExpiredProjection, versions map[string]ReferenceVersion, now time.Time) error {
	for _, expiry := range expiries {
		v, proof := versions[expiry.ReferenceID], proofs[expiry.ReferenceID]
		if err := validateTransientOwner(row, v.Field, proof); err != nil {
			return err
		}
		if err := validateRecipeProjectionOwner(row, proof); err != nil {
			return err
		}
		if v.Expiration == nil || v.Expiration.PriorRevision != expiry.Revision || v.Reference.PayloadHash != expiry.PayloadHash || v.Reference.Rule != expiry.Rule || v.Storage != expiry.Storage || !v.Deadline.Equal(expiry.Deadline) || stableHash(proof) != v.Expiration.EvidenceHash {
			return ErrInvalidInventory
		}
		var raw, hash, original, parentHash string
		if err := tx.QueryRowContext(ctx, "SELECT projection,projection_hash,original_payload_hash FROM retention_evidence WHERE project_id=? AND reference_id=? AND reference_revision=? FOR SHARE", project, v.Reference.ID, v.Reference.Revision).Scan(&raw, &hash, &original); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT record_hash FROM retention_reference_versions WHERE project_id=? AND reference_id=? AND revision=? FOR SHARE", project, v.Reference.ID, v.Expiration.PriorRevision).Scan(&parentHash); err != nil {
			return err
		}
		if hash != v.Expiration.EvidenceHash || financialDigest([]byte(raw)) != hash || original != v.Reference.PayloadHash || parentHash != v.Expiration.PriorHash {
			return ErrInvalidInventory
		}
		if binding := proof.Summary.Context; binding != nil {
			if binding.ID != row.Values["id"] || binding.IndexID != row.Values["index_id"] || binding.ContentHash != row.Values["content_hash"] {
				return ErrInvalidInventory
			}
		}
		if v.Storage == Inline {
			if proof.Materialization == AnonymizedLabel {
				if row.Values[v.Field] != retiredLabelPrefix+proof.OriginalPayloadHash {
					return ErrInvalidInventory
				}
				continue
			}
			if proof.Materialization == ClearScalar {
				if !ClearScalarAllowed(v.Reference.Rule) || row.Values[v.Field] != "" {
					return ErrInvalidInventory
				}
				continue
			}
			stored, err := ParseSourceProjection([]byte(row.Values[v.Field]), v.Reference.Rule)
			if err != nil || stableHash(stored) != hash {
				return ErrInvalidInventory
			}
		} else {
			value, err := replayNestedSourceValue(ctx, tx, project, row, v, versions, now)
			if err != nil || value != v.Reference.ObjectHash || financialDigest([]byte(value)) != v.Reference.PayloadHash {
				return ErrInvalidInventory
			}
		}
	}
	return nil
}

// A whole JSON field may already have been replaced in the same expiry batch.
// Its typed summary can retain a nested object identity only when that enclosing
// envelope has its own matching, independently persisted expiry evidence.
func replayNestedSourceValue(ctx context.Context, tx *sql.Tx, project string, row SourceRow, v ReferenceVersion, versions map[string]ReferenceVersion, now time.Time) (string, error) {
	return replayNestedSourceValueMode(ctx, tx, project, row, v, versions, now, true)
}

func replayNestedSourceValueMode(ctx context.Context, tx *sql.Tx, project string, row SourceRow, v ReferenceVersion, versions map[string]ReferenceVersion, now time.Time, current bool) (string, error) {
	return nestedSourceValue(project, row, v, versions, func(id string) (ReferenceVersion, bool, error) {
		loaded, err := readReferenceVersionsMode(ctx, tx, project, now, current, id)
		parent, ok := loaded[id]
		return parent, ok, err
	})
}

func nestedSourceValue(project string, row SourceRow, v ReferenceVersion, versions map[string]ReferenceVersion, loadParent func(string) (ReferenceVersion, bool, error)) (string, error) {
	parts := strings.Split(v.Field, ".")
	if len(parts) == 1 {
		return expirySourceValue(row, v.Field)
	}
	if len(parts) != 2 && !(row.Table == "artifact_releases" && len(parts) == 4 && parts[0] == "manifest" && parts[1] == "artifacts") {
		return "", ErrInvalidInventory
	}
	container, err := ParseSourceProjection([]byte(row.Values[parts[0]]), row.Table+"."+parts[0])
	if err != nil {
		return expirySourceValue(row, v.Field)
	}
	parentID, err := ReferenceID(project, row.Table, v.OwnerKey, parts[0])
	if err != nil || container.ReferenceID != parentID {
		return "", ErrInvalidInventory
	}
	parent, ok := versions[parentID]
	if !ok {
		if loadParent == nil {
			return "", ErrInvalidInventory
		}
		parent, ok, err = loadParent(parentID)
		if err != nil {
			return "", err
		}
	}
	if !ok || parent.Reference.State != Expired || parent.Expiration == nil || stableHash(container) != parent.Expiration.EvidenceHash {
		return "", ErrInvalidInventory
	}
	if row.Table == "artifact_releases" && len(parts) == 4 {
		return releaseArtifactValue(container.Summary.Release, v.Field)
	}
	switch v.Field {
	case "source.archive_hash":
		if row.Table == "skill_packages" {
			return container.Summary.InputHash, nil
		}
	case "frozen.input_hash":
		return container.Summary.InputHash, nil
	case "payload.output_hash":
		if container.Summary.Receipt != nil {
			return container.Summary.Receipt.OutputHash, nil
		}
	}
	return "", ErrInvalidInventory
}
