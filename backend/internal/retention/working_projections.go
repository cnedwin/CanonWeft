package retention

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"novelstudio/internal/domain"
)

type ProjectionDependency struct {
	ReferenceID string `json:"reference_id"`
	PayloadHash string `json:"payload_hash"`
}

// Projection evidence contains hashes and fixed identities only. Dependencies
// are copied into the consumer's actual source pins before it can be planned.
type WorkingProjectionEvidence struct {
	Kind         string                 `json:"kind"`
	Version      string                 `json:"version"`
	State        string                 `json:"state"`
	BaseHead     string                 `json:"base_head"`
	Hash         string                 `json:"hash"`
	Dependencies []ProjectionDependency `json:"dependencies,omitempty"`
}

func matchesWorkingProjection(request, proof *WorkingProjectionEvidence) bool {
	return request != nil && proof != nil && request.Kind == proof.Kind && request.Version == proof.Version && request.BaseHead == proof.BaseHead && request.Hash == proof.Hash && (request.State == "" || request.State == proof.State)
}

func validWorkingProjection(v ReferenceVersion) bool {
	p := v.WorkingProjection
	if p == nil {
		return true
	}
	if !hash64.MatchString(p.Hash) || (p.BaseHead != "" && !id32.MatchString(p.BaseHead)) || len(p.Dependencies) > 8 {
		return false
	}
	switch p.Kind {
	case "memory_session":
		n, err := strconv.ParseInt(p.Version, 10, 64)
		if v.Table != "memory_sessions" || v.Field != "document" || p.State != "PRIVATE_DRAFT" || p.BaseHead != "" || err != nil || n < 1 || strconv.FormatInt(n, 10) != p.Version {
			return false
		}
	case "ip_draft":
		if v.Table != "ip_drafts" || v.Field != "payload" || p.State != "PRIVATE_DRAFT" || v.DraftVersion == nil || p.Version != v.DraftVersion.Version || p.BaseHead != v.DraftVersion.BaseHead {
			return false
		}
	case "ip_candidate":
		if v.Table != "ip_revisions" || v.Field != "payload" || !hash64.MatchString(p.Version) {
			return false
		}
	case "release_manifest":
		if v.Table != "artifact_releases" || v.Field != "manifest" || !hash64.MatchString(p.Version) || p.Hash != p.Version {
			return false
		}
	case "release_artifact":
		if v.Table != "artifact_releases" || !strings.HasPrefix(v.Field, "manifest.artifacts.") || !strings.HasSuffix(v.Field, ".hash") || !hash64.MatchString(p.Version) || p.Hash != v.Reference.ObjectHash {
			return false
		}
	default:
		return false
	}
	if p.Kind != "ip_draft" && p.Kind != "memory_session" && p.State != "PREPARED" && p.State != "READY" && p.State != "PUBLISHED" {
		return false
	}
	previous := ""
	for _, d := range p.Dependencies {
		if !hash64.MatchString(d.ReferenceID) || !hash64.MatchString(d.PayloadHash) || d.ReferenceID <= previous || d.ReferenceID == v.Reference.ID {
			return false
		}
		previous = d.ReferenceID
	}
	return true
}

func (index *SourceIndex) optionalCandidateRow(table, id string) (SourceRow, bool) {
	key, err := RowOwnerKey(table, map[string]string{"candidate_id": id})
	if err != nil {
		return SourceRow{}, false
	}
	row, ok := index.rows[table][key]
	return row, ok
}

func (index *SourceIndex) attachWorkingProjections(project string, materials []MaterialReference) error {
	byID := map[string]MaterialReference{}
	for _, m := range materials {
		byID[m.ID] = m
	}
	for i := range materials {
		m := &materials[i]
		row := index.rows[m.Table][m.OwnerKey]
		var proof *WorkingProjectionEvidence
		var text any
		switch {
		case m.Table == "memory_sessions" && m.Field == "document":
			var session memoryCheckpoint
			if authorDocument(row.Values["document"], &session) != nil {
				return ErrSourceOwnership
			}
			proof = &WorkingProjectionEvidence{Kind: "memory_session", Version: row.Values["revision"], State: "PRIVATE_DRAFT", Hash: stableHash(session)}
		case m.Table == "ip_drafts" && m.Field == "payload" && m.DraftVersion != nil:
			var stored domain.StoredIPDraft
			if json.Unmarshal([]byte(row.Values["payload"]), &stored) != nil {
				continue
			}
			if stored.DraftSchema == 1 || stored.DraftSchema == 2 {
				if authorDocument(row.Values["payload"], &stored) != nil {
					continue
				}
			} else {
				var legacy domain.Snapshot
				if authorDocument(row.Values["payload"], &legacy) != nil {
					continue
				}
			}
			revision, _ := strconv.ParseInt(m.DraftVersion.Version, 10, 64)
			draft, err := domain.DecodeIPDraft([]byte(row.Values["payload"]), revision, row.Values["base_head"])
			if err != nil {
				continue
			}
			proof = &WorkingProjectionEvidence{Kind: "ip_draft", Version: m.DraftVersion.Version, State: "PRIVATE_DRAFT", BaseHead: draft.BaseHead, Hash: stableHash(draft)}
			text = draft
		case m.Table == "ip_revisions" && m.Field == "payload":
			incomplete := false
			for table, field := range map[string]string{"ip_amendments": "reason", "ip_promotions": "payload", "ip_branch_deltas": "payload"} {
				if aux, ok := index.optionalCandidateRow(table, row.Values["id"]); ok && index.replacedColumn(project, aux, field) {
					incomplete = true
				}
			}
			if incomplete {
				// The immutable IP snapshot remains intact, but no complete
				// authoring candidate can be reconstructed from erased auxiliaries.
				// Existing consumers keep their exact historical source pins.
				if prior, ok := index.partialCandidates[m.ID]; ok && prior.Reference.PayloadHash == m.PayloadHash {
					m.WorkingProjection, m.TextProof = prior.WorkingProjection, prior.TextProof
				}
				continue
			}
			candidate := domain.IPCandidate{ID: row.Values["id"], BranchID: row.Values["branch_id"], BaseHead: row.Values["base_head"], Hash: row.Values["content_hash"], Status: row.Values["status"]}
			if contentJSON([]byte(row.Values["payload"]), &candidate.Snapshot, candidate.Hash) != nil {
				continue
			}
			candidate.Checks = domain.ReviewRules(candidate.Snapshot)
			deps := []ProjectionDependency{}
			add := func(aux SourceRow, field string) error {
				owner, err := RowOwnerKey(aux.Table, aux.Values)
				if err != nil {
					return err
				}
				id, err := ReferenceID(project, aux.Table, owner, field)
				ref, ok := byID[id]
				if err != nil || !ok {
					return ErrSourceOwnership
				}
				deps = append(deps, ProjectionDependency{id, ref.PayloadHash})
				return nil
			}
			if aux, ok := index.optionalCandidateRow("ip_amendments", candidate.ID); ok {
				candidate.AmendmentReason = aux.Values["reason"]
				if err := add(aux, "reason"); err != nil {
					return err
				}
			}
			if aux, ok := index.optionalCandidateRow("ip_promotions", candidate.ID); ok {
				var promotion domain.PromotionInput
				if authorDocument(aux.Values["payload"], &promotion) != nil {
					return ErrSourceOwnership
				}
				candidate.Promotion = &promotion
				if err := add(aux, "payload"); err != nil {
					return err
				}
			}
			_, merge := index.optionalCandidateRow("ip_merge_candidates", candidate.ID)
			candidate.RequiresSemanticReview = candidate.Promotion != nil || merge
			if aux, ok := index.optionalCandidateRow("ip_branch_deltas", candidate.ID); ok {
				var delta domain.BranchDelta
				if financialDigest([]byte(aux.Values["payload"])) != aux.Values["delta_hash"] || authorDocument(aux.Values["payload"], &delta) != nil || delta.Schema != 1 {
					return ErrSourceOwnership
				}
				candidate.Delta = &delta
				if err := add(aux, "payload"); err != nil {
					return err
				}
			}
			sort.Slice(deps, func(i, j int) bool { return deps[i].ReferenceID < deps[j].ReferenceID })
			proof = &WorkingProjectionEvidence{Kind: "ip_candidate", Version: candidate.Hash, State: candidate.Status, BaseHead: candidate.BaseHead, Hash: stableHash(candidate), Dependencies: deps}
			text = candidate
		case m.Table == "artifact_releases" && (m.Field == "manifest" || (strings.HasPrefix(m.Field, "manifest.artifacts.") && strings.HasSuffix(m.Field, ".hash"))):
			var release domain.Manifest
			if contentJSON([]byte(row.Values["manifest"]), &release, row.Values["content_hash"]) != nil {
				return ErrSourceOwnership
			}
			proof = &WorkingProjectionEvidence{Kind: "release_manifest", Version: row.Values["content_hash"], State: row.Values["status"], BaseHead: release.ExpectedIPHead, Hash: row.Values["content_hash"]}
			text = release
			if m.Field != "manifest" {
				proof.Kind = "release_artifact"
				proof.Hash = m.ObjectHash
				text = nil
			}
		}
		m.WorkingProjection = proof
		if text != nil {
			raw, err := json.Marshal(text)
			if err != nil {
				return err
			}
			m.TextProof, err = textCopyEvidence(string(raw))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (index *SourceIndex) privateProjectionSource(project string, manifest domain.PrivateManifest, pin domain.PrivatePin) (MaterialSource, error) {
	var row SourceRow
	var err error
	field := "payload"
	p := &WorkingProjectionEvidence{Kind: pin.Ref.Kind, Version: pin.Ref.Version, State: pin.State, BaseHead: pin.BaseHead, Hash: pin.Hash}
	switch pin.Ref.Kind {
	case "ip_draft":
		if pin.Ref.ID != manifest.Branch || pin.Part != 0 || pin.Format != "structured" || pin.State != "PRIVATE_DRAFT" {
			return MaterialSource{}, ErrSourceOwnership
		}
		// The original row may no longer exist; its composite identity still
		// permits an exact history lookup, never a guess at another actor.
		row = SourceRow{Table: "ip_drafts", Values: map[string]string{"branch_id": manifest.Branch, "actor_id": manifest.Actor}}
	case "ip_candidate":
		if pin.Part != 0 || pin.Format != "structured" {
			return MaterialSource{}, ErrSourceOwnership
		}
		row, err = index.sourceRow("ip_revisions", map[string]string{"id": pin.Ref.ID})
		if err != nil || row.Values["branch_id"] != manifest.Branch {
			return MaterialSource{}, ErrSourceOwnership
		}
	case "release_candidate":
		row, err = index.sourceRow("artifact_releases", map[string]string{"id": pin.Ref.ID})
		if err != nil || row.Values["project_id"] != project || row.Values["branch_id"] != manifest.Branch || row.Values["content_hash"] != pin.Ref.Version {
			return MaterialSource{}, ErrSourceOwnership
		}
		var release domain.Manifest
		if contentJSON([]byte(row.Values["manifest"]), &release, pin.Ref.Version) != nil || pin.BaseHead != release.ExpectedIPHead {
			return MaterialSource{}, ErrSourceOwnership
		}
		switch {
		case pin.Part == 0 && pin.Format == "structured":
			p.Kind = "release_manifest"
			field = "manifest"
		case pin.Part > 0 && pin.Part <= len(release.Artifacts) && pin.Format == "text":
			artifact := release.Artifacts[pin.Part-1]
			if pin.Hash != artifact.Hash {
				return MaterialSource{}, ErrSourceOwnership
			}
			field, err = ArtifactField(artifact.ResourceID, false)
			if err != nil {
				return MaterialSource{}, err
			}
			p.Kind = "release_artifact"
		case pin.Part == len(release.Artifacts)+1 && release.IPCandidateID != "" && pin.Format == "structured":
			row, err = index.sourceRow("ip_revisions", map[string]string{"id": release.IPCandidateID})
			if err != nil || row.Values["branch_id"] != manifest.Branch {
				return MaterialSource{}, ErrSourceOwnership
			}
			p.Kind, p.Version, p.BaseHead, p.State = "ip_candidate", row.Values["content_hash"], row.Values["base_head"], ""
		default:
			return MaterialSource{}, ErrSourceOwnership
		}
	default:
		return MaterialSource{}, ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return MaterialSource{}, err
	}
	id, err := ReferenceID(project, row.Table, key, field)
	if err != nil {
		return MaterialSource{}, err
	}
	return MaterialSource{ReferenceID: id, WorkingProjection: p}, nil
}
