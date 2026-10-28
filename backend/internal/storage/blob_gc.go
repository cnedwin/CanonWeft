package storage

import (
	"context"
	"errors"
	"regexp"
)

// BlobGC is deliberately separate from BlobStore. Only the retention executor
// receives this authority, after locking and revalidating an exact object plan.
// It has no list, prefix, project-directory, or recursive deletion operation.
type BlobGC interface {
	DeleteObject(context.Context, string, string) (GCResult, error)
}
type GCResult string

const (
	GCRemoved GCResult = "REMOVED"
	GCAbsent  GCResult = "ABSENT"
)

var (
	ErrGCInvalidKey  = errors.New("invalid retention object key")
	ErrGCConflict    = errors.New("retention object changed or is not a regular immutable object")
	ErrGCUnavailable = errors.New("retention object storage unavailable")
	ErrGCVersioned   = errors.New("physical retention deletion requires an unversioned bucket")
)
var gcProject = regexp.MustCompile(`^[a-f0-9]{32}$`)
var gcHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validGCKey(project, hash string) bool {
	return gcProject.MatchString(project) && gcHash.MatchString(hash)
}
