package service

import (
	"context"
	"errors"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

type recoveryCleanupStore interface {
	RetentionScanStore
	storage.BlobGC
	RetentionNamespace() string
}

type recoveryCleanupSource struct {
	inventorySource
	recoveryCleanupStore
}

type recoveryPreparedStore interface {
	ScanRecoveryObjects(context.Context, string, []storage.BlobRef) ([]storage.BlobRef, error)
	DeleteRecoveryPrepared(context.Context, string, storage.BlobRef) error
}

func (s recoveryCleanupSource) InventoryRecoveryObjects(ctx context.Context, project string, expected []retention.PhysicalObject) ([]retention.PhysicalObject, error) {
	var objects []retention.PhysicalObject
	var err error
	if store, ok := s.recoveryCleanupStore.(recoveryPreparedStore); ok {
		refs := make([]storage.BlobRef, 0, len(expected))
		for _, object := range expected {
			refs = append(refs, storage.BlobRef{Hash: object.Hash, Size: object.Size})
		}
		var actual []storage.BlobRef
		actual, err = store.ScanRecoveryObjects(ctx, project, refs)
		for _, ref := range actual {
			objects = append(objects, retention.PhysicalObject{Hash: ref.Hash, Size: ref.Size})
		}
	} else {
		objects, err = s.inventorySource.InventoryObjects(ctx, project)
	}
	if errors.Is(err, storage.ErrInventoryConflict) || errors.Is(err, storage.ErrInventoryLimit) {
		return nil, retention.ErrInvalidInventory
	}
	return objects, err
}

func (s recoveryCleanupSource) RemoveRecoveryObject(ctx context.Context, project string, object retention.PhysicalObject) error {
	if store, ok := s.recoveryCleanupStore.(recoveryPreparedStore); ok {
		if err := store.DeleteRecoveryPrepared(ctx, project, storage.BlobRef{Hash: object.Hash, Size: object.Size}); err != nil {
			return err
		}
	}
	_, err := s.DeleteObject(ctx, project, object.Hash)
	return err
}

func (s *RetentionScanner) ReconcileRecovery(ctx context.Context) (bool, error) {
	store, ok := s.Blobs.(recoveryCleanupStore)
	if !ok {
		return false, domain.ErrNotReady
	}
	return retention.ReconcileRecovery(ctx, s.Workspace.DB, protectedLedger(), recoveryCleanupSource{inventorySource{s.Blobs}, store})
}
