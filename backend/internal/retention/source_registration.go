package retention

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"novelstudio/internal/authority"
)

type SourceRegistrationReport struct {
	ExpiredReferences    int       `json:"expired_references"`
	CapturedAt           time.Time `json:"captured_at"`
	Sources              int       `json:"sources"`
	NewSources           int       `json:"new_sources"`
	NewValues            int       `json:"new_values"`
	DeletionReady        bool      `json:"deletion_ready"`
	MaterialReferences   int       `json:"material_references"`
	BlobReferences       int       `json:"blob_references"`
	ContentRoots         int       `json:"content_roots"`
	NewReferences        int       `json:"new_references"`
	UpdatedReferences    int       `json:"updated_references"`
	UnresolvedReferences int       `json:"unresolved_references"`
	AccountingReferences int       `json:"accounting_references"`
	PinnedReferences     int       `json:"pinned_references"`
	UnboundSources       int       `json:"unbound_sources"`
}

// RegisterSourceObservations must run inside an authorized transaction. On any
// failure the caller MUST roll back. This function neither commits nor grants
// access, execution permission, Inventory.Complete or object deletion authority.
func RegisterSourceObservations(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, readObject ReadSourceObject) (report SourceRegistrationReport, err error) {
	stage := "catalog"
	defer func() {
		if err != nil {
			err = fmt.Errorf("source registration %s: %w", stage, err)
		}
	}()
	if tx == nil || !id32.MatchString(project) {
		return SourceRegistrationReport{}, ErrSourceOwnership
	}
	if err := verifyRegistrationCatalog(ctx, tx); err != nil {
		return SourceRegistrationReport{}, err
	}
	// This takes the business project lock and verifies the live protected
	// deletion identity. The source age journal never restores a deleted project.
	stage = "lifecycle"
	lifecycle, err := RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "source snapshot"
	snapshot, err := scanSourceRows(ctx, tx, true)
	if err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "source observations"
	if err := snapshot.Index.bindExpiredSources(ctx, tx, project, snapshot.CapturedAt); err != nil {
		return SourceRegistrationReport{}, err
	}
	observations, err := snapshot.Index.SourceObservations(project)
	if err != nil {
		return SourceRegistrationReport{}, err
	}
	// Reads can wait for a concurrent writer. The registration clock is taken
	// after those locks, so a source first observed after a wait cannot receive
	// an earlier first-observation time from the beginning of the scan.
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return SourceRegistrationReport{}, err
	}
	snapshot.CapturedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil || snapshot.CapturedAt.IsZero() {
		return SourceRegistrationReport{}, ErrSourceOwnership
	}
	report = SourceRegistrationReport{CapturedAt: snapshot.CapturedAt, Sources: len(observations)}
	stage = "source age journal"
	for offset := 0; offset < len(observations); offset += 500 {
		batch := observations[offset:min(offset+500, len(observations))]
		newSources, newValues, err := registerSourceBatch(ctx, tx, project, batch, snapshot.CapturedAt)
		if err != nil {
			return SourceRegistrationReport{}, err
		}
		report.NewSources += newSources
		report.NewValues += newValues
	}
	stage = "observed materials"
	materials, err := ObservedMaterialReferences(ctx, tx, project, snapshot.Index, snapshot.CapturedAt)
	if err != nil {
		return SourceRegistrationReport{}, err
	}
	readObject = boundedSourceReader(project, readObject)
	stage = "published indexes"
	if err := retainPublishedIndexes(ctx, snapshot.Index, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "artifact text"
	if err := snapshot.Index.attachArtifactTextProofs(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "context"
	if err := snapshot.Index.attachContextSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "generation"
	if err := snapshot.Index.attachGenerationContextSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	if err := snapshot.Index.attachGenerationOutputSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "tool"
	if err := snapshot.Index.attachCancelledReceiptSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	if err := snapshot.Index.attachToolSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "recipe"
	if err := snapshot.Index.attachRecipeSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "qualification"
	if err := snapshot.Index.attachQualificationSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "semantic review"
	if err := snapshot.Index.attachSemanticSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "import sources"
	if err := snapshot.Index.attachIngestSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	stage = "change plans"
	if err := snapshot.Index.attachChangePlanSources(ctx, project, materials, readObject); err != nil {
		return SourceRegistrationReport{}, err
	}
	// Unexpired nested objects may outlive an erased enclosing document. Their
	// exact original metadata/pins were verified above, not reconstructed from
	// an invented document or registered with the envelope's observation time.
	materials = append(materials, snapshot.Index.preservedAfterExpiry...)
	report.ExpiredReferences = len(snapshot.Index.expired)
	report.MaterialReferences = len(materials) + report.ExpiredReferences
	for _, v := range snapshot.Index.expired {
		if v.Storage == Blob {
			report.BlobReferences++
		}
		if v.Reference.ContentRoot {
			report.ContentRoots++
		}
	}
	for _, material := range materials {
		if material.Storage == Metadata {
			report.AccountingReferences++
		}
		if material.Storage == Blob {
			report.BlobReferences++
		}
		if material.ContentRoot {
			report.ContentRoots++
		}
	}
	stage = "reference journal"
	changes, err := registerMaterialReferences(ctx, tx, project, snapshot.Index, lifecycle, materials, snapshot.CapturedAt)
	if err != nil {
		return SourceRegistrationReport{}, err
	}
	report.NewReferences, report.UpdatedReferences, report.UnresolvedReferences = changes.added, changes.updated, changes.unresolved
	report.PinnedReferences, report.UnboundSources = changes.pinned, changes.unbound
	return report, nil
}

// At most four statements per 500 source columns, rather than several round
// trips per column. Every batch still belongs to the same project transaction.
func registerSourceBatch(ctx context.Context, tx *sql.Tx, project string, batch []SourceObservation, now time.Time) (int, int, error) {
	if len(batch) == 0 || len(batch) > 500 {
		return 0, 0, ErrSourceOwnership
	}
	wanted := make(map[string]SourceObservation, len(batch))
	ids := make([]any, 0, len(batch))
	valueArgs := make([]any, 0, 3*len(batch))
	for _, source := range batch {
		if _, duplicate := wanted[source.ID]; duplicate {
			return 0, 0, ErrSourceOwnership
		}
		wanted[source.ID] = source
		ids = append(ids, source.ID)
		valueArgs = append(valueArgs, source.ID, source.PayloadHash, source.Null)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
	rows, err := tx.QueryContext(ctx, "SELECT id,project_id,owner_table,owner_key,field_key,time_basis,CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE id IN ("+placeholders+") FOR UPDATE", ids...)
	if err != nil {
		return 0, 0, err
	}
	first := make(map[string]time.Time, len(batch))
	for rows.Next() {
		var id, storedProject, table, owner, field, basis, clock string
		if err := rows.Scan(&id, &storedProject, &table, &owner, &field, &basis, &clock); err != nil {
			rows.Close()
			return 0, 0, err
		}
		source, exists := wanted[id]
		at, parseErr := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
		if !exists || !first[id].IsZero() || parseErr != nil || at.IsZero() || at.After(now) || basis != string(LegacyRegistered) || storedProject != project || table != source.Table || owner != source.OwnerKey || field != source.Field {
			rows.Close()
			return 0, 0, ErrSourceOwnership
		}
		first[id] = at
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, err
	}
	tuples := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(batch)), ",")
	rows, err = tx.QueryContext(ctx, "SELECT source_id,payload_hash,is_null,CAST(first_verified_at AS CHAR) FROM retention_source_values WHERE (source_id,payload_hash,is_null) IN ("+tuples+") FOR UPDATE", valueArgs...)
	if err != nil {
		return 0, 0, err
	}
	values := make(map[string]bool, len(batch))
	for rows.Next() {
		var id, hash, clock string
		var isNull bool
		if err := rows.Scan(&id, &hash, &isNull, &clock); err != nil {
			rows.Close()
			return 0, 0, err
		}
		source, exists := wanted[id]
		at, parseErr := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
		if !exists || values[id] || first[id].IsZero() || source.PayloadHash != hash || source.Null != isNull || parseErr != nil || at.IsZero() || at.Before(first[id]) || at.After(now) {
			rows.Close()
			return 0, 0, ErrSourceOwnership
		}
		values[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, err
	}
	var slotTuples, valueTuples []string
	var slotInsert, valueInsert []any
	for _, source := range batch {
		if first[source.ID].IsZero() {
			slotTuples = append(slotTuples, "(?,?,?,?,?,'LEGACY_REGISTERED',?)")
			slotInsert = append(slotInsert, source.ID, project, source.Table, source.OwnerKey, source.Field, lifecycleSQLTime(now))
		}
		if !values[source.ID] {
			valueTuples = append(valueTuples, "(?,?,?,?)")
			valueInsert = append(valueInsert, source.ID, source.PayloadHash, source.Null, lifecycleSQLTime(now))
		}
	}
	if len(slotTuples) > 0 {
		if _, err := tx.ExecContext(ctx, "INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES "+strings.Join(slotTuples, ","), slotInsert...); err != nil {
			return 0, 0, err
		}
	}
	if len(valueTuples) > 0 {
		if _, err := tx.ExecContext(ctx, "INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES "+strings.Join(valueTuples, ","), valueInsert...); err != nil {
			return 0, 0, err
		}
	}
	return len(slotTuples), len(valueTuples), nil
}
