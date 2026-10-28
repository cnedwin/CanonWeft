package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"novelstudio/internal/domain"
)

type CapabilityContentExpirySummary struct {
	PackageID   string                        `json:"package_id,omitempty"`
	PackageHash string                        `json:"package_hash,omitempty"`
	AgentRole   string                        `json:"agent_role,omitempty"`
	RecipeSteps []CapabilityRecipeStepSummary `json:"recipe_steps,omitempty"`
}

// Keep only relation hashes and closed control values needed to verify retired
// workflows. Names, prompts, schemas and routing text are not retained here.
type CapabilityRecipeStepSummary struct {
	KeyHash             string   `json:"key_hash"`
	StepHash            string   `json:"step_hash"`
	AgentID             string   `json:"agent_id"`
	AfterHashes         []string `json:"after_hashes,omitempty"`
	WaitForConfirmation bool     `json:"wait_for_confirmation"`
}

func capabilityContentSummary(d domain.CapabilityDefinition) *CapabilityContentExpirySummary {
	s := &CapabilityContentExpirySummary{PackageID: d.PackageID, PackageHash: d.PackageHash, AgentRole: d.Role}
	for _, step := range d.Steps {
		v := CapabilityRecipeStepSummary{KeyHash: financialDigest([]byte(step.Key)), StepHash: capabilityProjectionHash(step), AgentID: step.AgentID, WaitForConfirmation: step.WaitForConfirmation}
		for _, key := range step.After {
			v.AfterHashes = append(v.AfterHashes, financialDigest([]byte(key)))
		}
		s.RecipeSteps = append(s.RecipeSteps, v)
	}
	return s
}

func validCapabilityContentExpiry(s *CapabilityContentExpirySummary) bool {
	if s == nil || !((s.PackageID == "" && s.PackageHash == "") || (id32.MatchString(s.PackageID) && hash64.MatchString(s.PackageHash))) || len(s.RecipeSteps) > 32 {
		return false
	}
	if s.AgentRole != "" && (!slices.Contains(domain.ModelRoles(), s.AgentRole) || s.AgentRole == "image_generator" || len(s.RecipeSteps) > 0) {
		return false
	}
	seen := map[string]bool{}
	for _, step := range s.RecipeSteps {
		if !hash64.MatchString(step.KeyHash) || !hash64.MatchString(step.StepHash) || !id32.MatchString(step.AgentID) || seen[step.KeyHash] || len(step.AfterHashes) > 32 {
			return false
		}
		deps := map[string]bool{}
		for _, key := range step.AfterHashes {
			if !seen[key] || deps[key] {
				return false
			}
			deps[key] = true
		}
		seen[step.KeyHash] = true
	}
	return true
}

func DirectSkillObjectRule(key string) bool {
	return key == "skill_packages.bundle_hash" || key == "skill_packages.source.archive_hash"
}

func SkillArchiveSourceValue(row SourceRow) (string, error) {
	var source authorSkillSource
	if authorDocument(row.Values["source"], &source) != nil || source.Kind == "" || !hash64.MatchString(source.ArchiveHash) {
		return "", ErrInvalidInventory
	}
	return source.ArchiveHash, nil
}

// Frozen execution inputs are covered by their registered source pins. These
// additional edges protect authored versions and package ancestry as well.
func skillConsumersTx(ctx context.Context, tx *sql.Tx, target ReferenceVersion, current map[string]ReferenceVersion, batch map[string]bool) (bool, error) {
	isDefinition := target.Table == "capability_versions" && target.Field == "definition"
	isPackage := target.Table == "skill_packages"
	if !isDefinition && !isPackage {
		return false, nil
	}
	var key []string
	if json.Unmarshal([]byte(target.OwnerKey), &key) != nil || len(key) != 1 {
		return false, ErrInvalidInventory
	}
	for _, v := range current {
		if v.Reference.State != Retained || batch[v.Reference.ID] {
			continue
		}
		if isPackage && target.Field == "source" && v.Table == target.Table && v.OwnerKey == target.OwnerKey && v.Field == "source.archive_hash" {
			return true, nil
		}
		var owner []string
		if json.Unmarshal([]byte(v.OwnerKey), &owner) != nil || len(owner) == 0 {
			return false, ErrInvalidInventory
		}
		if v.Table == "capability_versions" && v.Field == "definition" {
			var raw, parent string
			if err := tx.QueryRowContext(ctx, "SELECT definition,parent_id FROM capability_versions WHERE id=?", owner[0]).Scan(&raw, &parent); err != nil {
				return false, err
			}
			var d domain.CapabilityDefinition
			if authorDocument(raw, &d) != nil || d.Validate() != nil {
				return false, ErrInvalidInventory
			}
			if isPackage && d.PackageID == key[0] {
				return true, nil
			}
			if isDefinition {
				if parent == key[0] {
					return true, nil
				}
				for _, dep := range d.Dependencies {
					if dep.ID == key[0] {
						return true, nil
					}
				}
			}
		}
		if isPackage && v.Table == "skill_packages" && owner[0] != key[0] {
			var parent, upstream string
			if err := tx.QueryRowContext(ctx, "SELECT parent_id,upstream_id FROM skill_packages WHERE id=?", owner[0]).Scan(&parent, &upstream); err != nil {
				return false, err
			}
			if parent == key[0] || upstream == key[0] {
				return true, nil
			}
		}
	}
	return false, nil
}
