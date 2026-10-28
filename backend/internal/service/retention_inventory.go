package service

import (
	"context"
	"database/sql"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// RetentionInventoryReport exposes aggregate ownership only. Source identities,
// payload digests, names, values and credentials never leave the scanner.
type RetentionInventoryReport struct {
	State                    string    `json:"state"`
	CapturedAt               time.Time `json:"captured_at"`
	DeletionReady            bool      `json:"deletion_ready"`
	ProjectCount             int       `json:"project_count"`
	ProjectRecords           int       `json:"project_records"`
	SharedRecords            int       `json:"shared_records"`
	PrivateValueBytes        int64     `json:"private_value_bytes"`
	TablesChecked            int       `json:"tables_checked"`
	AdoptedContentReferences int       `json:"adopted_content_references"`
	AdoptedContentObjects    int       `json:"adopted_content_objects"`
	AuthorContentReferences  int       `json:"author_content_references"`
	AuthorContentObjects     int       `json:"author_content_objects"`
}

func (s *Workspace) RetentionInventory(ctx context.Context, actorID string) (RetentionInventoryReport, error) {
	var report RetentionInventoryReport
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	var workspaceID, role string
	err = tx.QueryRowContext(ctx, "SELECT workspace_id,role FROM users WHERE id=? AND enabled=TRUE", actorID).Scan(&workspaceID, &role)
	if err == sql.ErrNoRows || (err == nil && role != "owner") {
		return report, domain.ErrForbidden
	}
	if err != nil {
		return report, err
	}
	snapshot, err := retention.ScanSources(ctx, tx)
	if err != nil {
		return report, domain.ErrNotReady
	}
	for _, row := range snapshot.Index.Records() {
		owner, err := snapshot.Index.Owner(row.Table, row.Values)
		if err != nil {
			return RetentionInventoryReport{}, domain.ErrNotReady
		}
		if owner.WorkspaceID != workspaceID {
			continue
		}
		if owner.Shared {
			report.SharedRecords++
		} else {
			report.ProjectRecords++
		}
		if row.Table == "projects" {
			report.ProjectCount++
			refs, rootErr := snapshot.Index.AdoptedContentReferences(owner.ProjectID)
			if rootErr != nil {
				return RetentionInventoryReport{}, domain.ErrNotReady
			}
			report.AdoptedContentReferences += len(refs)
			objects := map[string]bool{}
			for _, ref := range refs {
				if ref.ObjectHash != "" {
					objects[ref.ObjectHash] = true
				}
			}
			report.AdoptedContentObjects += len(objects)
			authorRefs, rootErr := snapshot.Index.AuthorContentReferences(owner.ProjectID)
			if rootErr != nil {
				return RetentionInventoryReport{}, domain.ErrNotReady
			}
			report.AuthorContentReferences += len(authorRefs)
			authorObjects := map[string]bool{}
			for _, ref := range authorRefs {
				if ref.ObjectHash != "" {
					authorObjects[ref.ObjectHash] = true
				}
			}
			report.AuthorContentObjects += len(authorObjects)
		}
		spec, _ := retention.OwnerSpec(row.Table)
		for _, column := range spec.Columns {
			if column.Privacy != retention.Structural {
				report.PrivateValueBytes += int64(len(row.Values[column.Column]))
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return RetentionInventoryReport{}, err
	}
	// A long scan must not deliver its result after the actor has been disabled
	// or moved out of this workspace. This read observes current committed state.
	var authorized int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id=? AND workspace_id=? AND role='owner' AND enabled=TRUE", actorID, workspaceID).Scan(&authorized); err != nil {
		return RetentionInventoryReport{}, err
	}
	if authorized != 1 {
		return RetentionInventoryReport{}, domain.ErrForbidden
	}
	report.State = "INVENTORIED"
	report.CapturedAt = snapshot.CapturedAt
	report.TablesChecked = len(snapshot.Tables)
	// Ownership alone establishes no expiry or permission to remove a record.
	return report, nil
}
