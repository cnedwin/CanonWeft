package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

func labelConsumersTx(ctx context.Context, tx *sql.Tx, target ReferenceVersion, current map[string]ReferenceVersion, batch map[string]bool) (bool, error) {
	if !AnonymizedLabelAllowed(target.Reference.Rule) {
		return false, nil
	}
	var key []string
	if json.Unmarshal([]byte(target.OwnerKey), &key) != nil || len(key) == 0 {
		return false, ErrInvalidInventory
	}
	var runID string
	if target.Table == "recipe_steps" {
		if err := tx.QueryRowContext(ctx, "SELECT run_id FROM recipe_steps WHERE id=?", key[0]).Scan(&runID); err != nil {
			return false, err
		}
	}
	for _, v := range current {
		if v.Reference.State != Retained || batch[v.Reference.ID] {
			continue
		}
		if target.Table == "capability_versions" && v.Table == target.Table && v.OwnerKey == target.OwnerKey && v.Field == "definition" {
			return true, nil
		}
		if target.Table == "capability_heads" && v.Table == "capability_versions" && v.Field == "capability_key" {
			if v.Reference.PayloadHash == target.Reference.PayloadHash {
				return true, nil
			}
		}
		if runID != "" && v.Field == "frozen" && (v.Table == "recipe_steps" || v.Table == "recipe_runs") {
			var owner []string
			if json.Unmarshal([]byte(v.OwnerKey), &owner) != nil || len(owner) != 1 {
				return false, ErrInvalidInventory
			}
			if v.Table == "recipe_runs" && owner[0] == runID {
				return true, nil
			}
			if v.Table == "recipe_steps" {
				var parent string
				if err := tx.QueryRowContext(ctx, "SELECT run_id FROM recipe_steps WHERE id=?", owner[0]).Scan(&parent); err != nil {
					return false, err
				}
				if parent == runID {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

const retiredLabelPrefix = "~erased:"

// Labels remain unique after erasure. The digest is not proof of erasure;
// callers must bind the independent journal before accepting a marked value.
func RetiredLabelHash(value string) (string, bool) {
	if !strings.HasPrefix(value, retiredLabelPrefix) {
		return "", false
	}
	hash := strings.TrimPrefix(value, retiredLabelPrefix)
	return hash, hash64.MatchString(hash)
}

func LabelIdentityHash(value string) string {
	if hash, ok := RetiredLabelHash(value); ok {
		return hash
	}
	return financialDigest([]byte(value))
}

func AnonymizedLabelAllowed(key string) bool {
	switch key {
	case "branches.name", "recipe_steps.step_key", "capability_versions.capability_key", "capability_heads.capability_key", "skill_discovery_candidates.source_path":
		return true
	}
	return false
}

func ProjectAnonymizedLabel(expiry Expiry, raw []byte, summary EvidenceSummary, at time.Time) (ExpiredProjection, error) {
	if !AnonymizedLabelAllowed(expiry.Rule) || strings.HasPrefix(string(raw), retiredLabelPrefix) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	p, err := ProjectExpired(expiry, raw, summary, at)
	if err != nil {
		return ExpiredProjection{}, err
	}
	p.Materialization = AnonymizedLabel
	return p, ValidateProjectionForRule(p, expiry.Rule)
}

func (index *SourceIndex) validateRetiredLabelCoverage(project string) error {
	for _, rule := range referenceRules {
		if !AnonymizedLabelAllowed(rule.Key) {
			continue
		}
		parts := strings.SplitN(rule.Key, ".", 2)
		for _, row := range index.rows[parts[0]] {
			if !strings.HasPrefix(row.Values[parts[1]], retiredLabelPrefix) {
				continue
			}
			owner, err := index.Owner(row.Table, row.Values)
			if err != nil {
				return err
			}
			if owner.ProjectID != project {
				continue
			}
			if _, ok := RetiredLabelHash(row.Values[parts[1]]); !ok || !index.replacedColumn(project, row, parts[1]) {
				return ErrSourceOwnership
			}
		}
	}
	return nil
}
