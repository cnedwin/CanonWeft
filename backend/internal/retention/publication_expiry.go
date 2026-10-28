package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

func PublicationArtifactSourceValue(row SourceRow, field string) (string, error) {
	s, err := releaseExpirySummary(row)
	if err != nil {
		return "", err
	}
	return releaseArtifactValue(s, field)
}

// Author roots have no TTL source pins. Their structural dependencies still
// need the original publication until each consumer has been retired.
func publicationConsumersTx(ctx context.Context, tx *sql.Tx, target ReferenceVersion, current map[string]ReferenceVersion, batch map[string]bool) (bool, error) {
	isIP := target.Table == "ip_revisions" && target.Field == "payload"
	isRelease := target.Table == "artifact_releases" && target.Field == "manifest"
	isReleaseMaterial := target.Table == "artifact_releases"
	if !isIP && !isReleaseMaterial {
		return false, nil
	}
	var key []string
	if json.Unmarshal([]byte(target.OwnerKey), &key) != nil || len(key) != 1 {
		return false, ErrInvalidInventory
	}
	id := key[0]
	for _, v := range current {
		if v.Reference.State != Retained || batch[v.Reference.ID] {
			continue
		}
		if isRelease && v.Table == target.Table && v.OwnerKey == target.OwnerKey && strings.HasPrefix(v.Field, "manifest.artifacts.") {
			return true, nil
		}
		var pk []string
		if json.Unmarshal([]byte(v.OwnerKey), &pk) != nil || len(pk) == 0 {
			return false, ErrInvalidInventory
		}
		if isIP && (v.Table == "ip_promotions" || v.Table == "ip_branch_deltas") && v.Field == "payload" && pk[0] == id {
			return true, nil
		}
		if v.Table == "resources" && v.Field == "content_hash" && isRelease {
			var head string
			if err := tx.QueryRowContext(ctx, "SELECT head_revision FROM resources WHERE id=?", pk[0]).Scan(&head); err != nil {
				return false, err
			}
			if head == id {
				return true, nil
			}
		}
		if v.Table == "artifact_releases" && v.Field == "manifest" {
			var raw, hash string
			if err := tx.QueryRowContext(ctx, "SELECT manifest,content_hash FROM artifact_releases WHERE id=?", pk[0]).Scan(&raw, &hash); err != nil {
				return false, err
			}
			var m domain.Manifest
			if contentJSON([]byte(raw), &m, hash) != nil {
				return false, ErrInvalidInventory
			}
			if isIP && (m.ExpectedIPHead == id || m.IPCandidateID == id) {
				return true, nil
			}
			if isRelease && (slices.Contains(m.PatchParents, id) || slices.Contains(m.CompositionParents, id) || slices.Contains(m.PeerReleases, id)) {
				return true, nil
			}
			for _, a := range m.Artifacts {
				if isRelease && (a.ExpectedHead == id || (a.Origin != nil && a.Origin.ParentReleaseID == id)) {
					return true, nil
				}
				if isIP && a.Origin != nil && a.Origin.IPHead == id {
					return true, nil
				}
			}
		}
		if v.Table == "knowledge_indexes" && v.Field == "manifest" {
			var raw string
			if err := tx.QueryRowContext(ctx, "SELECT manifest FROM knowledge_indexes WHERE id=?", pk[0]).Scan(&raw); err != nil {
				return false, err
			}
			var m domain.KnowledgeIndexManifest
			if authorDocument(raw, &m) != nil {
				return false, ErrInvalidInventory
			}
			if isIP && m.IPHead == id {
				return true, nil
			}
			for _, source := range m.Sources {
				if isReleaseMaterial && source.Head == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// Only fixed identities survive a release manifest. Policies, patch text and
// author notes are erased; this inventory is not a reconstructed manifest.
type ReleaseExpirySummary struct {
	Artifacts []ReleaseExpiryArtifact `json:"artifacts"`
}

type ReleaseExpiryArtifact struct {
	ResourceID   string `json:"resource_id"`
	Hash         string `json:"hash"`
	OriginalHash string `json:"original_hash,omitempty"`
}

func validReleaseExpirySummary(s *ReleaseExpirySummary) bool {
	if s == nil || len(s.Artifacts) == 0 || len(s.Artifacts) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, a := range s.Artifacts {
		if !id32.MatchString(a.ResourceID) || seen[a.ResourceID] || !hash64.MatchString(a.Hash) || (a.OriginalHash != "" && !hash64.MatchString(a.OriginalHash)) {
			return false
		}
		seen[a.ResourceID] = true
	}
	return true
}

func releaseExpirySummary(row SourceRow) (*ReleaseExpirySummary, error) {
	var m domain.Manifest
	if contentJSON([]byte(row.Values["manifest"]), &m, row.Values["content_hash"]) != nil || (m.Schema != 1 && m.Schema != 2) || m.BranchID != row.Values["branch_id"] || (m.ReleaseID != "" && m.ReleaseID != row.Values["id"]) {
		return nil, ErrInvalidInventory
	}
	s := &ReleaseExpirySummary{}
	for _, a := range m.Artifacts {
		if a.Size < 0 || (m.Schema == 2 && (a.Origin == nil || a.Origin.ValidateTarget(m.BranchID, a) != nil)) || (a.SourceImportID == "") != (a.OriginalHash == "") {
			return nil, ErrInvalidInventory
		}
		s.Artifacts = append(s.Artifacts, ReleaseExpiryArtifact{a.ResourceID, a.Hash, a.OriginalHash})
	}
	if !validReleaseExpirySummary(s) {
		return nil, ErrInvalidInventory
	}
	return s, nil
}

func releaseArtifactValue(s *ReleaseExpirySummary, field string) (string, error) {
	parts := strings.Split(field, ".")
	if !validReleaseExpirySummary(s) || len(parts) != 4 || parts[0] != "manifest" || parts[1] != "artifacts" || (parts[3] != "hash" && parts[3] != "original_hash") {
		return "", ErrInvalidInventory
	}
	for _, a := range s.Artifacts {
		if a.ResourceID == parts[2] {
			value := a.Hash
			if parts[3] == "original_hash" {
				value = a.OriginalHash
			}
			if !hash64.MatchString(value) {
				return "", ErrInvalidInventory
			}
			return value, nil
		}
	}
	return "", ErrInvalidInventory
}

func DirectPublicationObjectRule(key string) bool {
	if key == "ip_revisions.content_hash" || key == "resources.content_hash" || key == "artifact_releases.content_hash" {
		return true
	}
	rule, ok := ConcreteRule(key)
	return ok && rule.Storage == Blob && strings.HasPrefix(key, "artifact_releases.manifest.artifacts.")
}
