package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

func (s *Capabilities) methodBundlesTx(ctx context.Context, tx *sql.Tx, project string, versions []domain.CapabilityVersion) (map[string]skills.Bundle, error) {
	result := map[string]skills.Bundle{}
	for _, v := range versions {
		if v.Definition.Kind != "skill" {
			continue
		}
		if _, ok := result[v.Definition.PackageID]; ok {
			continue
		}
		pkg, err := skillTx(ctx, tx, project, v.Definition.PackageID)
		if err != nil {
			return nil, err
		}
		bundle, err := s.packageTx(ctx, project, pkg)
		if err != nil {
			return nil, err
		}
		result[pkg.ID] = bundle
	}
	return result, nil
}
