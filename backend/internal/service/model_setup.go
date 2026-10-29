package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"os"
	"time"
)

// Configuration diagnostics are advisory. They neither reserve money nor grant
// generation, agent qualification or publishing permission.
type ModelSetupReport struct {
	CheckedAt       string           `json:"checked_at"`
	Available       bool             `json:"available"`
	NetworkDisabled bool             `json:"network_disabled"`
	Budget          ModelSetupBudget `json:"budget"`
	Roles           []ModelRoleSetup `json:"roles"`
}

type ModelSetupBudget struct {
	Currency   string `json:"currency"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Available  int64  `json:"available_micros"`
	PerCall    int64  `json:"per_call_micros"`
}

type ModelRoleSetup struct {
	Role            string `json:"role"`
	Code            string `json:"code"`
	ConnectionID    string `json:"connection_id,omitempty"`
	BoundRevision   int64  `json:"bound_revision,omitempty"`
	CurrentRevision int64  `json:"current_revision,omitempty"`
	PriceID         string `json:"price_id,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
}

// Secret references never leave the server; equality preserves valid pinned
// versions while detecting a changed or cleared current credential.
type setupModel struct {
	ID            string
	Revision      int64
	Config        domain.ModelConfig
	CredentialRef string
}

func (m setupModel) valid() bool {
	return m.ID != "" && m.Revision > 0 && m.Config.ID == m.ID && m.Config.Revision == m.Revision &&
		m.Config.HasKey == (m.CredentialRef != "") && domain.ValidateModelConfig(m.Config) == nil
}

type setupPrice struct {
	ID         string
	Connection string
	Revision   int64
	Raw        []byte
	Hash       string
}

func setupBudget(b Budget) ModelSetupBudget {
	const maximum = int64(1000000000000)
	valid := b.Revision > 0 && (b.Currency == "USD" || b.Currency == "CNY") &&
		b.Limit > 0 && b.Limit <= maximum && b.PerCall > 0 && b.PerCall <= b.Limit &&
		b.Spent >= 0 && b.Spent <= maximum && b.Reserved >= 0 && b.Reserved <= maximum
	result := ModelSetupBudget{Currency: b.Currency, Enabled: b.Enabled, Configured: valid, PerCall: b.PerCall}
	if valid {
		result.Available = max(0, b.Limit-b.Spent-b.Reserved)
	}
	return result
}

func setupRole(role string, binding *domain.ModelBinding, current, pinned setupModel, prices []setupPrice, currency string) ModelRoleSetup {
	r := ModelRoleSetup{Role: role, Code: "BINDING_MISSING"}
	if binding == nil {
		return r
	}
	r.ConnectionID, r.BoundRevision, r.CurrentRevision = binding.ConnectionID, binding.ConnectionRevision, current.Revision
	switch {
	case current.ID == "":
		r.Code = "CONNECTION_MISSING"
	case !current.valid() || !pinned.valid() || binding.ConnectionID != pinned.ID || binding.ConnectionRevision != pinned.Revision || binding.Revision < 1 || !pinned.Config.Enabled:
		r.Code = "BINDING_INVALID"
	case !current.Config.Enabled:
		r.Code = "CONNECTION_DISABLED"
	case current.CredentialRef == "" || pinned.CredentialRef == "":
		r.Code = "KEY_MISSING"
	case current.CredentialRef != pinned.CredentialRef:
		r.Code = "KEY_CHANGED"
	default:
		images := role == "image_generator"
		if images && !((domain.SupportsImageOperation(pinned.Config, false, false) && domain.SupportsImageOperation(current.Config, false, false)) ||
			(domain.SupportsImageOperation(pinned.Config, true, false) && domain.SupportsImageOperation(current.Config, true, false))) {
			r.Code = "IMAGE_UNSUPPORTED"
			return r
		}
		r.Code = "PRICE_MISSING"
		for _, entry := range prices {
			if entry.Connection != pinned.ID || entry.Revision != pinned.Revision {
				continue
			}
			var price PriceCard
			if json.Unmarshal(entry.Raw, &price) != nil {
				r.Code = "PRICE_UNVERIFIED"
				return r
			}
			canonical, _ := json.Marshal(price)
			if digest(string(canonical)) != entry.Hash || price.ID != entry.ID || price.ConnectionID != entry.Connection || price.ConnectionRevision != entry.Revision {
				r.Code = "PRICE_UNVERIFIED"
				return r
			}
			if (price.Image != nil) != images {
				continue
			}
			if price.Validate() != nil {
				expires, err := time.Parse(time.RFC3339, price.ExpiresAt)
				r.Code = "PRICE_INVALID"
				if err == nil && !expires.After(time.Now()) {
					r.Code = "PRICE_EXPIRED"
				}
				continue // The generator can use an older still-valid price.
			}
			r.PriceID, r.ExpiresAt = price.ID, price.ExpiresAt
			r.Code = "CONFIGURED"
			if price.Currency != currency {
				r.Code = "CURRENCY_MISMATCH"
			}
			return r
		}
	}
	return r
}

func modelSetupTx(ctx context.Context, tx *sql.Tx, workspace string, current map[string]setupModel, bindings []domain.ModelBinding, budget Budget) *ModelSetupReport {
	r := &ModelSetupReport{CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), NetworkDisabled: os.Getenv("STUDIO_PROVIDER_NETWORK") == "disabled", Budget: setupBudget(budget), Roles: []ModelRoleSetup{}}
	// Diagnostic failure must not turn otherwise readable settings into an error,
	// nor disclose database or credential details to the client.
	pinned := map[string]setupModel{}
	rows, err := tx.QueryContext(ctx, `SELECT b.role,v.connection_id,v.revision,v.config,v.credential_ref FROM model_bindings b
 JOIN model_connections c ON c.id=b.connection_id JOIN model_connection_versions v ON v.connection_id=c.id AND v.revision=b.connection_revision
 WHERE b.workspace_id=? AND c.workspace_id=?`, workspace, workspace)
	if err != nil {
		return r
	}
	for rows.Next() {
		var role string
		var raw []byte
		var m setupModel
		if err = rows.Scan(&role, &m.ID, &m.Revision, &raw, &m.CredentialRef); err != nil {
			rows.Close()
			return r
		}
		if json.Unmarshal(raw, &m.Config) != nil {
			m.Config = domain.ModelConfig{} // Reject partially decoded versions too.
		}
		pinned[role] = m
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r
	}
	rows, err = tx.QueryContext(ctx, `SELECT p.id,p.connection_id,p.connection_revision,p.payload,p.content_hash FROM model_prices p
 JOIN model_bindings b ON b.connection_id=p.connection_id AND b.connection_revision=p.connection_revision
 JOIN model_connections c ON c.id=b.connection_id
 WHERE p.workspace_id=? AND b.workspace_id=? AND c.workspace_id=? ORDER BY p.created_at DESC,p.id`, workspace, workspace, workspace)
	if err != nil {
		return r
	}
	prices := []setupPrice{}
	seen := map[string]bool{}
	for rows.Next() {
		var p setupPrice
		if err = rows.Scan(&p.ID, &p.Connection, &p.Revision, &p.Raw, &p.Hash); err != nil {
			rows.Close()
			return r
		}
		if !seen[p.ID] {
			prices = append(prices, p)
			seen[p.ID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r
	}
	byRole := map[string]*domain.ModelBinding{}
	for i := range bindings {
		byRole[bindings[i].Role] = &bindings[i]
	}
	for _, role := range domain.ModelRoles() {
		var m setupModel
		if b := byRole[role]; b != nil {
			m = current[b.ConnectionID]
		}
		r.Roles = append(r.Roles, setupRole(role, byRole[role], m, pinned[role], prices, budget.Currency))
	}
	r.Available = true
	return r
}
