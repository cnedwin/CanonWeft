package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
)

type GenerationOption struct {
	ImageCapabilities *domain.ImageCapabilities `json:"image_capabilities,omitempty"`
	Role              string                    `json:"role"`
	Name              string                    `json:"name"`
	Model             string                    `json:"model"`
	Price             PriceCard                 `json:"price"`
}

func (s *Generations) Options(ctx context.Context, a domain.Actor, project string, imageOnly ...bool) ([]GenerationOption, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT b.role,v.config,p.payload,current_v.config FROM model_bindings b
 JOIN model_connection_versions v ON v.connection_id=b.connection_id AND v.revision=b.connection_revision
 JOIN model_connections c ON c.id=b.connection_id JOIN model_connection_versions current_v ON current_v.connection_id=c.id AND current_v.revision=c.revision
 JOIN model_prices p ON p.connection_id=b.connection_id AND p.connection_revision=b.connection_revision
 WHERE b.workspace_id=? AND JSON_EXTRACT(current_v.config,'$.enabled')=TRUE AND current_v.credential_ref=v.credential_ref AND v.credential_ref<>'' ORDER BY p.created_at DESC,p.id`, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []GenerationOption{}
	seen := map[string]bool{}
	for rows.Next() {
		var option GenerationOption
		var configRaw, priceRaw, currentRaw []byte
		if err = rows.Scan(&option.Role, &configRaw, &priceRaw, &currentRaw); err != nil {
			return nil, err
		}
		var config, current domain.ModelConfig
		if json.Unmarshal(configRaw, &config) != nil || json.Unmarshal(priceRaw, &option.Price) != nil || json.Unmarshal(currentRaw, &current) != nil {
			return nil, domain.ErrNotReady
		}
		images := len(imageOnly) > 0 && imageOnly[0]
		if images {
			// A pinned version cannot restore capabilities withdrawn in settings.
			config.ImageCapabilities = &domain.ImageCapabilities{
				Input:      config.ImageCapabilities != nil && current.ImageCapabilities != nil && config.ImageCapabilities.Input && current.ImageCapabilities.Input,
				Generate:   domain.SupportsImageOperation(config, false, false) && domain.SupportsImageOperation(current, false, false),
				Edit:       domain.SupportsImageOperation(config, true, false) && domain.SupportsImageOperation(current, true, false),
				RegionEdit: domain.SupportsImageOperation(config, true, true) && domain.SupportsImageOperation(current, true, true),
			}
		}
		if images && !domain.SupportsImageOperation(config, false, false) && !domain.SupportsImageOperation(config, true, false) {
			continue
		}
		if seen[option.Role] || (option.Role == "image_generator") != images || (option.Price.Image != nil) != images || (images && config.Provider != "openai") || option.Price.Validate() != nil {
			continue
		}
		seen[option.Role] = true
		option.Name = config.Name
		option.Model = config.Model
		option.ImageCapabilities = config.ImageCapabilities
		result = append(result, option)
	}
	return result, rows.Err()
}

func (s *Generations) List(ctx context.Context, a domain.Actor, project, branch string, character ...string) ([]Generation, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	query := "SELECT " + generationColumns + " FROM generation_tasks WHERE project_id=? AND branch_id=? AND (actor_id=? OR ?)"
	args := []any{project, branch, a.ID, a.Role == "owner"}
	if len(character) > 0 && character[0] != "" {
		query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.image.character_id'))=?"
		args = append(args, character[0])
	}
	if len(character) > 1 && character[1] != "" {
		query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.creative.kind'))=?"
		args = append(args, character[1])
		if len(character) > 2 && character[2] != "" {
			if character[1] == "character_visual" {
				query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.creative.visual.character_id'))=?"
			} else if character[1] == "novel_setup" {
				query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.creative.setup_id'))=?"
			} else if character[1] == "novel_visual" {
				query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.creative.visual.novel.book_id'))=?"
			} else {
				return nil, domain.ErrValidation
			}
			args = append(args, character[2])
		}
	}
	if len(character) > 3 && character[3] != "" {
		query += " AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.image.novel.book_id'))=?"
		args = append(args, character[3])
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY created_at DESC,id LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Generation{}
	for rows.Next() {
		g, readErr := scanGeneration(rows)
		if readErr != nil && !errors.Is(readErr, domain.ErrMaterialExpired) {
			return nil, readErr
		}
		result = append(result, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		g := &result[i]
		if len(g.expiredMaterial) == 0 {
			continue
		}
		marker, err := runtimeRetentionTx(ctx, tx, project, "generation_tasks", g.ID, g.CallID, g.expiredMaterial)
		if err != nil {
			return nil, err
		}
		*g = Generation{ID: g.ID, ProjectID: project, BranchID: branch, State: g.State, CallID: g.CallID, Retention: marker}
	}
	return result, nil
}
func (s *Generations) Prices(ctx context.Context, actor string) ([]PriceCard, error) {
	tx, a, err := (&Models{Workspace: s.IP.Workspace}).owner(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM model_prices WHERE workspace_id=? ORDER BY created_at DESC,id", a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []PriceCard{}
	for _, id := range ids {
		p, readErr := priceTx(ctx, tx, a.WorkspaceID, id)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, p)
	}
	return result, nil
}

func (s *Generations) Control(ctx context.Context, a domain.Actor, project, id, action string) (Generation, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return Generation{}, err
	}
	defer tx.Rollback()
	g, err := generationTx(ctx, tx, project, id)
	if err != nil {
		return g, err
	}
	if !canControlGeneration(a, g) {
		return g, domain.ErrForbidden
	}
	if action != "pause" && action != "resume" && action != "cancel" {
		return g, domain.ErrValidation
	}
	if g.State == "SUCCEEDED" || g.State == "CANCELLED" || g.State == "REJECTED" || g.State == "INVALID_OUTPUT" {
		return g, nil
	}
	if action == "cancel" {
		if g.CallID != "" {
			if _, err = budgetTx(ctx, tx, g.WorkspaceID); err != nil {
				return g, err
			}
			if err = lockQualificationCallTx(ctx, tx, g.CallID); err != nil {
				return g, err
			}
			call, readErr := callTx(ctx, tx, g.WorkspaceID, project, g.CallID)
			if readErr != nil {
				return g, readErr
			}
			if err = captureCallSourcesTx(ctx, tx, project, g.CallID); err != nil {
				return g, err
			}
			if call.State == "RESERVED" {
				if err = accountQualificationCallTx(ctx, tx, g.CallID, "CANCELLED", 0, true); err != nil {
					return g, err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros-? WHERE workspace_id=?", call.Reserved, g.WorkspaceID); err != nil {
					return g, err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='CANCELLED',cancel_requested=TRUE WHERE id=?", g.CallID); err != nil {
					return g, err
				}
			} else if _, err = tx.ExecContext(ctx, "UPDATE model_calls SET cancel_requested=TRUE WHERE id=?", g.CallID); err != nil {
				return g, err
			}
		}
		g.State = "CANCELLED"
		g.Cancelled = true
	} else {
		if g.State != "PREPARED" && g.State != "QUEUED" {
			return g, domain.ErrConflict
		}
		g.Paused = action == "pause"
	}
	_, err = tx.ExecContext(ctx, "UPDATE generation_tasks SET state=?,paused=?,cancel_requested=? WHERE id=?", g.State, g.Paused, g.Cancelled, id)
	if err != nil {
		return g, err
	}
	if err = audit(ctx, tx, a, "generation."+action, id); err != nil {
		return g, err
	}
	if action == "cancel" && g.CallID != "" {
		if err = captureCallSourcesTx(ctx, tx, project, g.CallID); err != nil {
			return g, err
		}
	}
	return g, tx.Commit()
}

// Recover records only local knowledge. It cannot claim an unknown provider call
// failed or refund it. A later, verifiable receipt may settle the retained hold.
func (s *Generations) Recover(ctx context.Context, id string) (string, error) {
	g, err := scanGeneration(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+generationColumns+" FROM generation_tasks WHERE id=?", id))
	if err != nil {
		return "", err
	}
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, g.ProjectID); err != nil {
		return "", err
	}
	g, err = generationTx(ctx, tx, g.ProjectID, id)
	if err != nil {
		return "", err
	}
	switch g.State {
	case "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "RECONCILED", "CANCELLED", "FAILED":
		return g.State, nil
	}
	if g.Paused && g.State == "QUEUED" && !g.Cancelled {
		return "PAUSED", nil
	}
	state := "BLOCKED"
	if g.CallID != "" {
		if _, err = budgetTx(ctx, tx, g.WorkspaceID); err != nil {
			return "", err
		}
		if err = lockQualificationCallTx(ctx, tx, g.CallID); err != nil {
			return "", err
		}
		call, readErr := callTx(ctx, tx, g.WorkspaceID, g.ProjectID, g.CallID)
		if readErr != nil {
			return "", readErr
		}
		if err = captureCallSourcesTx(ctx, tx, g.ProjectID, g.CallID); err != nil {
			return "", err
		}
		switch call.State {
		case "DISPATCHED", "UNKNOWN":
			state = "UNKNOWN"
			if err = accountQualificationCallTx(ctx, tx, g.CallID, "UNKNOWN", call.Charged, false); err != nil {
				return "", err
			}
			_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='UNKNOWN' WHERE id=?", g.CallID)
		case "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED":
			state = call.State
		case "RESERVED":
			if err = accountQualificationCallTx(ctx, tx, g.CallID, "CANCELLED", 0, true); err != nil {
				return "", err
			}
			// No dispatch happened. Releasing this reservation cannot duplicate a charge.
			_, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros-? WHERE workspace_id=?", call.Reserved, g.WorkspaceID)
			if err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='CANCELLED',cancel_requested=TRUE WHERE id=?", g.CallID)
			}
		}
		if err != nil {
			return "", err
		}
	}
	if g.Cancelled {
		state = "CANCELLED"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE generation_tasks SET state=? WHERE id=?", state, id); err != nil {
		return "", err
	}
	if g.CallID != "" {
		if err = captureCallSourcesTx(ctx, tx, g.ProjectID, g.CallID); err != nil {
			return "", err
		}
	}
	if state == "SUCCEEDED" || state == "INVALID_OUTPUT" {
		if err = s.IP.captureGenerationSourceTx(ctx, tx, g.ProjectID, g.ID); err != nil {
			return "", err
		}
	}
	return state, tx.Commit()
}

func (s *Generations) Inspect(ctx context.Context, id string) (string, error) {
	g, readErr := scanGeneration(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+generationColumns+" FROM generation_tasks WHERE id=?", id))
	if readErr != nil {
		return "", readErr
	}
	if g.Frozen.SemanticReviewID != "" && g.State == "QUEUED" {
		var paused, cancelled, disabled bool
		if err := s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT paused,cancel_requested,restored_disabled FROM semantic_review_runs WHERE id=? AND project_id=?", g.Frozen.SemanticReviewID, g.ProjectID).Scan(&paused, &cancelled, &disabled); err != nil {
			return "", err
		}
		if paused && !cancelled && !disabled {
			return "PAUSED", nil
		}
	}
	if g.Frozen.RecipeRunID != "" && g.State == "QUEUED" {
		var paused, cancelled, disabled bool
		if err := s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT paused,cancel_requested,restored_disabled FROM recipe_runs WHERE id=? AND project_id=?", g.Frozen.RecipeRunID, g.ProjectID).Scan(&paused, &cancelled, &disabled); err != nil {
			return "", err
		}
		if cancelled || disabled {
			return "CANCELLED", nil
		}
		if paused {
			return "PAUSED", nil
		}
	}
	if g.Frozen.Capability != nil && g.Frozen.Capability.Mode == "EVALUATION" && g.State == "QUEUED" {
		var parentPaused, parentCancelled bool
		if err := s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT paused,cancelled FROM qualification_runs WHERE id=?", g.Frozen.Capability.EvaluationID).Scan(&parentPaused, &parentCancelled); err != nil {
			return "", err
		}
		if parentPaused && !parentCancelled {
			return "PAUSED", nil
		}
	}
	var state string
	var paused, cancelled bool
	err := s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT state,paused,cancel_requested FROM generation_tasks WHERE id=?", id).Scan(&state, &paused, &cancelled)
	if err == sql.ErrNoRows {
		return "", domain.ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if cancelled {
		return "CANCELLED", nil
	}
	if paused {
		return "PAUSED", nil
	}
	return state, nil
}
