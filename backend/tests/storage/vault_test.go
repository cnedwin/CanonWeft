package storage_test

import (
	"bytes"
	"novelstudio/internal/credentials"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultRestartScopeAndTamper(t *testing.T) {
	root := t.TempDir()
	master := filepath.Join(root, "master", "key.dpapi")
	objects := filepath.Join(root, "ciphertexts")
	vault, err := credentials.Open(master, objects)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("test-only-secret-never-a-real-key")
	ref, err := vault.Put("workspace-one", key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(objects, ref))
	if err != nil || bytes.Contains(stored, key) {
		t.Fatal("plaintext persisted", err)
	}
	vault, err = credentials.Open(master, objects)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := vault.Get("workspace-one", ref)
	if err != nil || !bytes.Equal(recovered, key) {
		t.Fatal("restart lost credential", err)
	}
	if _, err = vault.Get("workspace-two", ref); err == nil {
		t.Fatal("cross workspace decryption allowed")
	}
	stored[len(stored)-1] ^= 1
	if err = os.WriteFile(filepath.Join(objects, ref), stored, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Get("workspace-one", ref); err == nil {
		t.Fatal("tampered credential accepted")
	}
	if _, err = vault.Get("workspace-one", "../key.dpapi"); err == nil {
		t.Fatal("path traversal")
	}
}

func TestVaultMasterCannotBeStoredWithCiphertexts(t *testing.T) {
	root := t.TempDir()
	if _, err := credentials.Open(filepath.Join(root, "master.key"), root); err == nil {
		t.Fatal("master must be separate")
	}
}
