package retention

import (
	"encoding/json"
	"strconv"

	"novelstudio/internal/novel"
)

func (index *SourceIndex) validateNovelLinks(row SourceRow, owner SourceOwner) error {
	if row.Table == "novels" {
		draft, err := index.sourceRow("novel_setup_drafts", map[string]string{"id": row.Values["setup_id"]})
		if err != nil || draft.Values["confirmed_novel_id"] != row.Values["id"] || draft.Values["workspace_id"] != owner.WorkspaceID || draft.Values["actor_id"] != row.Values["created_by"] {
			return ErrSourceOwnership
		}
		head, err := index.sourceRow("novel_settings_versions", map[string]string{"novel_id": row.Values["id"], "revision": row.Values["settings_revision"]})
		if err != nil || head.Values["revision"] != row.Values["settings_revision"] || row.Values["stage"] != "WORLD_PLANNING" {
			return ErrSourceOwnership
		}
		return nil
	}
	var setup novel.Setup
	if financialDigest([]byte(row.Values["payload"])) != row.Values["content_hash"] || json.Unmarshal([]byte(row.Values["payload"]), &setup) != nil {
		return ErrSourceOwnership
	}
	complete := row.Table == "novel_settings_versions" || row.Values["confirmed_novel_id"] != ""
	if setup.Validate(complete) != nil {
		return ErrSourceOwnership
	}
	if row.Table == "novel_settings_drafts" {
		base, err := index.sourceRow("novel_settings_versions", map[string]string{"novel_id": row.Values["novel_id"], "revision": row.Values["base_revision"]})
		var previous novel.Setup
		if err != nil || json.Unmarshal([]byte(base.Values["payload"]), &previous) != nil || novel.ValidateSettingsEdit(previous, setup, false) != nil {
			return ErrSourceOwnership
		}
		revision, err := strconv.ParseInt(row.Values["revision"], 10, 64)
		if err != nil || revision < 1 {
			return ErrSourceOwnership
		}
	}
	if row.Table == "novel_setup_drafts" {
		step, err := strconv.Atoi(row.Values["step"])
		if err != nil || step < 1 || step > 5 {
			return ErrSourceOwnership
		}
		if !complete {
			return nil
		}
		book, err := index.sourceRow("novels", map[string]string{"id": row.Values["confirmed_novel_id"]})
		if err != nil || book.Values["setup_id"] != row.Values["id"] || book.Values["created_by"] != row.Values["actor_id"] || book.Values["project_id"] != owner.ProjectID {
			return ErrSourceOwnership
		}
	} else {
		book, err := index.sourceRow("novels", map[string]string{"id": row.Values["novel_id"]})
		if err != nil || book.Values["project_id"] != owner.ProjectID {
			return ErrSourceOwnership
		}
	}
	return nil
}
