package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
)

type ResourceInput struct {
	BranchID    string `json:"branch_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
}

func resourceTx(ctx context.Context, tx *sql.Tx, project, id string) (domain.Resource, error) {
	var resource domain.Resource
	err := tx.QueryRowContext(ctx, "SELECT id,branch_id,name,kind,head_revision,content_hash,revision FROM resources WHERE id=? AND project_id=? FOR UPDATE", id, project).Scan(&resource.ID, &resource.BranchID, &resource.Name, &resource.Kind, &resource.Head, &resource.Hash, &resource.Revision)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	if err == nil {
		resource.Policy, err = sourcePolicyTx(ctx, tx, project, id, resource.Head)
	}
	if err == nil {
		resource.Dependencies, err = resourceDependenciesTx(ctx, tx, project, id, resource.Head)
	}
	return resource, err
}
func (s *IP) CreateResource(ctx context.Context, a domain.Actor, project string, input ResourceInput) (domain.Resource, error) {
	var resource domain.Resource
	input.Name = strings.TrimSpace(input.Name)
	if !validName(input.Name, 160) || !validName(input.OperationID, 120) || (input.Kind != "note" && input.Kind != "source") {
		return resource, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return resource, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, input.BranchID); err != nil {
		return resource, err
	}
	encoded, _ := json.Marshal(input)
	hash := digest("resource.create\x00" + project + "\x00" + string(encoded))
	var stored, result string
	stored, result, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if stored != hash {
			return resource, domain.ErrConflict
		}
		return resourceTx(ctx, tx, project, result)
	}
	if err != sql.ErrNoRows {
		return resource, err
	}
	resource = domain.Resource{ID: NewID(), BranchID: input.BranchID, Name: input.Name, Kind: input.Kind, Revision: 1}
	_, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", resource.ID, project, input.BranchID, input.Name, input.Kind)
	if err != nil {
		return resource, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'resource.create',?,?)", a.ID, input.OperationID, hash, resource.ID)
	if err != nil {
		return resource, err
	}
	if err = audit(ctx, tx, a, "resource.create", resource.ID); err != nil {
		return resource, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "resources", resource.ID); err != nil {
		return resource, err
	}
	return resource, tx.Commit()
}
func (s *IP) Resources(ctx context.Context, a domain.Actor, project, branch string) ([]domain.Resource, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,branch_id,name,kind,head_revision,content_hash,revision FROM resources WHERE project_id=? AND branch_id=? AND kind NOT IN ('novel_chapter_history','novel_manuscript_import','novel_manuscript_progress') ORDER BY created_at,id", project, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Resource{}
	for rows.Next() {
		var resource domain.Resource
		if err = rows.Scan(&resource.ID, &resource.BranchID, &resource.Name, &resource.Kind, &resource.Head, &resource.Hash, &resource.Revision); err != nil {
			return nil, err
		}
		result = append(result, resource)
	}
	return result, rows.Err()
}
func (s *IP) ReadResource(ctx context.Context, a domain.Actor, project, id string) (domain.Resource, error) {
	var resource domain.Resource
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return resource, err
	}
	defer tx.Rollback()
	resource, err = resourceTx(ctx, tx, project, id)
	if err != nil {
		return resource, err
	}
	if resource.Hash != "" {
		data, readErr := s.Blobs.ReadObject(ctx, project, resource.Hash)
		if readErr != nil {
			return resource, domain.ErrNotReady
		}
		resource.Text = string(data)
	}
	return resource, nil
}
