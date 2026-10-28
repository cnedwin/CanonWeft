package service

import (
	"context"
	"database/sql"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

type RetentionScanStore interface {
	storage.BlobStore
	storage.BlobInventory
}

type RetentionScanner struct {
	Workspace *Workspace
	Blobs     RetentionScanStore
}

// Keep storage implementations outside retention; storage also consumes the
// qualification ledger, which already depends on retention's proof types.
type inventorySource struct{ RetentionScanStore }

func (s inventorySource) InventoryObjects(ctx context.Context, project string) ([]retention.PhysicalObject, error) {
	objects, err := s.ScanObjects(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]retention.PhysicalObject, len(objects))
	for i, object := range objects {
		result[i] = retention.PhysicalObject{Hash: object.Hash, Size: object.Size}
	}
	return result, nil
}

// Only aggregate counts and a digest leave the scanner. A complete inventory
// is not permission to start a cleanup job or adopt a recovered project.
type RetentionScanReport struct {
	State             string    `json:"state"`
	Complete          bool      `json:"complete"`
	CapturedAt        time.Time `json:"captured_at"`
	InventoryRevision int64     `json:"inventory_revision"`
	InventoryHash     string    `json:"inventory_hash"`
	References        int       `json:"references"`
	Objects           int       `json:"objects"`
	Expiring          int       `json:"expiring"`
	RemovableObjects  int       `json:"removable_objects"`
	Blocked           int       `json:"blocked"`
	Overdue           bool      `json:"overdue"`
}

func (s *RetentionScanner) Scan(ctx context.Context, actorID, project string) (RetentionScanReport, error) {
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil || s.Blobs == nil {
		return RetentionScanReport{}, domain.ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	epoch, err := s.begin(ctx, actorID, project)
	if err != nil {
		return RetentionScanReport{}, err
	}
	tx, err := s.Workspace.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RetentionScanReport{}, err
	}
	defer tx.Rollback()
	if err := authorizeRetentionScan(ctx, tx, actorID, project); err != nil {
		return RetentionScanReport{}, err
	}
	scan, err := retention.CompleteProjectScanTx(ctx, tx, protectedLedger(), project, epoch, inventorySource{s.Blobs})
	if err != nil {
		return RetentionScanReport{}, domain.ErrNotReady
	}
	if err := tx.Commit(); err != nil {
		return RetentionScanReport{}, err
	}
	return RetentionScanReport{
		State: "CURRENT", Complete: true, CapturedAt: scan.Inventory.CapturedAt,
		InventoryRevision: scan.Inventory.Revision, InventoryHash: scan.Plan.InventoryHash,
		References: len(scan.Inventory.References), Objects: len(scan.Inventory.Objects),
		Expiring: len(scan.Plan.Expire), RemovableObjects: len(scan.Plan.DeleteObjects),
		Blocked: len(scan.Plan.Blocked), Overdue: scan.Plan.Overdue,
	}, nil
}

func (s *RetentionScanner) begin(ctx context.Context, actorID, project string) (int64, error) {
	tx, err := s.Workspace.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := authorizeRetentionScan(ctx, tx, actorID, project); err != nil {
		return 0, err
	}
	epoch, err := retention.BeginProjectScanTx(ctx, tx, protectedLedger(), project)
	if err != nil {
		return 0, domain.ErrNotReady
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return epoch, nil
}

func authorizeRetentionScan(ctx context.Context, tx *sql.Tx, actorID, project string) error {
	var workspace, role string
	err := tx.QueryRowContext(ctx, "SELECT workspace_id,role FROM users WHERE id=? AND enabled=TRUE FOR SHARE", actorID).Scan(&workspace, &role)
	if err == sql.ErrNoRows || (err == nil && role != "owner") {
		return domain.ErrForbidden
	}
	if err != nil {
		return err
	}
	var owned string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? AND workspace_id=? FOR UPDATE", project, workspace).Scan(&owned)
	if err == sql.ErrNoRows {
		return domain.ErrForbidden
	}
	return err
}
