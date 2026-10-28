package service

import "novelstudio/internal/domain"

type ChangedField = domain.ChangedField
type DependencyDifference = domain.DependencyDifference

func dependencyDifferences(changes []domain.DependencyChange, before, after domain.Snapshot) []DependencyDifference {
	return domain.DependencyDifferences(changes, before, after)
}
