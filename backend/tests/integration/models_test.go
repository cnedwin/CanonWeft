package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func modelEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STUDIO_VAULT_MASTER", filepath.Join(root, "master", "key.dpapi"))
	t.Setenv("STUDIO_VAULT_ROOT", filepath.Join(root, "cipher"))
}

func TestModelConfigurationPersistsWithoutLeakingKey(t *testing.T) {
	modelEnvironment(t)
	b, db := setup(t)
	catalog := b.request("GET", "/api/models", nil, 200)
	if len(catalog["catalog"].([]any)) != 5 {
		t.Fatal("five providers required")
	}
	input := map[string]any{"provider": "deepseek", "name": "创作模型", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-secret-not-a-real-provider-key", "enabled": true, "revision": 0}
	saved := b.request("POST", "/api/models", input, 201)
	id := saved["id"].(string)
	encoded, _ := json.Marshal(saved)
	if strings.Contains(string(encoded), "test-secret") || strings.Contains(string(encoded), "credential_ref") {
		t.Fatal("secret metadata leaked")
	}
	input["revision"] = 1
	input["api_key"] = ""
	input["name"] = "重命名后保留 Key"
	saved = b.request("PUT", "/api/models/"+id, input, 200)
	if !saved["has_key"].(bool) {
		t.Fatal("blank replaced credential")
	}
	b.request("PUT", "/api/models/"+id, input, 409)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": id, "connection_revision": 2, "revision": 0}, 200)
	listing := b.request("GET", "/api/models", nil, 200)
	if len(listing["bindings"].([]any)) != 1 {
		t.Fatal("binding not persisted")
	}
	var config string
	if err := db.QueryRow("SELECT config FROM model_connection_versions WHERE connection_id=? AND revision=1", id).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "test-secret") {
		t.Fatal("business DB contains plaintext")
	}
	input["revision"] = 2
	input["enabled"] = false
	b.request("PUT", "/api/models/"+id, input, 200)
	b.request("PUT", "/api/model-bindings/critic", map[string]any{"connection_id": id, "connection_revision": 3, "revision": 0}, 409)
}

func TestModelConfigurationRejectsUnsafeEndpointsAndReaders(t *testing.T) {
	modelEnvironment(t)
	b, _ := setup(t)
	for _, url := range []string{"http://api.deepseek.com", "https://127.0.0.1", "https://api.deepseek.com.evil.example", "https://key:secret@api.deepseek.com", "https://api.deepseek.com/?key=secret"} {
		b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "测试", "model": "deepseek-flash", "base_url": url, "enabled": true, "revision": 0}, 400)
	}
	b.request("POST", "/api/members", map[string]any{"Username": "reader", "Password": "reader-password-123", "Role": "reader"}, 201)
	b.request("POST", "/api/login", map[string]any{"Username": "reader", "Password": "reader-password-123"}, 200)
	b.request("GET", "/api/models", nil, 403)
	b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "测试", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "enabled": true}, 403)
}
