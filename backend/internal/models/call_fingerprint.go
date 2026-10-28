package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CallFingerprint binds the original request to its reservation scope and
// quoted amount. It is not the hash of the raw request object alone.
func CallFingerprint(project string, amount int64, currency, inputHash string) string {
	encoded, _ := json.Marshal([]any{project, amount, currency, inputHash})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
