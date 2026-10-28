package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PendingRetentionSQL is the frozen schema36 contract shared by the application
// migrator, archive and live scan admission. The historical split filenames and
// function name stay unchanged to preserve exact pre-activation archive hashes.
func PendingRetentionSQL() (text, checksum string, err error) {
	var sql strings.Builder
	for _, name := range []string{"retention.sql.pending", "retention_identity.sql.pending", "retention_sources.sql.pending"} {
		raw, err := PendingFiles.ReadFile(name)
		if err != nil {
			return "", "", err
		}
		sql.WriteString(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"))
		sql.WriteString("\n")
	}
	text = sql.String()
	hash := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(hash[:]), nil
}
