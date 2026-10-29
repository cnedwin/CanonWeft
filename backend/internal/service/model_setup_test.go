package service

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"testing"
	"time"
)

func TestModelSetupRoleDiagnostics(t *testing.T) {
	type fixture struct {
		role            string
		binding         *domain.ModelBinding
		current, pinned setupModel
		prices          []setupPrice
		currency        string
	}
	price := func(card PriceCard) setupPrice {
		raw, _ := json.Marshal(card)
		return setupPrice{ID: card.ID, Connection: card.ConnectionID, Revision: card.ConnectionRevision, Raw: raw, Hash: digest(string(raw))}
	}
	baseCard := PriceCard{ID: "p", ConnectionID: "c", ConnectionRevision: 1, Currency: "USD", Input: 100, Output: 200, Source: "https://example.com/price", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	for _, tc := range []struct {
		name, want string
		change     func(*fixture)
	}{
		{"valid pinned", "CONFIGURED", func(*fixture) {}},
		{"missing binding", "BINDING_MISSING", func(f *fixture) { f.binding = nil }},
		{"missing model", "CONNECTION_MISSING", func(f *fixture) { f.current = setupModel{} }},
		{"invalid identity", "BINDING_INVALID", func(f *fixture) { f.current.Config.ID = "other" }},
		{"malformed pinned", "BINDING_INVALID", func(f *fixture) { f.pinned.Config.Revision = 9 }},
		{"disabled current", "CONNECTION_DISABLED", func(f *fixture) { f.current.Config.Enabled = false }},
		{"cleared key", "KEY_MISSING", func(f *fixture) { f.current.Config.HasKey = false; f.current.CredentialRef = "" }},
		{"rotated key", "KEY_CHANGED", func(f *fixture) { f.current.CredentialRef = "new-ref" }},
		{"no price", "PRICE_MISSING", func(f *fixture) { f.prices = nil }},
		{"different binding price", "PRICE_MISSING", func(f *fixture) { f.prices[0].Revision = 2 }},
		{"hash corrupted", "PRICE_UNVERIFIED", func(f *fixture) { f.prices[0].Hash = "changed" }},
		{"payload identity corrupted", "PRICE_UNVERIFIED", func(f *fixture) {
			p := baseCard
			p.ID = "other"
			f.prices = []setupPrice{price(p)}
			f.prices[0].ID = "p"
		}},
		{"expired", "PRICE_EXPIRED", func(f *fixture) {
			p := baseCard
			p.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			f.prices = []setupPrice{price(p)}
		}},
		{"invalid rates", "PRICE_INVALID", func(f *fixture) { p := baseCard; p.Input = 0; f.prices = []setupPrice{price(p)} }},
		{"expired falls back", "CONFIGURED", func(f *fixture) {
			p := baseCard
			p.ID = "newer"
			p.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			f.prices = append([]setupPrice{price(p)}, f.prices...)
		}},
		{"latest valid currency does not silently fall back", "CURRENCY_MISMATCH", func(f *fixture) {
			p := baseCard
			p.ID = "newer"
			p.Currency = "CNY"
			f.prices = append([]setupPrice{price(p)}, f.prices...)
		}},
		{"image withdrawn", "IMAGE_UNSUPPORTED", func(f *fixture) {
			f.role = "image_generator"
			f.current.Config.Provider = "openai"
			f.current.Config.BaseURL = "https://api.openai.com/v1"
			f.current.Config.ImageCapabilities = &domain.ImageCapabilities{Input: true, Edit: true}
			f.pinned.Config.Provider = "openai"
			f.pinned.Config.BaseURL = "https://api.openai.com/v1"
			f.pinned.Config.ImageCapabilities = &domain.ImageCapabilities{Generate: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := domain.ModelConfig{ID: "c", Revision: 1, Enabled: true, HasKey: true, Provider: "deepseek", Name: "fixture", Model: "fixture", BaseURL: "https://api.deepseek.com"}
			f := fixture{role: "writer", binding: &domain.ModelBinding{Role: "writer", ConnectionID: "c", ConnectionRevision: 1, Revision: 1}, pinned: setupModel{ID: "c", Revision: 1, Config: config, CredentialRef: "old-ref"}, prices: []setupPrice{price(baseCard)}, currency: "USD"}
			f.current = f.pinned
			f.current.Revision = 2
			f.current.Config.Revision = 2
			tc.change(&f)
			if got := setupRole(f.role, f.binding, f.current, f.pinned, f.prices, f.currency); got.Code != tc.want {
				t.Fatalf("got %s, want %s", got.Code, tc.want)
			}
		})
	}
}

func TestModelSetupBudgetDoesNotReleaseHoldsOrOverflow(t *testing.T) {
	for _, tc := range []struct {
		b          Budget
		configured bool
		available  int64
	}{
		{Budget{Currency: "USD"}, false, 0},
		{Budget{Currency: "USD", Revision: 1, Limit: 100, PerCall: 80, Reserved: 60, Spent: 10}, true, 30},
		{Budget{Currency: "USD", Revision: 1, Limit: 100, PerCall: 80, Reserved: 100, Spent: 10}, true, 0},
		{Budget{Currency: "USD", Revision: 1, Limit: 100, PerCall: 80, Reserved: 1 << 62}, false, 0},
		{Budget{Currency: "USD", Revision: 1, Limit: 100, PerCall: 101}, false, 0},
	} {
		got := setupBudget(tc.b)
		if got.Configured != tc.configured || got.Available != tc.available || got.Enabled != tc.b.Enabled {
			t.Fatalf("unexpected snapshot: %+v", got)
		}
	}
}
