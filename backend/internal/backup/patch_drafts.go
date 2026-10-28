package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"strconv"
)

func upgradePatchDrafts(a *Archive) error {
	if a.Manifest.Schema >= 32 {
		return nil
	}
	if _, exists := a.Data["patch_drafts"]; exists {
		return errors.New("unexpected patch draft table in legacy archive")
	}
	a.Data["patch_drafts"] = nil
	return nil
}

func validatePatchDraftBackup(d Data) error {
	return validatePatchDraftBackupWithEvidence(d, nil)
}

func validatePatchDraftBackupWithEvidence(d Data, expired map[string]retention.ExpiredProjection) error {
	projects, branches, users := map[string]string{}, map[string]string{}, map[string]string{}
	var spec tableSpec
	for _, s := range tableCatalog {
		if s.name == "patch_drafts" {
			spec = s
		}
		for _, r := range d[s.name] {
			switch s.name {
			case "projects":
				projects[s.value(r, "id")] = s.value(r, "workspace_id")
			case "branches":
				branches[s.value(r, "id")] = s.value(r, "project_id")
			case "users":
				users[s.value(r, "id")] = s.value(r, "workspace_id")
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range d["patch_drafts"] {
		var content domain.PatchDraftContent
		raw := []byte(spec.value(r, "payload"))
		revision, err := strconv.ParseInt(spec.value(r, "revision"), 10, 64)
		project, branch, actor := spec.value(r, "project_id"), spec.value(r, "branch_id"), spec.value(r, "actor_id")
		key := branch + ":" + actor
		if err != nil || revision < 1 || seen[key] || projects[project] == "" || branches[branch] != project || users[actor] != projects[project] {
			return errors.New("invalid private patch draft")
		}
		seen[key] = true
		owner, err := retention.RowOwnerKey("patch_drafts", map[string]string{"branch_id": branch, "actor_id": actor})
		if err != nil {
			return err
		}
		if proof, ok := expired[owner]; ok {
			p, err := retention.ParseSourceProjection(raw, "patch_drafts.payload")
			if err != nil || p.OriginalPayloadHash != spec.value(r, "content_hash") || !recipeJSONEqual(marshalRecipeProof(p), marshalRecipeProof(proof)) {
				return errors.New("invalid expired patch draft evidence")
			}
		} else if digest(raw) != spec.value(r, "content_hash") || json.Unmarshal(raw, &content) != nil || content.Validate() != nil {
			return errors.New("invalid private patch draft")
		}
	}
	return nil
}
