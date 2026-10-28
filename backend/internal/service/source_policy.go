package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

func sourcePolicyTx(ctx context.Context, tx *sql.Tx, project, resource, head string) (*domain.KnowledgePolicy, error) {
	if head == "" {
		return nil, nil
	}
	release, err := releaseTx(ctx, tx, project, head)
	if err != nil {
		return nil, err
	}
	if release.Status != "PUBLISHED" {
		return nil, domain.ErrNotReady
	}
	for _, ref := range release.Manifest.Artifacts {
		if ref.ResourceID == resource {
			return ref.Policy, nil
		}
	}
	return nil, domain.ErrNotReady
}

func validPolicyReferences(p *domain.KnowledgePolicy, snapshot domain.Snapshot) error {
	if p == nil {
		return nil
	}
	if err := p.Validate(); err != nil {
		return err
	}
	for _, id := range p.KnownBy {
		found := false
		for _, e := range snapshot.Entities {
			if e.ID == id {
				found = true
				break
			}
		}
		if !found {
			return domain.ErrValidation
		}
	}
	return nil
}
