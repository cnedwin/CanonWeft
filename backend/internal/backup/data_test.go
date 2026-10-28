package backup

import (
	"bytes"

	"strings"
	"testing"
	"time"
)

func emptyArchive(t *testing.T) *Archive {
	t.Helper()
	m, err := migrationCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for i := range m {
		m[i].Origin = "EXECUTED"
	}
	a := &Archive{Manifest: Manifest{Format: 1, Schema: supportedSchemaVersion, Source: Source{"11111111-1111-1111-1111-111111111111", "studio_test"}, SnapshotAt: time.Now().UTC(), Migrations: m, Exclusions: exclusions, Authority: &Provenance{LedgerID: strings.Repeat("f", 32), Protocol: 1}}, Data: Data{}, Blobs: map[string][]byte{}}
	for _, s := range tableCatalog {
		a.Data[s.name] = nil
	}
	return a
}
func testRow(a *Archive, name string, values map[string]string) {
	for _, s := range tableCatalog {
		if s.name != name {
			continue
		}
		r := make(Row, len(s.cols()))
		for k, v := range values {
			s.set(r, k, v)
		}
		a.Data[name] = append(a.Data[name], r)
		return
	}
	panic(name)
}
func archiveBytes(t *testing.T, a *Archive) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := writeArchive(&b, a); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestArchiveRoundTripAndSchemaRefusals(t *testing.T) {
	a := emptyArchive(t)
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Archive){func(a *Archive) { a.Manifest.Schema = supportedSchemaVersion + 1 }, func(a *Archive) { a.Manifest.Migrations[0].SHA256 = strings.Repeat("0", 64) }, func(a *Archive) { a.Data["unknown; DROP DATABASE studio"] = nil }, func(a *Archive) { delete(a.Data, "projects") }} {
		a = emptyArchive(t)
		mutation(a)
		raw = archiveBytes(t, a)
		if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Fatal("unsafe schema accepted")
		}
	}
}
func TestArchiveRejectsMissingAndCorruptReferencedBlobs(t *testing.T) {
	a := emptyArchive(t)
	project := strings.Repeat("a", 32)
	body := []byte("immutable body")
	hash := digest(body)
	testRow(a, "projects", map[string]string{"id": project})
	testRow(a, "resources", map[string]string{"project_id": project, "content_hash": hash})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("missing reference accepted")
	}
	a.Blobs["blobs/"+project+"/"+hash] = body
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	a.Blobs["blobs/"+project+"/"+hash] = []byte("corrupted")
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("corrupt blob accepted")
	}
}
func TestSecretsAndSideEffectEligibilityCannotSurviveRestore(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "users", map[string]string{"password_hash": "password-canary", "enabled": "1"})
	testRow(a, "sessions", map[string]string{"token_hash": "session-canary"})
	testRow(a, "generation_tasks", map[string]string{"credential_ref": "vault-canary", "state": "RUNNING", "frozen": "{}"})
	testRow(a, "model_calls", map[string]string{"state": "DISPATCHED", "reserved_micros": "420"})
	testRow(a, "tool_trials", map[string]string{"state": "RUNNING", "lease_id": "lease-canary", "frozen": "{}"})
	sanitize(a.Data)

	for _, s := range []string{"password-canary", "session-canary", "vault-canary", "lease-canary"} {
		for _, rows := range a.Data {
			for _, row := range rows {
				for _, cell := range row {
					if string(cell) == s {
						t.Fatal("secret retained", s)
					}
				}
			}
		}
	}

	for _, s := range tableCatalog {
		for _, r := range a.Data[s.name] {
			switch s.name {
			case "users":
				if s.value(r, "enabled") != "0" {
					t.Fatal("user enabled")
				}
			case "generation_tasks", "tool_trials":
				if s.value(r, "state") != "CANCELLED" {
					t.Fatal("job may resume")
				}
			case "model_calls":
				if s.value(r, "state") != "UNKNOWN" || s.value(r, "reserved_micros") != "420" {
					t.Fatal("unknown fee lost")
				}
			}
		}
	}
}

func TestArchiveRejectsMalformedRelationIdentity(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "users", map[string]string{"id": "../../outside", "workspace_id": "not-a-project-id", "enabled": "0"})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("malformed relation identity accepted")
	}
}

func TestArchiveRejectsMalformedReceiptObjectReferences(t *testing.T) {
	for _, payload := range []string{`{}`, `["SUCCEEDED",1,"receipt"]`, `["SUCCEEDED",1,"receipt",null]`, `["SUCCEEDED",1,"receipt",123]`, `["SUCCEEDED",1,"receipt","../escape"]`} {
		t.Run(payload, func(t *testing.T) {
			a := emptyArchive(t)
			project, call := strings.Repeat("a", 32), strings.Repeat("b", 32)
			testRow(a, "projects", map[string]string{"id": project})
			testRow(a, "model_calls", map[string]string{"id": call, "project_id": project})
			testRow(a, "model_call_receipts", map[string]string{"id": strings.Repeat("c", 64), "call_id": call, "payload": payload})
			raw := archiveBytes(t, a)
			if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
				t.Fatal("malformed receipt object reference accepted")
			}
		})
	}
}
