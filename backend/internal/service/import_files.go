package service

import (
	"context"

	"novelstudio/internal/domain"
)

// Raw files remain private to the uploading actor, including after an edited
// extract is published. The permission/retention checks precede blob access.
func (s *IP) ReadImportFile(ctx context.Context, a domain.Actor, project, branch, id string) (string, []byte, error) {
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return "", nil, err
	}
	source, err := importTx(ctx, tx, a.ID, project, branch, id)
	if err != nil {
		return "", nil, err
	}
	data, err := s.readOriginalBytes(ctx, project, source.Hash, source.Size)
	return source.Name, data, err
}

// An upload is available for inspection even while parsing is queued or failed.
func (s *IngestJobs) ReadOriginal(ctx context.Context, a domain.Actor, project, branch, id string) (string, []byte, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	job, err := ingestTx(ctx, tx, project, id, true)
	if err != nil {
		return "", nil, err
	}
	if !job.owns(a, branch) {
		return "", nil, domain.ErrForbidden
	}
	if err = requireRuntimeObjectTx(ctx, tx, project, "ingest_jobs", id, "source_hash", job.Hash); err != nil {
		return "", nil, err
	}
	data, err := s.IP.readOriginalBytes(ctx, project, job.Hash, job.Size)
	return job.Name, data, err
}

func (s *IP) readOriginalBytes(ctx context.Context, project, hash string, size int64) ([]byte, error) {
	if s.Blobs == nil {
		return nil, domain.ErrNotReady
	}
	data, err := s.Blobs.ReadObject(ctx, project, hash)
	if err != nil || int64(len(data)) != size || digest(string(data)) != hash {
		return nil, domain.ErrNotReady
	}
	return data, nil
}
