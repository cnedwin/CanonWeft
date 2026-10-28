package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type LocalBlobGC struct {
	mu   sync.RWMutex
	root *os.Root
}

func NewLocalBlobGC(root string) (*LocalBlobGC, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrGCUnavailable
	}
	clean := filepath.Clean(root)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !strings.EqualFold(clean, filepath.Clean(resolved)) {
		return nil, ErrGCUnavailable
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrGCUnavailable
	}
	bound, err := os.OpenRoot(clean)
	if err != nil {
		return nil, ErrGCUnavailable
	}
	actual, err := bound.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		bound.Close()
		return nil, ErrGCUnavailable
	}
	return &LocalBlobGC{root: bound}, nil
}
func (s *LocalBlobGC) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}
func (s *LocalBlobGC) DeleteObject(ctx context.Context, project, hash string) (GCResult, error) {
	if !validGCKey(project, hash) {
		return "", ErrGCInvalidKey
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return "", ErrGCUnavailable
	}
	info, err := s.root.Lstat(project)
	if os.IsNotExist(err) {
		return GCAbsent, nil
	}
	if err != nil {
		return "", ErrGCUnavailable
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrGCConflict
	}
	parent, err := s.root.OpenRoot(project)
	if err != nil {
		return "", ErrGCUnavailable
	}
	defer parent.Close()
	actual, err := parent.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return "", ErrGCConflict
	}
	before, err := parent.Lstat(hash)
	if os.IsNotExist(err) {
		return GCAbsent, nil
	}
	if err != nil {
		return "", ErrGCUnavailable
	}
	if !before.Mode().IsRegular() || before.Size() > MaxBlobBytes {
		return "", ErrGCConflict
	}
	file, err := parent.Open(hash)
	if err != nil {
		return "", ErrGCUnavailable
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, opened) {
		file.Close()
		return "", ErrGCConflict
	}
	body, readErr := readBounded(ctx, file)
	closeErr := file.Close()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if readErr != nil || closeErr != nil {
		return "", ErrGCUnavailable
	}
	if contentHash(body) != hash {
		return "", ErrGCConflict
	}
	current, err := parent.Lstat(hash)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
		return "", ErrGCConflict
	}
	// Directory handles confine this exact unlink even after a path rename.
	// Business writers still must share the retention executor's object lock.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := parent.Remove(hash); err != nil {
		return "", ErrGCUnavailable
	}
	if _, err := parent.Lstat(hash); !os.IsNotExist(err) {
		return "", ErrGCConflict
	}
	return GCRemoved, nil
}
