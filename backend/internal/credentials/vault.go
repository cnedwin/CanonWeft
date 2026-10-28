// Package credentials stores provider secrets outside the business database and blobs.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrVault = errors.New("凭据库不可用或凭据已损坏")

type Vault struct {
	root string
	aead cipher.AEAD
}

func Open(masterPath, ciphertextRoot string) (*Vault, error) {
	master, err := filepath.Abs(masterPath)
	if err != nil {
		return nil, ErrVault
	}
	root, err := filepath.Abs(ciphertextRoot)
	if err != nil {
		return nil, ErrVault
	}
	relative, err := filepath.Rel(root, master)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return nil, ErrVault
	}
	for _, dir := range []string{filepath.Dir(master), root} {
		if err = privateDirectory(dir); err != nil {
			return nil, ErrVault
		}
	}
	protected, err := os.ReadFile(master)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, ErrVault
		}
		protected, err = protect(key)
		clear(key)
		if err != nil {
			return nil, ErrVault
		}
		file, createErr := os.OpenFile(master, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(createErr, os.ErrExist) {
			protected, err = os.ReadFile(master)
		} else if createErr != nil {
			return nil, ErrVault
		} else {
			_, err = file.Write(protected)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil || len(protected) > 4096 {
		return nil, ErrVault
	}
	key, err := unprotect(protected)
	if err != nil || len(key) != 32 {
		return nil, ErrVault
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrVault
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrVault
	}
	return &Vault{root: root, aead: aead}, nil
}

func (v *Vault) Put(scope string, key []byte) (string, error) {
	if scope == "" || len(key) < 8 || len(key) > 8192 {
		return "", ErrVault
	}
	id := make([]byte, 32)
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(id); err != nil {
		return "", ErrVault
	}
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrVault
	}
	ref := hex.EncodeToString(id)
	data := v.aead.Seal(nonce, nonce, key, []byte("studio-vault-v1\x00"+scope+"\x00"+ref))
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return "", ErrVault
	}
	defer root.Close()
	file, err := root.OpenFile(ref, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", ErrVault
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", ErrVault
	}
	return ref, nil
}

func (v *Vault) Get(scope, ref string) ([]byte, error) {
	raw, err := hex.DecodeString(ref)
	if err != nil || len(raw) != 32 || scope == "" {
		return nil, ErrVault
	}
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return nil, ErrVault
	}
	defer root.Close()
	file, err := root.Open(ref)
	if err != nil {
		return nil, ErrVault
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 || len(data) < v.aead.NonceSize()+v.aead.Overhead() {
		return nil, ErrVault
	}
	n := v.aead.NonceSize()
	plain, err := v.aead.Open(nil, data[:n], data[n:], []byte("studio-vault-v1\x00"+scope+"\x00"+ref))
	if err != nil {
		return nil, ErrVault
	}
	return plain, nil
}
