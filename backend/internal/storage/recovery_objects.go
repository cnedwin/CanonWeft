package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

type recoveryStore interface {
	BlobStore
	BlobInventory
	BlobGC
	RetentionNamespace() string
}

// RecoveryObjects confines compensation to a new, initially empty project
// namespace. The service must own its uncommitted project row throughout IO.
// It never deletes a prefix, an existing project or an unrecognized file.
type RecoveryObjects struct {
	mu            sync.Mutex
	store         recoveryStore
	project       string
	written       map[string]bool
	commitStarted bool
	closed        bool
	closeStore    func() error
	beforeWrite   func(context.Context, BlobRef) error
}

func NewRecoveryObjects(ctx context.Context, blobs BlobStore, project string) (*RecoveryObjects, error) {
	if !gcProject.MatchString(project) {
		return nil, ErrGCInvalidKey
	}
	result := &RecoveryObjects{project: project, written: map[string]bool{}}
	switch store := blobs.(type) {
	case LocalBlobs:
		root, err := store.openRoot(true)
		if err != nil {
			return nil, err
		}
		if err := root.Close(); err != nil {
			return nil, err
		}
		bound, err := NewLocalRetentionStore(store.Root)
		if err != nil {
			return nil, err
		}
		result.store, result.closeStore = bound, bound.Close
	case *LocalRetentionStore:
		result.store = store
	case *S3RetentionStore:
		result.store = store
	case *S3Blobs:
		gc, err := NewS3BlobGC(store)
		if err != nil {
			return nil, err
		}
		result.store = &S3RetentionStore{blobs: store, gc: gc, namespace: store.namespace}
	default:
		return nil, ErrGCUnavailable
	}
	objects, err := result.store.ScanObjects(ctx, project)
	if err != nil || len(objects) != 0 {
		_ = result.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrGCConflict
	}
	return result, nil
}

func (s *RecoveryObjects) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.commitStarted || project != s.project {
		return BlobRef{}, ErrGCConflict
	}
	data, err := readBounded(ctx, input)
	if err != nil {
		return BlobRef{}, err
	}
	// Record before IO: a failed write response may still have created bytes.
	hash := contentHash(data)
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx, BlobRef{Hash: hash, Size: int64(len(data))}); err != nil {
			return BlobRef{}, err
		}
	}
	s.written[hash] = true
	var ref BlobRef
	if writer, ok := s.store.(interface {
		putRecoveryObject(context.Context, string, io.Reader) (BlobRef, error)
	}); ok && s.beforeWrite != nil {
		ref, err = writer.putRecoveryObject(ctx, project, bytes.NewReader(data))
	} else {
		ref, err = s.store.PutObject(ctx, project, bytes.NewReader(data))
	}
	if err != nil {
		return BlobRef{}, err
	}
	if ref.Hash != hash || ref.Size != int64(len(data)) {
		return BlobRef{}, ErrGCConflict
	}
	return ref, nil
}

// Journal installs durable write-ahead recording before any bytes are copied.
func (s *RecoveryObjects) Journal(record func(context.Context, BlobRef) error) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.commitStarted || len(s.written) != 0 || s.beforeWrite != nil || record == nil {
		return "", ErrGCConflict
	}
	namespace := s.store.RetentionNamespace()
	if !gcHash.MatchString(namespace) {
		return "", ErrGCUnavailable
	}
	s.beforeWrite = record
	return namespace, nil
}

func (s *RecoveryObjects) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrGCUnavailable
	}
	return s.store.ReadObject(ctx, project, hash)
}

// BeginCommit permanently disables compensation before SQL Commit is called.
// A lost commit response is not evidence of rollback.
func (s *RecoveryObjects) BeginCommit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitStarted = true
}

// Discard requires the caller to have freshly verified that its original SQL
// transaction still owns the newly inserted, uncommitted project row. If that
// transaction has ended, cancellation or crash recovery must reconcile later.
func (s *RecoveryObjects) Discard(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.commitStarted {
		return ErrGCConflict
	}
	var failures []error
	for hash := range s.written {
		if _, err := s.store.DeleteObject(ctx, s.project, hash); err != nil {
			failures = append(failures, err)
		} else {
			delete(s.written, hash)
		}
	}
	return errors.Join(failures...)
}

func (s *RecoveryObjects) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.closeStore != nil {
		return s.closeStore()
	}
	return nil
}
