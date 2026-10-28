package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Retention stores are constructed separately from ordinary application stores.
// Their authority is deliberately not added to LocalBlobs/S3Blobs.
type LocalRetentionStore struct {
	mu        sync.RWMutex
	blobs     LocalBlobs
	gc        *LocalBlobGC
	identity  os.FileInfo
	storeID   string
	namespace string
	closed    bool
}

const retentionStoreIDFile = ".retention-store-id"

func NewLocalRetentionStore(root string) (*LocalRetentionStore, error) {
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		return nil, err
	}
	s := &LocalRetentionStore{blobs: LocalBlobs{Root: filepath.Clean(root)}, gc: gc}
	s.identity, err = gc.root.Stat(".")
	if err == nil {
		s.storeID, err = loadRetentionStoreID(gc.root)
	}
	if err != nil {
		gc.Close()
		return nil, err
	}
	canonical := s.blobs.Root
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	encoded, _ := json.Marshal([]string{"local-retention-v1", canonical, s.storeID})
	s.namespace = contentHash(encoded)
	if err := s.check(); err != nil {
		gc.Close()
		return nil, err
	}
	return s, nil
}

func loadRetentionStoreID(root *os.Root) (string, error) {
	info, err := root.Lstat(retentionStoreIDFile)
	if os.IsNotExist(err) {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			return "", err
		}
		file, openErr := root.OpenFile(retentionStoreIDFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr == nil {
			_, writeErr := file.WriteString(hex.EncodeToString(bytes))
			syncErr := file.Sync()
			closeErr := file.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil {
				return "", ErrGCUnavailable
			}
		} else if !os.IsExist(openErr) {
			return "", ErrGCUnavailable
		}
		info, err = root.Lstat(retentionStoreIDFile)
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != 64 {
		return "", ErrGCUnavailable
	}
	data, err := root.ReadFile(retentionStoreIDFile)
	if err != nil || !gcHash.Match(data) {
		return "", ErrGCUnavailable
	}
	return string(data), nil
}

func (s *LocalRetentionStore) check() error {
	if s.closed {
		return ErrGCUnavailable
	}
	actual, err := os.Lstat(s.blobs.Root)
	if err != nil || !actual.IsDir() || actual.Mode()&os.ModeSymlink != 0 || !os.SameFile(s.identity, actual) {
		return ErrGCConflict
	}
	resolved, err := filepath.EvalSymlinks(s.blobs.Root)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), s.blobs.Root) {
		return ErrGCConflict
	}
	info, err := s.gc.root.Lstat(retentionStoreIDFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 64 {
		return ErrGCConflict
	}
	identity, err := s.gc.root.ReadFile(retentionStoreIDFile)
	if err != nil || string(identity) != s.storeID {
		return ErrGCConflict
	}
	return nil
}

func (s *LocalRetentionStore) RetentionNamespace() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.check() != nil {
		return ""
	}
	return s.namespace
}
func (s *LocalRetentionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.gc.Close()
}
func (s *LocalRetentionStore) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return BlobRef{}, err
	}
	ref, err := s.blobs.PutObject(ctx, project, input)
	if err != nil {
		return BlobRef{}, err
	}
	if err := s.check(); err != nil {
		return BlobRef{}, err
	}
	return ref, nil
}
func (s *LocalRetentionStore) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	data, err := s.blobs.ReadObject(ctx, project, hash)
	if err != nil {
		return nil, err
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return data, nil
}
func (s *LocalRetentionStore) ScanObjects(ctx context.Context, project string) ([]BlobRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	objects, err := s.blobs.ScanObjects(ctx, project)
	if err != nil {
		return nil, err
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return objects, nil
}
func (s *LocalRetentionStore) DeleteObject(ctx context.Context, project, hash string) (GCResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return "", err
	}
	result, err := s.gc.DeleteObject(ctx, project, hash)
	if err != nil {
		return "", err
	}
	if err := s.check(); err != nil {
		return "", err
	}
	return result, nil
}

type S3RetentionStore struct {
	blobs     *S3Blobs
	gc        *S3BlobGC
	namespace string
}

func NewS3RetentionStore(config S3Config) (*S3RetentionStore, error) {
	blobs, err := NewS3Blobs(config)
	if err != nil {
		return nil, err
	}
	gc, err := NewS3BlobGC(blobs)
	if err != nil {
		return nil, err
	}
	return &S3RetentionStore{blobs: blobs, gc: gc, namespace: blobs.namespace}, nil
}
func (s *S3RetentionStore) RetentionNamespace() string { return s.namespace }
func (s *S3RetentionStore) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	return s.blobs.PutObject(ctx, project, input)
}
func (s *S3RetentionStore) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	return s.blobs.ReadObject(ctx, project, hash)
}
func (s *S3RetentionStore) ScanObjects(ctx context.Context, project string) ([]BlobRef, error) {
	return s.blobs.ScanObjects(ctx, project)
}
func (s *S3RetentionStore) DeleteObject(ctx context.Context, project, hash string) (GCResult, error) {
	return s.gc.DeleteObject(ctx, project, hash)
}
