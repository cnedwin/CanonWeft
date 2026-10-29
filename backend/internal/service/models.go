package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/credentials"
	"novelstudio/internal/domain"
	"os"
	"slices"
	"strings"
)

type Models struct{ Workspace *Workspace }

// Secret fields are accepted only in this write request, never stored as JSON.
type ModelInput struct {
	ImageCapabilities *domain.ImageCapabilities `json:"image_capabilities,omitempty"`
	Provider          string                    `json:"provider"`
	Name              string                    `json:"name"`
	Model             string                    `json:"model"`
	BaseURL           string                    `json:"base_url"`
	ProviderWorkspace string                    `json:"provider_workspace"`
	APIVersion        string                    `json:"api_version"`
	Enabled           bool                      `json:"enabled"`
	Revision          int64                     `json:"revision"`
	APIKey            string                    `json:"api_key"`
	ClearKey          bool                      `json:"clear_key"`
}

type ModelSettings struct {
	Setup       *ModelSetupReport       `json:"setup,omitempty"`
	Catalog     []domain.ProviderPreset `json:"catalog"`
	Connections []domain.ModelConfig    `json:"connections"`
	Bindings    []domain.ModelBinding   `json:"bindings"`
	Roles       []string                `json:"roles"`
	PaidEnabled bool                    `json:"paid_enabled"`
}

func (s *Models) owner(ctx context.Context, actorID string) (*sql.Tx, domain.Actor, error) {
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, domain.Actor{}, err
	}
	a, err := s.Workspace.actorTx(ctx, tx, actorID)
	if err == nil && a.Role != "owner" {
		err = domain.ErrForbidden
	}
	if err != nil {
		tx.Rollback()
		return nil, a, err
	}
	return tx, a, nil
}

func modelTx(ctx context.Context, tx *sql.Tx, workspace, id string) (domain.ModelConfig, string, error) {
	var config domain.ModelConfig
	var encoded, ref string
	err := tx.QueryRowContext(ctx, `SELECT v.config,v.credential_ref FROM model_connections c
 JOIN model_connection_versions v ON v.connection_id=c.id AND v.revision=c.revision
 WHERE c.workspace_id=? AND c.id=? FOR UPDATE`, workspace, id).Scan(&encoded, &ref)
	if err == sql.ErrNoRows {
		return config, "", domain.ErrForbidden
	}
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &config)
	}
	return config, ref, err
}

func openVault() (*credentials.Vault, error) {
	master, root := os.Getenv("STUDIO_VAULT_MASTER"), os.Getenv("STUDIO_VAULT_ROOT")
	if master == "" || root == "" {
		return nil, domain.ErrNotReady
	}
	return credentials.Open(master, root)
}

func (s *Models) Settings(ctx context.Context, actorID string) (ModelSettings, error) {
	result := ModelSettings{Catalog: domain.ProviderCatalog(), Connections: []domain.ModelConfig{}, Bindings: []domain.ModelBinding{}, Roles: domain.ModelRoles()}
	tx, a, err := s.owner(ctx, actorID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current := map[string]setupModel{}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.revision,v.config,v.credential_ref FROM model_connections c JOIN model_connection_versions v
 ON v.connection_id=c.id AND v.revision=c.revision WHERE c.workspace_id=? ORDER BY c.id`, a.WorkspaceID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var encoded string
		var config domain.ModelConfig
		var record setupModel
		if err = rows.Scan(&record.ID, &record.Revision, &encoded, &record.CredentialRef); err != nil {
			rows.Close()
			return result, err
		}
		if err = json.Unmarshal([]byte(encoded), &config); err != nil {
			rows.Close()
			return result, err
		}
		result.Connections = append(result.Connections, config)
		record.Config = config
		current[record.ID] = record
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT role,connection_id,connection_revision,revision FROM model_bindings WHERE workspace_id=? ORDER BY role", a.WorkspaceID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var binding domain.ModelBinding
		if err = rows.Scan(&binding.Role, &binding.ConnectionID, &binding.ConnectionRevision, &binding.Revision); err != nil {
			return result, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	budget, err := budgetTx(ctx, tx, a.WorkspaceID)
	result.PaidEnabled = budget.Enabled
	if err == nil {
		result.Setup = modelSetupTx(ctx, tx, a.WorkspaceID, current, result.Bindings, budget)
	}
	return result, err
}

func (s *Models) Save(ctx context.Context, actorID, id string, input ModelInput) (domain.ModelConfig, error) {
	config := domain.ModelConfig{ID: id, Provider: input.Provider, Name: strings.TrimSpace(input.Name), Model: strings.TrimSpace(input.Model), BaseURL: strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"), ProviderWorkspace: strings.TrimSpace(input.ProviderWorkspace), APIVersion: input.APIVersion, Enabled: input.Enabled, Revision: input.Revision + 1}
	config.ImageCapabilities = input.ImageCapabilities
	tx, a, err := s.owner(ctx, actorID)
	if err != nil {
		return config, err
	}
	defer tx.Rollback()
	if err = domain.ValidateModelConfig(config); err != nil {
		return config, err
	}
	key := strings.TrimSpace(input.APIKey)
	if input.Revision < 0 || (key != "" && (len(key) < 8 || len(key) > 8192 || strings.ContainsAny(key, "\r\n\x00"))) || (input.ClearKey && key != "") {
		return config, domain.ErrValidation
	}
	ref := ""
	if id != "" {
		previous, previousRef, readErr := modelTx(ctx, tx, a.WorkspaceID, id)
		if readErr != nil {
			return config, readErr
		}
		if previous.Revision != input.Revision {
			return config, domain.ErrConflict
		}
		if previous.Provider != config.Provider {
			return config, domain.ErrValidation
		}
		ref = previousRef
	} else {
		if input.Revision != 0 {
			return config, domain.ErrConflict
		}
		config.ID = NewID()
	}
	if input.ClearKey {
		ref = ""
	}
	if key != "" {
		vault, vaultErr := openVault()
		if vaultErr != nil {
			return config, domain.ErrNotReady
		}
		secret := []byte(key)
		ref, err = vault.Put(a.WorkspaceID, secret)
		clear(secret)
		if err != nil {
			return config, domain.ErrNotReady
		}
	}
	config.HasKey = ref != ""
	encoded, err := json.Marshal(config)
	if err != nil {
		return config, err
	}
	if id == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO model_connections(id,workspace_id,revision) VALUES(?,?,?)", config.ID, a.WorkspaceID, config.Revision)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE model_connections SET revision=? WHERE id=?", config.Revision, id)
	}
	if err != nil {
		return config, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO model_connection_versions(connection_id,revision,config,credential_ref) VALUES(?,?,?,?)", config.ID, config.Revision, string(encoded), ref)
	if err != nil {
		return config, err
	}
	if err = audit(ctx, tx, a, "model.configure", config.ID); err != nil {
		return config, err
	}
	return config, tx.Commit()
}

func (s *Models) Bind(ctx context.Context, actorID string, input domain.ModelBinding) (domain.ModelBinding, error) {
	tx, a, err := s.owner(ctx, actorID)
	if err != nil {
		return input, err
	}
	defer tx.Rollback()
	if !slices.Contains(domain.ModelRoles(), input.Role) || input.Revision < 0 {
		return input, domain.ErrValidation
	}
	config, _, err := modelTx(ctx, tx, a.WorkspaceID, input.ConnectionID)
	if err != nil {
		return input, err
	}
	if !config.Enabled || !config.HasKey || config.Revision != input.ConnectionRevision {
		return input, domain.ErrConflict
	}
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM model_bindings WHERE workspace_id=? AND role=? FOR UPDATE", a.WorkspaceID, input.Role).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return input, err
	}
	if current != input.Revision {
		return input, domain.ErrConflict
	}
	input.Revision++
	_, err = tx.ExecContext(ctx, `INSERT INTO model_bindings(workspace_id,role,connection_id,connection_revision,revision) VALUES(?,?,?,?,?)
 ON DUPLICATE KEY UPDATE connection_id=VALUES(connection_id),connection_revision=VALUES(connection_revision),revision=VALUES(revision)`, a.WorkspaceID, input.Role, input.ConnectionID, input.ConnectionRevision, input.Revision)
	if err != nil {
		return input, err
	}
	if err = audit(ctx, tx, a, "model.bind."+input.Role, input.ConnectionID); err != nil {
		return input, err
	}
	return input, tx.Commit()
}
