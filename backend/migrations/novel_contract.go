package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const NovelEntryVersion = 37
const NovelEntryName = "0037_novel_entry.sql"
const NovelSettingsName = "0038_novel_settings_drafts.sql"

func NovelSettingsSQL() (string, string, error) {
	raw, err := Files.ReadFile(NovelSettingsName)
	if err != nil {
		return "", "", err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	hash := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(hash[:]), nil
}

func NovelEntrySQL() (string, string, error) {
	raw, err := Files.ReadFile(NovelEntryName)
	if err != nil {
		return "", "", err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	hash := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(hash[:]), nil
}
