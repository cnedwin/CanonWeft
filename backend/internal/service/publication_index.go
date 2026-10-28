package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"os"
	"slices"
	"strings"
)

var errAlreadyPublished = errors.New("publication already complete")

// Prospective indexes are immutable and invisible to Search until their exact
// branch/resource heads are atomically published. They never expose candidates.
func (s *IP) publicationInfo(ctx context.Context, tx *sql.Tx, project, branch, releaseID, candidateID string) (IndexInfo, error) {
	var release domain.Release
	if releaseID != "" {
		var err error
		release, err = releaseTx(ctx, tx, project, releaseID)
		if err != nil {
			return IndexInfo{}, err
		}
		if release.Status == "PUBLISHED" {
			return IndexInfo{}, errAlreadyPublished
		}
		if release.Status != "READY" {
			return IndexInfo{}, domain.ErrConflict
		}
		if err = s.validateManifest(ctx, tx, project, release.Manifest); err != nil {
			return IndexInfo{}, err
		}
		branch = release.Manifest.BranchID
		candidateID = release.Manifest.IPCandidateID
	}
	info, err := knowledgeManifest(ctx, tx, project, branch)
	if err != nil {
		return info, err
	}
	if candidateID != "" {
		candidate, err := candidateTx(ctx, tx, branch, candidateID)
		if err != nil {
			return info, err
		}
		if candidate.Status == "PUBLISHED" {
			return info, errAlreadyPublished
		}
		if candidate.Status != "READY" || candidate.BaseHead != info.Manifest.IPHead {
			return info, domain.ErrConflict
		}
		info.Manifest.IPHead = candidateID
	}
	for _, ref := range release.Manifest.Artifacts {
		resource, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return info, err
		}
		pin := SourcePin{ID: ref.ResourceID, Name: resource.Name, Head: releaseID, Hash: ref.Hash, Policy: ref.Policy}
		index := slices.IndexFunc(info.Manifest.Sources, func(old SourcePin) bool { return old.ID == pin.ID })
		if index >= 0 {
			info.Manifest.Sources[index] = pin
		} else {
			info.Manifest.Sources = append(info.Manifest.Sources, pin)
		}
	}
	slices.SortFunc(info.Manifest.Sources, func(a, b SourcePin) int { return strings.Compare(a.ID, b.ID) })
	raw, _ := json.Marshal(info.Manifest)
	info.ID = digest(project + "\x00" + string(raw))
	return info, nil
}

func (s *IP) preparePublicationIndex(ctx context.Context, a domain.Actor, project, branch, releaseID, candidateID string) error {
	if releaseID != "" {
		release, err := s.ReadRelease(ctx, a, project, releaseID)
		if err != nil {
			return err
		}
		branch = release.Manifest.BranchID
	}
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	_, err := (&Knowledge{IP: s, Embedder: embedder}).build(ctx, a, project, branch, true, func(tx *sql.Tx) (IndexInfo, error) {
		return s.publicationInfo(ctx, tx, project, branch, releaseID, candidateID)
	})
	if errors.Is(err, errAlreadyPublished) {
		return nil
	}
	return err
}

func (s *IP) requirePublicationIndex(ctx context.Context, tx *sql.Tx, project, branch, releaseID, candidateID string) error {
	info, err := s.publicationInfo(ctx, tx, project, branch, releaseID, candidateID)
	if errors.Is(err, errAlreadyPublished) {
		return nil
	}
	if err != nil {
		return err
	}
	return readyIndex(ctx, tx, &info)
}
