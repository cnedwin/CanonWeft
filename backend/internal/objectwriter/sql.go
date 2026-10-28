// Package objectwriter coordinates immutable file IO with reference transactions.
package objectwriter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

// Write runs inside the caller's authorized transaction. Commit only after
// saving the business reference; roll back on any error. Failed transactions
// can leave unreachable files, never a committed reference to a failed write.
func Write(ctx context.Context, tx *sql.Tx, store storage.BlobStore, project string, data []byte) (storage.BlobRef, error) {
	if store == nil || int64(len(data)) > storage.MaxBlobBytes {
		return storage.BlobRef{}, retention.ErrInvalidInventory
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	var closeBound func() error
	defer func() {
		if closeBound != nil {
			_ = closeBound()
		}
	}()
	bind := func() (string, error) {
		binder, ok := store.(storage.RecreationWriter)
		if !ok {
			return "", retention.ErrInvalidInventory
		}
		bound, namespace, close, err := binder.BindRecreationWrite()
		closeBound = close
		if err != nil {
			return "", err
		}
		if bound == nil {
			return "", retention.ErrInvalidInventory
		}
		store = bound
		return namespace, nil
	}
	if err := retention.ProtectObjectWriteInNamespace(ctx, tx, project, hash, bind); err != nil {
		return storage.BlobRef{}, err
	}
	ref, err := store.PutObject(ctx, project, bytes.NewReader(data))
	if err != nil {
		return storage.BlobRef{}, err
	}
	if ref.Hash != hash || ref.Size != int64(len(data)) {
		return storage.BlobRef{}, retention.ErrInvalidInventory
	}
	return ref, nil
}
