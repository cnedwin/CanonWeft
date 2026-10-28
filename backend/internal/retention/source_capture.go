package retention

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/authority"
)

// SourceKey identifies an actual SQL row. IfPresent is for a pre-write capture
// of a row that has not been created yet; required post-write rows must exist.
type SourceKey struct {
	Table     string
	Primary   map[string]string
	IfPresent bool
	// Generation inputs are immutable after creation, but an evaluation child
	// is associated only when queued. Observe its original age at Prepare;
	// complete consumer validation still requires the later real association.
	ObserveOnly bool
	// Only catalogued scalar debug text; never certifies inputs, outputs, JSON,
	// credentials or execution. Cancellation remains independent of blob IO.
	DiagnosticsOnly bool
}

func diagnosticColumn(column ColumnPolicy) bool {
	return column.Privacy == PrivateText && column.Handling == ClearText && column.Class == Debug && column.Shape == ShapeText
}

// CaptureSourceValues preserves original hashes/observation times before a
// mutable row changes, and captures newly saved values in the same transaction.
// It reads only requested rows and their fixed ownership dependencies. It does
// not grant authority, save plaintext copies, certify full project lineage,
// infer original creation time or mark an inventory complete. Roll back on error.
func CaptureSourceValues(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, targets ...SourceKey) error {
	return CaptureSourceValuesWithObjects(ctx, tx, ledger, project, nil, targets...)
}

// Object-backed projections additionally verify the original immutable files.
func CaptureSourceValuesWithObjects(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, read ReadSourceObject, targets ...SourceKey) error {
	return captureSourceValuesWithObjects(ctx, tx, ledger, project, read, nil, targets...)
}

func captureSourceValuesWithObjects(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, read ReadSourceObject, public *publicIndexProof, targets ...SourceKey) error {
	if tx == nil || !id32.MatchString(project) || len(targets) == 0 || len(targets) > 256 {
		return ErrSourceOwnership
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil || !enabled {
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
	if err != nil || now.IsZero() {
		return ErrSourceOwnership
	}
	index, _ := NewSourceIndex(nil)
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index.loadSource = reader.read
	read = boundedSourceReader(project, read)
	observations := map[string]SourceObservation{}
	selectedRows := map[string]SourceRow{}
	diagnosticRows := map[string]SourceRow{}
	queue := append([]SourceKey(nil), targets...)
	seen := map[string]bool{}
	expiryBound := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		if len(queue) > 2048 {
			return ErrSourceOwnership
		}
		target := queue[i]
		if target.ObserveOnly && (target.Table != "generation_tasks" || target.DiagnosticsOnly) {
			return ErrSourceOwnership
		}
		key, err := RowOwnerKey(target.Table, target.Primary)
		if err != nil {
			return err
		}
		identity := target.Table + ":" + key
		if target.DiagnosticsOnly {
			identity += ":diagnostics"
		}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		row, err := index.sourceRow(target.Table, target.Primary)
		if target.IfPresent && errors.Is(err, sql.ErrNoRows) {
			delete(seen, identity)
			continue
		}
		if err != nil {
			return err
		}
		// Memory envelopes cannot pass ordinary document ownership until their
		// independent expiry proof is bound. First check durable scope, then bind
		// evidence and repeat the full ownership check below.
		owner, err := recoveryExpiryOwnerScope(index, row)
		if err != nil || owner.Shared || owner.ProjectID != project {
			return ErrSourceOwnership
		}
		key, err = RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return err
		}
		if !expiryBound[identity] {
			if err := index.bindExpiredRowMode(ctx, tx, project, row, now, target.DiagnosticsOnly); err != nil {
				return err
			}
			expiryBound[identity] = true
		}
		owner, err = index.Owner(row.Table, row.Values)
		if err != nil || owner.Shared || owner.ProjectID != project {
			return ErrSourceOwnership
		}
		if target.DiagnosticsOnly {
			spec, _ := OwnerSpec(row.Table)
			if !slices.ContainsFunc(spec.Columns, diagnosticColumn) {
				return ErrSourceOwnership
			}
			diagnosticRows[row.Table+":"+key] = row
		} else if !target.ObserveOnly {
			selectedRows[row.Table+":"+key] = row
			if (row.Table == "memory_items" || row.Table == "memory_revisions") && !index.replacedColumn(project, row, "document") {
				keys, err := memoryCaptureKeys(row)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if row.Table == "import_drafts" {
				queue = append(queue, SourceKey{Table: "source_imports", Primary: map[string]string{"id": row.Values["import_id"]}})
			}
			if row.Table == "skill_discovery_runs" || row.Table == "skill_discovery_candidates" {
				queue = append(queue, SourceKey{Table: "skill_discovery_sources", Primary: map[string]string{"id": row.Values["source_id"]}})
				if row.Table == "skill_discovery_candidates" {
					queue = append(queue, SourceKey{Table: "skill_discovery_runs", Primary: map[string]string{"id": row.Values["run_id"]}}, SourceKey{Table: "skill_packages", Primary: map[string]string{"id": row.Values["package_id"]}})
				}
			}
			if row.Table == "platform_tasks" {
				queue = append(queue, SourceKey{Table: "ip_revisions", Primary: map[string]string{"id": row.Values["candidate_id"]}})
			}
			if public != nil && row.Table == "knowledge_indexes" && row.Values["id"] == public.id {
				queue = append(queue, public.keys...)
			}
			if row.Table == "change_plans" && !index.replacedColumn(project, row, "payload") {
				keys, err := index.changeCaptureKeys(ctx, project, row, read)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if (row.Table == "tool_trials" || row.Table == "qualification_runs" || row.Table == "qualification_steps" || row.Table == "qualification_approvals") && !index.replacedColumn(project, row, "frozen") {
				keys, err := evaluationCaptureKeys(row)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if (row.Table == "semantic_review_runs" || row.Table == "semantic_review_acceptances") && !index.replacedColumn(project, row, "frozen") {
				keys, err := index.semanticCaptureKeys(ctx, project, row, read)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if (row.Table == "recipe_runs" || row.Table == "recipe_steps" || row.Table == "generation_tasks") && !index.replacedColumn(project, row, "frozen") {
				keys, err := index.recipeCaptureKeys(ctx, tx, row)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if row.Table == "capability_versions" || row.Table == "skill_packages" {
				keys, err := capabilityCaptureKeys(row)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if row.Table == "model_calls" {
				keys, err := captureCallKeys(ctx, tx, row.Values["id"])
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if row.Table == "qualification_calls" && row.Values["grant_id"] != "" {
				queue = append(queue, SourceKey{Table: "qualification_week_grants", Primary: map[string]string{"id": row.Values["grant_id"]}})
			}
			if row.Table == "ip_revisions" {
				for _, table := range []string{"ip_amendments", "ip_promotions", "ip_branch_deltas", "ip_merge_candidates"} {
					queue = append(queue, SourceKey{Table: table, Primary: map[string]string{"candidate_id": row.Values["id"]}, IfPresent: true})
				}
			}
			if row.Table == "context_packages" {
				keys, err := index.contextCaptureKeys(ctx, project, row, read)
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
			if row.Table == "working_indexes" {
				sources, err := privateManifestCaptureKeys(project, row)
				if err != nil {
					return err
				}
				queue = append(queue, sources...)
				keys, err := capturePrivateChunkKeys(ctx, tx, row.Values["id"])
				if err != nil {
					return err
				}
				queue = append(queue, keys...)
			}
		}
		spec, _ := OwnerSpec(row.Table)
		for _, column := range spec.Columns {
			field := column.Column
			if target.DiagnosticsOnly && !diagnosticColumn(column) {
				continue
			}
			if !observedSourceColumn(row.Table, field) {
				continue
			}
			id := observationID(project, row.Table, key, field)
			if _, replaced := index.replacedSources[id]; replaced {
				continue
			}
			observations[id] = SourceObservation{ID: id, Table: row.Table, OwnerKey: key, Field: field, PayloadHash: financialDigest([]byte(row.Values[field])), Null: row.Nulls[field]}
		}
		if target.ObserveOnly {
			// Observation cannot suppress a later request for full capture of
			// the same row in this transaction.
			delete(seen, identity)
		}
	}
	if err := index.verifyCapturedPackages(ctx, project, selectedRows, read); err != nil {
		return err
	}
	var accounting []MaterialReference
	for _, row := range selectedRows {
		if accountingField(row.Table) == "" {
			continue
		}
		ref, err := index.accountingReference(project, row)
		if err != nil {
			return err
		}
		accounting = append(accounting, ref)
		observations[ref.SourceID] = SourceObservation{ID: ref.SourceID, Table: ref.Table, OwnerKey: ref.OwnerKey, Field: ref.Field, PayloadHash: ref.SourcePayloadHash}
	}
	batch := make([]SourceObservation, 0, len(observations))
	for _, observation := range observations {
		batch = append(batch, observation)
	}
	slices.SortFunc(batch, func(a, b SourceObservation) int { return strings.Compare(a.ID, b.ID) })
	for offset := 0; offset < len(batch); offset += 500 {
		if _, _, err := registerSourceBatch(ctx, tx, project, batch[offset:min(offset+500, len(batch))], now); err != nil {
			return err
		}
	}
	if err := captureMutableSourceVersions(ctx, tx, project, index, lifecycle, selectedRows, diagnosticRows, accounting, now, read, public); err != nil {
		return err
	}
	if len(batch) > 0 {
		return invalidateCapturedInventory(ctx, tx, project)
	}
	return nil
}

// Business changes invalidate a cleanup scan in their existing transaction.
// This changes neither the source's age nor any retention/deletion identity.
func invalidateCapturedInventory(ctx context.Context, tx *sql.Tx, project string) error {
	result, err := tx.ExecContext(ctx, "UPDATE project_lifecycle SET inventory_revision=inventory_revision+1,scanned_at=NULL,state='UNSCANNED',updated_at=UTC_TIMESTAMP(6) WHERE project_id=? AND inventory_revision<9223372036854775807", project)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM project_lifecycle WHERE project_id=?", project).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrInvalidInventory
		}
	}
	return nil
}

// Capture exact business projections before edits or state transitions remove
// them. Candidate auxiliary fields are collected with their parent; release
// bodies use the original immutable objects, never a current resource head.
func captureMutableSourceVersions(ctx context.Context, tx *sql.Tx, project string, index *SourceIndex, lifecycle RegisteredLifecycle, rows, diagnosticRows map[string]SourceRow, accounting []MaterialReference, now time.Time, read ReadSourceObject, public *publicIndexProof) error {
	g := materialCollector{index: index, project: project, refs: map[string]MaterialReference{}}
	var preserved []ObservedMaterial
	for _, material := range index.preservedAfterExpiry {
		if _, selected := rows[material.Table+":"+material.OwnerKey]; selected {
			preserved = append(preserved, material)
		}
	}
	for _, ref := range accounting {
		g.refs[ref.ID] = ref
	}
	for identity, row := range diagnosticRows {
		if _, full := rows[identity]; full {
			continue
		}
		spec, _ := OwnerSpec(row.Table)
		for _, column := range spec.Columns {
			if diagnosticColumn(column) && !row.Nulls[column.Column] {
				if err := g.add(row, column.Column, row.Values[column.Column]); err != nil {
					return err
				}
			}
		}
	}
	composite := false
	authored := false
	for _, row := range rows {
		authored = authored || row.Table == "capability_versions" || row.Table == "import_drafts" || row.Table == "patch_drafts" || row.Table == "novel_setup_drafts" || row.Table == "novel_settings_versions" || row.Table == "novel_settings_drafts"
		composite = composite || row.Table == "memory_items" || row.Table == "memory_revisions"
		if row.Table == "ip_revisions" || row.Table == "artifact_releases" {
			composite = true
			field := "payload"
			if row.Table == "artifact_releases" {
				field = "manifest"
			}
			if err := g.add(row, field, row.Values[field]); err != nil {
				return err
			}
			if err := g.add(row, "content_hash", row.Values["content_hash"]); err != nil {
				return err
			}
			if row.Table == "artifact_releases" {
				if err := g.artifacts(row); err != nil {
					return err
				}
			}
			continue
		}
		field := map[string]string{"novel_setup_drafts": "payload", "novel_settings_versions": "payload", "novel_settings_drafts": "payload", "memory_sessions": "document", "ip_drafts": "payload", "import_drafts": "payload", "patch_drafts": "payload", "resource_drafts": "body", "resource_policy_drafts": "payload", "ip_amendments": "reason", "ip_promotions": "payload", "ip_branch_deltas": "payload"}[row.Table]
		if field == "" {
			if err := g.captureRow(row); err != nil {
				return err
			}
			continue
		}
		if err := g.add(row, field, row.Values[field]); err != nil {
			return err
		}
	}
	if len(g.refs) == 0 && len(preserved) == 0 {
		return nil
	}
	if len(g.refs) > 4096 {
		return ErrSourceOwnership
	}
	adopted := map[string]bool{}
	if authored {
		roots, err := index.AuthorContentReferences(project)
		if err != nil {
			return err
		}
		for _, root := range roots {
			adopted[root.ID] = true
		}
	}
	if composite {
		roots, err := index.AdoptedContentReferences(project)
		if err != nil {
			return err
		}
		for _, root := range roots {
			adopted[root.ID] = true
		}
	}
	refs := make([]MaterialReference, 0, len(g.refs))
	for _, ref := range g.refs {
		ref.ContentRoot = adopted[ref.ID] || ref.Table == "ip_drafts" || ref.Table == "resource_drafts" || ref.Table == "resource_policy_drafts"
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b MaterialReference) int { return strings.Compare(a.ID, b.ID) })
	if err := index.attachKnownMaterialSources(project, refs); err != nil {
		return err
	}
	if err := index.attachWorkingProjections(project, refs); err != nil {
		return err
	}
	if err := index.attachDraftTextProofs(refs); err != nil {
		return err
	}
	if err := index.attachPrivateDraftSources(project, refs); err != nil {
		return err
	}
	if read == nil {
		for _, ref := range refs {
			if ref.Table == "artifact_releases" {
				return ErrSourceOwnership
			}
		}
	}
	observed, err := observeMaterialReferences(ctx, tx, project, refs, now)
	if err != nil {
		return err
	}
	if err := index.attachIngestSources(ctx, project, observed, read); err != nil {
		return err
	}
	if read != nil {
		if err := index.attachArtifactTextProofs(ctx, project, observed, boundedSourceReader(project, read)); err != nil {
			return err
		}
	}
	if err := index.attachContextSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachGenerationContextSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachGenerationOutputSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachCancelledReceiptSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachRecipeSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachSemanticSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachToolSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachQualificationSources(ctx, project, observed, read); err != nil {
		return err
	}
	if err := index.attachChangePlanSources(ctx, project, observed, read); err != nil {
		return err
	}
	if public != nil {
		if err := public.attach(index, project, observed); err != nil {
			return err
		}
	}
	observed = append(observed, preserved...)
	ids := make([]string, len(observed))
	for i := range observed {
		ids[i] = observed[i].ID
	}
	changes, err := registerMaterialReferences(ctx, tx, project, index, lifecycle, observed, now, ids...)
	if err != nil {
		return err
	}
	if changes.unbound > 0 {
		if public != nil {
			return ErrSourceOwnership
		}
		for _, row := range rows {
			switch row.Table {
			case "ingest_jobs", "source_imports", "skill_discovery_runs", "skill_discovery_candidates", "platform_tasks", "context_packages", "recipe_runs", "recipe_steps", "semantic_review_runs", "semantic_review_acceptances", "tool_trials", "qualification_runs", "qualification_steps", "capability_qualifications", "qualification_approvals", "generation_tasks", "change_plans", "working_indexes":
				// A new consumer must not commit an incomplete set of source pins.
				// Legacy inventory may report missing evidence conservatively;
				// the live writer has all selected inputs and must prove them now.
				return ErrSourceOwnership
			}
		}
	}
	return nil
}

type captureSourceReader struct {
	ctx   context.Context
	tx    *sql.Tx
	count int
	bytes int
}

func (r *captureSourceReader) read(table string, primary map[string]string) (SourceRow, error) {
	spec, ok := OwnerSpec(table)
	if !ok || len(primary) != len(spec.PrimaryKey) || r.count >= 4096 {
		return SourceRow{}, ErrSourceOwnership
	}
	if _, err := RowOwnerKey(table, primary); err != nil {
		return SourceRow{}, err
	}
	r.count++
	columns := make([]string, len(spec.Columns))
	for i, column := range spec.Columns {
		columns[i] = column.Column
	}
	conditions := make([]string, len(spec.PrimaryKey))
	args := make([]any, len(spec.PrimaryKey))
	for i, key := range spec.PrimaryKey {
		conditions[i], args[i] = "`"+key.Name+"`=?", primary[key.Name]
	}
	// Every identifier is supplied by the compiled schema catalog; values are
	// parameters. Locking reads see the current transaction's own new values.
	query := "SELECT `" + strings.Join(columns, "`,`") + "` FROM `" + spec.Table + "` WHERE " + strings.Join(conditions, " AND ") + " FOR SHARE"
	rows, err := r.tx.QueryContext(r.ctx, query, args...)
	if err != nil {
		return SourceRow{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SourceRow{}, err
		}
		return SourceRow{}, sql.ErrNoRows
	}
	cells := make([]sql.RawBytes, len(columns))
	pointers := make([]any, len(columns))
	for i := range pointers {
		pointers[i] = &cells[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return SourceRow{}, err
	}
	row := SourceRow{Table: table, Values: map[string]string{}, Nulls: map[string]bool{}}
	for i, column := range columns {
		r.bytes += len(column) + len(cells[i]) + 96
		if r.bytes > 32<<20 {
			return SourceRow{}, ErrSourceOwnership
		}
		row.Values[column] = string(cells[i])
		if cells[i] == nil {
			row.Nulls[column] = true
		}
	}
	if rows.Next() {
		return SourceRow{}, ErrSourceOwnership
	}
	if err := rows.Err(); err != nil {
		return SourceRow{}, err
	}
	return row, nil
}
