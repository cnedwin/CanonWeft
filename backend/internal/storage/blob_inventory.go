package storage

import (
	"context"
	"errors"
)

// BlobInventory is a read-only capability, separate from deletion. The caller
// must hold the project coordination lock throughout listing and registration.
// A successful result covers verified immutable objects only; unknown files,
// partial pages and unavailable storage must never become an empty inventory.
// File times are deliberately absent: first observation is recorded in MySQL.
type BlobInventory interface {
	ScanObjects(context.Context, string) ([]BlobRef, error)
}

const maxInventoryObjects = 250000

var (
	ErrInventoryInvalidKey  = errors.New("invalid inventory project key")
	ErrInventoryConflict    = errors.New("object inventory changed or contains unrecognized material")
	ErrInventoryUnavailable = errors.New("object inventory unavailable")
	ErrInventoryLimit       = errors.New("object inventory exceeds capacity")
)
