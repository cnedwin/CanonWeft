package retention

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"novelstudio/internal/authority"
)

type InventoryStore interface {
	ReadObject(context.Context, string, string) ([]byte, error)
	InventoryObjects(context.Context, string) ([]PhysicalObject, error)
}

type PhysicalObject struct {
	Hash string
	Size int64
}

type ProjectScan struct {
	Inventory Inventory
	Plan      Plan
}

// BeginProjectScanTx invalidates a previous checkpoint before any expensive IO.
// The authorized caller commits this small transaction, then starts a fresh
// transaction for CompleteProjectScanTx. Failed scans therefore cannot leave a
// previously CURRENT inventory usable. This never expires content.
func BeginProjectScanTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string) (int64, error) {
	if tx == nil || !id32.MatchString(project) {
		return 0, ErrInvalidInventory
	}
	if err := verifyScanSchema(ctx, tx); err != nil {
		return 0, err
	}
	if err := verifyRegistrationCatalog(ctx, tx); err != nil {
		return 0, err
	}
	if _, err := RegisterProjectLifecycle(ctx, tx, ledger, project); err != nil {
		return 0, err
	}
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&state); err != nil {
		return 0, err
	}
	if state == "PURGING" || state == "PURGED" {
		return 0, ErrInvalidInventory
	}
	if err := invalidateCapturedInventory(ctx, tx, project); err != nil {
		return 0, err
	}
	lifecycle, _, found, err := readRegisteredLifecycle(ctx, tx, project)
	if err != nil || !found {
		return 0, ErrInvalidInventory
	}
	return lifecycle.Revision, nil
}

// CompleteProjectScanTx holds project/source/object locks until caller commit.
// It accepts only the committed epoch from BeginProjectScanTx, not a browser's
// Complete flag. Every failure requires rollback; no physical delete occurs.
func CompleteProjectScanTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, epoch int64, store InventoryStore) (result ProjectScan, err error) {
	return completeProjectScanTx(ctx, tx, ledger, project, epoch, store, nil)
}

func completeProjectScanTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, epoch int64, store InventoryStore, claim *ObjectDeleteClaim) (result ProjectScan, err error) {
	stage := "admission"
	defer func() {
		if err != nil {
			err = fmt.Errorf("complete inventory %s: %w", stage, err)
		}
	}()
	if tx == nil || store == nil || epoch < 1 || !id32.MatchString(project) {
		return result, ErrInvalidInventory
	}
	if err := verifyScanSchema(ctx, tx); err != nil {
		return result, err
	}
	lifecycle, err := RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil || lifecycle.Revision != epoch {
		return result, ErrInvalidInventory
	}
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&state); err != nil || state != "UNSCANNED" {
		return result, ErrInvalidInventory
	}
	stage = "source references"
	registration, err := RegisterSourceObservations(ctx, tx, ledger, project, store.ReadObject)
	if err != nil {
		return result, err
	}
	if registration.UnresolvedReferences != 0 || registration.UnboundSources != 0 {
		return result, ErrInvalidInventory
	}
	stage = "physical objects"
	physical, err := store.InventoryObjects(ctx, project)
	if err != nil {
		return result, err
	}
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return result, err
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil || now.IsZero() {
		return result, ErrInvalidInventory
	}
	stage = "object observations"
	objects, err := registerScannedObjectsWithClaim(ctx, tx, project, physical, now, claim)
	if err != nil {
		return result, err
	}
	stage = "reference history"
	current, err := readReferenceVersions(ctx, tx, project, now)
	if err != nil {
		return result, err
	}
	lifecycle, _, found, err := readRegisteredLifecycle(ctx, tx, project)
	if err != nil || !found {
		return result, ErrInvalidInventory
	}
	history, err := readPinnedReferenceHistory(ctx, tx, project, current, lifecycle.Lifecycle, now)
	if err != nil {
		return result, err
	}
	tables, err := liveSourceTables(ctx, tx)
	if err != nil {
		return result, err
	}
	catalogVersion := 1
	if len(tables) != len(KnownTables()) {
		catalogVersion = 2
	}
	if len(tables) == len(KnownTablesForSchema(38)) {
		catalogVersion = 3
	}
	inventory := Inventory{ProjectID: project, Revision: lifecycle.Revision, CatalogVersion: catalogVersion, CoveredTables: tables, CapturedAt: now, Complete: true, Lifecycle: lifecycle.Lifecycle, Objects: objects}
	for _, version := range current {
		inventory.References = append(inventory.References, version.Reference)
	}
	for key, version := range history {
		if head, ok := current[key.ID]; !ok || head.Reference.Revision != key.Revision {
			inventory.HistoricalReferences = append(inventory.HistoricalReferences, version.Reference)
		}
	}
	stage = "plan validation"
	plan, err := BuildPlan(inventory, now)
	if err != nil {
		return result, err
	}
	stage = "checkpoint"
	updated, err := tx.ExecContext(ctx, "UPDATE project_lifecycle SET state='CURRENT',catalog_version=?,scanned_at=?,updated_at=? WHERE project_id=? AND inventory_revision=? AND state NOT IN ('PURGING','PURGED')", catalogVersion, lifecycleSQLTime(now), lifecycleSQLTime(now), project, lifecycle.Revision)
	if err != nil {
		return result, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return result, ErrInvalidInventory
	}
	return ProjectScan{Inventory: inventory, Plan: plan}, nil
}
