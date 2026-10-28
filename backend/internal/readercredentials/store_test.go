package readercredentials

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProtectedCredentialLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("CurrentUser DPAPI requires Windows")
	}
	path := filepath.Join(t.TempDir(), "reader.dpapi")
	identity := Identity{Format: 1, Database: "source_test", ServerUUID: "12345678-1234-1234-1234-123456789abc", User: "backup_12345678901234567890", Host: "127.0.0.1", AuthoritySchema: "authority_test", GrantProfile: "log-reader-v1"}
	secret := New(identity, "reader:PrivateSecret@tcp(127.0.0.1:13306)/source_test")
	if strings.Contains(fmt.Sprintf("%v %+v %#v", secret, secret, secret), "PrivateSecret") {
		t.Fatal("formatting exposed credential")
	}
	hash, err := Create(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Contains(raw, []byte("PrivateSecret")) || len(hash) != 64 {
		t.Fatal("credential was not protected")
	}
	loaded, err := Load(path)
	if err != nil || loaded.Identity != identity || loaded.DSN() != secret.DSN() {
		t.Fatal("protected roundtrip failed")
	}
	if _, err = Create(path, New(identity, "different")); err == nil {
		t.Fatal("existing secret overwritten")
	}
	raw[len(raw)/2] ^= 1
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("tampered DPAPI accepted")
	}
}

func TestProtectedCredentialBoundsAndPublicMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.dpapi")
	if err := os.WriteFile(path, make([]byte, 32769), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversized credential accepted")
	}
	if _, err := CipherSHA256(path); err == nil {
		t.Fatal("oversized credential paired")
	}
	ready := filepath.Join(t.TempDir(), "reader.ready.json")
	if err := PublishReady(ready, []byte(`{"status":"READY"}`)); err != nil {
		t.Fatal(err)
	}
	if err := PublishReady(ready, []byte(`{"status":"READY","revision":2}`)); err != nil {
		t.Fatal("ready could not be atomically replaced", err)
	}
	if err := PublishReady(ready, make([]byte, 16385)); err == nil {
		t.Fatal("oversized public metadata accepted")
	}
	if err := PublishReady(ready+":stream", []byte(`{"status":"READY"}`)); err == nil {
		t.Fatal("alternate data stream accepted")
	}
	if runtime.GOOS == "windows" {
		if err := PublishReady(filepath.Join(filepath.Dir(ready), "NUL"), []byte(`{"status":"READY"}`)); err == nil {
			t.Fatal("reserved Windows device accepted")
		}
	}
	if err := RemoveReady(ready); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ready); !os.IsNotExist(err) {
		t.Fatal("failed setup READY could not be removed")
	}
}
