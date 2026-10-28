package storage

import (
	"context"
	"io"
	"os"
)

func recoveryPreparedName(hash string) string { return "prepared-recovery-" + hash }

// Only RecoveryObjects with a durable beforeWrite journal uses this path.
// Its caller owns the actor and uncommitted target project locks throughout IO.
func (s *LocalRetentionStore) putRecoveryObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return BlobRef{}, err
	}
	ref, err := s.blobs.putObject(ctx, project, input, true)
	if err != nil {
		return BlobRef{}, err
	}
	if err := s.check(); err != nil {
		return BlobRef{}, err
	}
	return ref, nil
}

// ScanRecoveryObjects does not grant deletion authority. The coordinator must
// first verify the durable journal, storage namespace and never-committed target.
// Every entry is inspected before any deletion; unknown/legacy names fail closed.
func (s *LocalRetentionStore) ScanRecoveryObjects(ctx context.Context, project string, expected []BlobRef) ([]BlobRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	if len(expected) > maxInventoryObjects {
		return nil, ErrInventoryLimit
	}
	prepared := make(map[string]BlobRef, len(expected))
	for _, ref := range expected {
		name := recoveryPreparedName(ref.Hash)
		if !gcHash.MatchString(ref.Hash) || ref.Size < 0 || ref.Size > MaxBlobBytes {
			return nil, ErrInventoryInvalidKey
		}
		if _, duplicate := prepared[name]; duplicate {
			return nil, ErrInventoryConflict
		}
		prepared[name] = ref
	}
	objects, err := s.blobs.scanObjects(ctx, project, prepared)
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		ref, known := prepared[recoveryPreparedName(object.Hash)]
		if !known || object != ref {
			return nil, ErrInventoryConflict
		}
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return objects, nil
}

func scanRecoveryPrepared(ctx context.Context, parent *os.Root, ref BlobRef) (os.FileInfo, error) {
	name := recoveryPreparedName(ref.Hash)
	before, err := parent.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > ref.Size {
		return nil, ErrInventoryConflict
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrInventoryConflict
	}
	data, err := readBounded(ctx, file)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	current, err := parent.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) || int64(len(data)) != before.Size() {
		return nil, ErrInventoryConflict
	}
	// A crash may leave any prefix, including zero bytes. Fully written files
	// must still match their journal hash; no reconstruction from partial data.
	if int64(len(data)) == ref.Size && contentHash(data) != ref.Hash {
		return nil, ErrInventoryConflict
	}
	return current, nil
}

// DeleteRecoveryPrepared unlinks only the exact journal-derived temporary name.
// It never deletes the immutable hash path, a directory, or a linked target.
func (s *LocalRetentionStore) DeleteRecoveryPrepared(ctx context.Context, project string, ref BlobRef) error {
	if !validGCKey(project, ref.Hash) || ref.Size < 0 || ref.Size > MaxBlobBytes {
		return ErrGCInvalidKey
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.check(); err != nil {
		return err
	}
	s.gc.mu.RLock()
	defer s.gc.mu.RUnlock()
	before, err := s.gc.root.Lstat(project)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrGCUnavailable
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return ErrGCConflict
	}
	parent, err := s.gc.root.OpenRoot(project)
	if err != nil {
		return ErrGCUnavailable
	}
	defer parent.Close()
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return ErrGCConflict
	}
	name := recoveryPreparedName(ref.Hash)
	if _, err := parent.Lstat(name); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return ErrGCUnavailable
	}
	verified, err := scanRecoveryPrepared(ctx, parent, ref)
	if err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(verified, current) || current.Size() != verified.Size() || !current.ModTime().Equal(verified.ModTime()) {
		return ErrGCConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := parent.Remove(name); err != nil {
		return ErrGCUnavailable
	}
	if _, err := parent.Lstat(name); !os.IsNotExist(err) {
		return ErrGCConflict
	}
	return s.check()
}
