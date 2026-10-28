package retention

import (
	"strconv"

	"novelstudio/internal/domain"
)

// A business edit number, not the retention journal revision. ProjectionHash
// hashes the stored body, or the canonical resource policy. Additional working
// wrappers (such as an IP draft DTO) still need their own projection proof.
type DraftVersionEvidence struct {
	Version        string `json:"version"`
	ProjectionHash string `json:"projection_hash"`
	BaseHead       string `json:"base_head,omitempty"`
}

func draftField(table string) string {
	switch table {
	case "resource_drafts":
		return "body"
	case "resource_policy_drafts", "ip_drafts", "patch_drafts", "import_drafts":
		return "payload"
	default:
		return ""
	}
}

func validDraftVersion(table, field string, v *DraftVersionEvidence) bool {
	if v == nil || draftField(table) != field || field == "" || !hash64.MatchString(v.ProjectionHash) {
		return false
	}
	if v.BaseHead != "" && ((!id32.MatchString(v.BaseHead)) || (table != "resource_drafts" && table != "ip_drafts")) {
		return false
	}
	n, err := strconv.ParseInt(v.Version, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == v.Version
}

func draftVersionEvidence(row SourceRow, field string) (*DraftVersionEvidence, error) {
	if draftField(row.Table) != field {
		return nil, nil
	}
	v := &DraftVersionEvidence{Version: row.Values["revision"], ProjectionHash: financialDigest([]byte(row.Values[field]))}
	if row.Table == "resource_drafts" || row.Table == "ip_drafts" {
		v.BaseHead = row.Values["base_head"]
	}
	if !validDraftVersion(row.Table, field, v) {
		return nil, nil
	}
	if row.Table == "resource_policy_drafts" {
		var policy *domain.KnowledgePolicy
		// Invalid legacy drafts remain saved author content, but cannot supply
		// a falsely reconstructed working-input projection.
		if authorDocument(row.Values[field], &policy) != nil {
			return nil, nil
		}
		v.ProjectionHash = stableHash(policy)
	}
	return v, nil
}

func sameDraftVersion(a, b *DraftVersionEvidence) bool { return a != nil && b != nil && *a == *b }

func (index *SourceIndex) attachPrivateDraftSources(project string, materials []MaterialReference) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "working_indexes" || m.Field != "manifest" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		var manifest domain.PrivateManifest
		if authorDocument(row.Values["manifest"], &manifest) != nil || stableHash(manifest) != row.Values["id"] || manifest.Project != project || manifest.Branch != row.Values["branch_id"] || manifest.Actor != row.Values["actor_id"] || manifest.Model != row.Values["model_revision"] || manifest.Parser != "private-unicode-512-overlap32-v1" || len(manifest.Sources) > 4096 {
			return ErrSourceOwnership
		}
		for _, pin := range manifest.Sources {
			if !hash64.MatchString(pin.Hash) || !id32.MatchString(pin.Ref.ID) || pin.Part < 0 {
				return ErrSourceOwnership
			}
			source, err := index.privateInputSource(project, manifest, pin)
			if err != nil {
				return err
			}
			if err := appendSourceRequest(m, source); err != nil {
				return err
			}
		}
	}
	return nil
}

func (index *SourceIndex) privateInputSource(project string, manifest domain.PrivateManifest, pin domain.PrivatePin) (MaterialSource, error) {
	if pin.Ref.Kind != "resource_draft" {
		return index.privateProjectionSource(project, manifest, pin)
	}
	table, field := "resource_drafts", "body"
	if pin.Part == 1 && pin.Format == "structured" {
		table, field = "resource_policy_drafts", "payload"
	} else if pin.Part != 0 || pin.Format != "text" {
		return MaterialSource{}, ErrSourceOwnership
	}
	v := &DraftVersionEvidence{Version: pin.Ref.Version, ProjectionHash: pin.Hash}
	if table == "resource_drafts" {
		v.BaseHead = pin.BaseHead
	}
	if pin.State != "PRIVATE_DRAFT" || !validDraftVersion(table, field, v) {
		return MaterialSource{}, ErrSourceOwnership
	}
	resource, err := index.sourceRow("resources", map[string]string{"id": pin.Ref.ID})
	if err != nil || resource.Values["project_id"] != project || resource.Values["branch_id"] != manifest.Branch {
		return MaterialSource{}, ErrSourceOwnership
	}
	key, err := RowOwnerKey(table, map[string]string{"resource_id": pin.Ref.ID, "actor_id": manifest.Actor})
	if err != nil {
		return MaterialSource{}, err
	}
	id, err := ReferenceID(project, table, key, field)
	if err != nil {
		return MaterialSource{}, err
	}
	return MaterialSource{ReferenceID: id, DraftVersion: v}, nil
}
