package backup

import "novelstudio/migrations"

func hasRetentionSchema(schema int) bool { return schema >= 36 && schema <= 38 }

func novelSettingsTableCatalog() []tableSpec {
	return []tableSpec{{"novel_settings_drafts", "novel_id,actor_id,revision,base_revision,payload,content_hash,updated_at"}}
}

func novelCatalogForSchema(schema int) []tableSpec {
	tables := novelTableCatalog()
	if schema == 38 {
		tables = append(tables, novelSettingsTableCatalog()...)
	}
	return tables
}

func novelTableCatalog() []tableSpec {
	return []tableSpec{
		{"novel_setup_drafts", "id,workspace_id,actor_id,revision,step,payload,content_hash,confirmed_novel_id,created_at,updated_at"},
		{"novels", "id,project_id,branch_id,created_by,setup_id,settings_revision,stage,created_at"},
		{"novel_settings_versions", "novel_id,revision,payload,content_hash,created_by,created_at"},
	}
}

// Archive version validation determines whether this extension is permitted.
// Presence here chooses its exact row layout; it never admits unknown tables.
func businessTableCatalog(data Data) []tableSpec {
	if _, ok := data["novel_settings_drafts"]; ok {
		return append(append([]tableSpec{}, tableCatalog...), novelCatalogForSchema(38)...)
	}
	for _, spec := range novelTableCatalog() {
		if _, ok := data[spec.name]; ok {
			return append(append([]tableSpec{}, tableCatalog...), novelTableCatalog()...)
		}
	}
	return tableCatalog
}

func novelSettingsMigration() (Migration, error) {
	_, hash, err := migrations.NovelSettingsSQL()
	return Migration{Version: 38, Name: migrations.NovelSettingsName, SHA256: hash}, err
}

func novelMigration() (Migration, error) {
	_, hash, err := migrations.NovelEntrySQL()
	return Migration{Version: 37, Name: migrations.NovelEntryName, SHA256: hash}, err
}
