package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Rule takes the complete table.path key. ReferenceID's field is table-local.
func Rule(key string) (ReferenceRule, bool) { return lookupRule(key) }

// DigestKeyPart represents a free-form primary-key component without retaining
// its content. Adapters must use this for operation IDs and named business keys.
func DigestKeyPart(value string) string {
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func validOwnerPart(part string) bool {
	if id32.MatchString(part) || hash64.MatchString(part) {
		return true
	}
	if strings.HasPrefix(part, "sha256:") {
		return hash64.MatchString(strings.TrimPrefix(part, "sha256:"))
	}
	number, err := strconv.ParseUint(part, 10, 64)
	return err == nil && strconv.FormatUint(number, 10) == part
}

// OwnerKey is canonical JSON of a string tuple in exact schema PK order.
// IDs/hashes and decimal integers use literal components; other keys use digests.
func OwnerKey(parts ...string) (string, error) {
	if len(parts) == 0 {
		return "", ErrInvalidInventory
	}
	for _, part := range parts {
		if !validOwnerPart(part) {
			return "", ErrInvalidInventory
		}
	}
	raw, err := json.Marshal(parts)
	if err != nil || len(raw) > 256 {
		return "", ErrInvalidInventory
	}
	return string(raw), nil
}

func ReferenceID(project, table, ownerKey, field string) (string, error) {
	if !id32.MatchString(project) {
		return "", ErrInvalidInventory
	}
	if _, ok := ConcreteRule(table + "." + field); !ok {
		return "", ErrInvalidInventory
	}
	var parts []string
	if json.Unmarshal([]byte(ownerKey), &parts) != nil {
		return "", ErrInvalidInventory
	}
	canonical, err := OwnerKey(parts...)
	if err != nil || canonical != ownerKey {
		return "", ErrInvalidInventory
	}
	return stableHash([]string{project, table, ownerKey, field}), nil
}
