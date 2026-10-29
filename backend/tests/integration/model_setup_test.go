package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
	"time"
)

func TestModelSetupChecklistIsReadOnlyVersionedAndScoped(t *testing.T) {
	modelEnvironment(t)
	t.Setenv("STUDIO_PROVIDER_NETWORK", "disabled")
	b, db := setup(t)
	read := func(want string) map[string]any {
		t.Helper()
		all := b.request("GET", "/api/models", nil, 200)
		report, ok := all["setup"].(map[string]any)
		if !ok {
			t.Fatal("owner settings need a read-only setup report")
		}
		if report["available"] != true || report["network_disabled"] != true {
			t.Fatal("configuration check lost availability or network policy", report)
		}
		for _, raw := range report["roles"].([]any) {
			row := raw.(map[string]any)
			if row["role"] == "writer" {
				if row["code"] != want {
					t.Fatalf("writer code = %v, want %s", row["code"], want)
				}
				return row
			}
		}
		t.Fatal("writer missing")
		return nil
	}
	read("BINDING_MISSING") // Meaningful RED on the original endpoint.
	input := map[string]any{"provider": "deepseek", "name": "隔离配置", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-not-a-provider-key", "enabled": true, "revision": 0}
	id := b.request("POST", "/api/models", input, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": id, "connection_revision": 1, "revision": 0}, 200)
	read("PRICE_MISSING")
	card := service.PriceCard{ConnectionID: id, ConnectionRevision: 1, Currency: "USD", Input: 100, Output: 200, Source: "https://example.com/test-price", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	saved := b.request("POST", "/api/model-prices", card, 201)
	card.ID = saved["id"].(string)
	input["api_key"], input["revision"], input["name"] = "", 1, "仅改名保留旧版绑定"
	b.request("PUT", "/api/models/"+id, input, 200)
	row := read("CONFIGURED")
	if row["bound_revision"] != float64(1) || row["current_revision"] != float64(2) {
		t.Fatal("valid pinned version was not preserved", row)
	}
	var pinnedRaw []byte
	if err := db.QueryRow("SELECT config FROM model_connection_versions WHERE connection_id=? AND revision=1", id).Scan(&pinnedRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE model_connection_versions SET config=JSON_SET(config,'$.image_capabilities',CAST(? AS JSON)) WHERE connection_id=? AND revision=1", `{"image_generate":"malformed"}`, id); err != nil {
		t.Fatal(err)
	}
	read("BINDING_INVALID") // Partial JSON decoding must not imply valid settings.
	if _, err := db.Exec("UPDATE model_connection_versions SET config=? WHERE connection_id=? AND revision=1", pinnedRaw, id); err != nil {
		t.Fatal(err)
	}

	// Hold an UNKNOWN request in this disposable database, without any provider.
	project := createProject(b, "隔离预算", "setup-budget-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 80, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := svc.Reserve(ctx, actor, project, "setup-hold", 60, "USD", "fixture-only")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 80, Enabled: false, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	var auditBefore int
	if err = db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	all := b.request("GET", "/api/models", nil, 200)
	report := all["setup"].(map[string]any)
	budget := report["budget"].(map[string]any)
	if budget["available_micros"] != float64(40) || budget["enabled"] != false || budget["configured"] != true {
		t.Fatal("read changed or ignored unknown hold", budget)
	}
	var auditAfter int
	if err = db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	actual, err := svc.Read(ctx, actor.ID)
	if err != nil || actual.Reserved != 60 || actual.Enabled || auditBefore != auditAfter {
		t.Fatal("check wrote budget/audit", err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM model_calls WHERE id=?", call.ID).Scan(&state); err != nil || state != "UNKNOWN" {
		t.Fatal("unknown call changed", state, err)
	}

	updatePrice := func(hash string) {
		t.Helper()
		raw, e := json.Marshal(card)
		if e != nil {
			t.Fatal(e)
		}
		if hash == "" {
			sum := sha256.Sum256(raw)
			hash = hex.EncodeToString(sum[:])
		}
		if _, e = db.Exec("UPDATE model_prices SET payload=?,content_hash=? WHERE id=?", raw, hash, card.ID); e != nil {
			t.Fatal(e)
		}
	}
	card.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	updatePrice("")
	read("PRICE_EXPIRED")
	card.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	updatePrice("tampered")
	read("PRICE_UNVERIFIED")
	card.Currency = "CNY"
	updatePrice("")
	read("CURRENCY_MISMATCH")
	card.Currency = "USD"
	updatePrice("")
	// A price in another workspace must not make this binding configured.
	if _, err = db.Exec("INSERT INTO workspaces(id,name) VALUES('foreign-setup','foreign')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE model_prices SET workspace_id='foreign-setup' WHERE id=?", card.ID); err != nil {
		t.Fatal(err)
	}
	read("PRICE_MISSING")
	input["revision"], input["api_key"] = 2, "rotated-test-key-only"
	b.request("PUT", "/api/models/"+id, input, 200)
	read("KEY_CHANGED")
	input["revision"], input["api_key"], input["enabled"] = 3, "", false
	b.request("PUT", "/api/models/"+id, input, 200)
	read("CONNECTION_DISABLED")
	encoded, _ := json.Marshal(b.request("GET", "/api/models", nil, 200))
	for _, secret := range []string{"test-not-a-provider-key", "rotated-test-key-only", "credential_ref", "foreign-setup"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private metadata leaked")
		}
	}
	b.request("POST", "/api/members", map[string]any{"Username": "reader", "Password": "reader-password-123", "Role": "reader"}, 201)
	// A diagnostic catalog failure must not lock the owner's core settings.
	if _, err = db.Exec("RENAME TABLE model_prices TO model_setup_prices_held"); err != nil {
		t.Fatal(err)
	}
	restorePrices := true
	t.Cleanup(func() {
		if restorePrices {
			if _, restoreErr := db.Exec("RENAME TABLE model_setup_prices_held TO model_prices"); restoreErr != nil {
				t.Error(restoreErr)
			}
		}
	})
	failed := b.request("GET", "/api/models", nil, 200)
	if failed["setup"].(map[string]any)["available"] != false || len(failed["connections"].([]any)) != 1 {
		t.Fatal("diagnostic failure blocked core settings")
	}
	if _, err = db.Exec("RENAME TABLE model_setup_prices_held TO model_prices"); err != nil {
		t.Fatal(err)
	}
	restorePrices = false
	b.request("POST", "/api/login", map[string]any{"Username": "reader", "Password": "reader-password-123"}, 200)
	b.request("GET", "/api/models", nil, 403)
}
