// Package readercredentials stores the dedicated local reader connection as
// raw CurrentUser DPAPI bytes. Credentials never enter public ready metadata.
package readercredentials

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrProtectedFile = errors.New("protected reader credential file is unavailable or invalid")

type Identity struct {
	Format          int    `json:"format"`
	Database        string `json:"database"`
	ServerUUID      string `json:"server_uuid"`
	User            string `json:"user"`
	Host            string `json:"host"`
	AuthoritySchema string `json:"authority_schema"`
	GrantProfile    string `json:"grant_profile"`
}

type Secret struct {
	Identity
	dsn string
}

func New(identity Identity, dsn string) Secret { return Secret{identity, dsn} }
func (s Secret) DSN() string                   { return s.dsn }
func (s Secret) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[protected log reader credential]")
}

type payload struct {
	Identity
	DSN string `json:"dsn"`
}

func safeParent(path string) (*os.Root, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", ErrProtectedFile
	}
	parent, name := filepath.Dir(absolute), filepath.Base(absolute)
	if !filepath.IsLocal(name) || strings.ContainsAny(name, `:\/`) {
		return nil, "", ErrProtectedFile
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(parent)) || name == "." {
		return nil, "", ErrProtectedFile
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", ErrProtectedFile
	}
	info, err := root.Lstat(name)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		root.Close()
		return nil, "", ErrProtectedFile
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, "", ErrProtectedFile
	}
	return root, name, nil
}

func readCipher(path string) ([]byte, error) {
	root, name, err := safeParent(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrProtectedFile
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil || len(raw) == 0 || len(raw) > 32768 {
		return nil, ErrProtectedFile
	}
	return raw, nil
}

func CipherSHA256(path string) (string, error) {
	raw, err := readCipher(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func Load(path string) (Secret, error) {
	raw, err := readCipher(path)
	if err != nil {
		return Secret{}, err
	}
	plain, err := protect(raw, true)
	if err != nil {
		return Secret{}, ErrProtectedFile
	}
	defer clear(plain)
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var value payload
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Format != 1 || value.DSN == "" || len(value.DSN) > 8192 {
		return Secret{}, ErrProtectedFile
	}
	return New(value.Identity, value.DSN), nil
}

// Create is exclusive. It never replaces an existing credential, even on error.
func Create(path string, secret Secret) (string, error) {
	if secret.Identity.Format != 1 || secret.dsn == "" || len(secret.dsn) > 8192 {
		return "", ErrProtectedFile
	}
	plain, err := json.Marshal(payload{secret.Identity, secret.dsn})
	if err != nil {
		return "", ErrProtectedFile
	}
	defer clear(plain)
	raw, err := protect(plain, false)
	if err != nil || len(raw) > 32768 {
		return "", ErrProtectedFile
	}
	root, name, err := safeParent(path)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", ErrProtectedFile
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			_ = root.Remove(name)
		}
	}()
	if privateFile(f, path) != nil {
		return "", ErrProtectedFile
	}
	if _, err = f.Write(raw); err != nil {
		return "", ErrProtectedFile
	}
	if f.Sync() != nil || f.Close() != nil {
		return "", ErrProtectedFile
	}
	success = true
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// PublishReady replaces only the explicitly named public metadata file.
func PublishReady(path string, raw []byte) error {
	if len(raw) == 0 || len(raw) > 16384 {
		return ErrProtectedFile
	}
	root, name, err := safeParent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	suffix := make([]byte, 12)
	if _, err = rand.Read(suffix); err != nil {
		return ErrProtectedFile
	}
	temporary := name + ".pending-" + hex.EncodeToString(suffix)
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrProtectedFile
	}
	defer func() { f.Close(); _ = root.Remove(temporary) }()
	if _, err = f.Write(raw); err != nil {
		return ErrProtectedFile
	}
	if f.Sync() != nil || f.Close() != nil {
		return ErrProtectedFile
	}
	if root.Rename(temporary, name) != nil {
		return ErrProtectedFile
	}
	return nil
}

func RemoveReady(path string) error {
	root, name, err := safeParent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrProtectedFile
	}
	return nil
}
