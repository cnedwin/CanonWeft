package qualificationledger

import (
	"bytes"
	"encoding/json"
	"io"
)

// Expiry's closed wire structs contain no arbitrary-key maps. Require canonical
// lowercase ASCII member names, rejecting both duplicate members and Go's
// otherwise case-insensitive struct-field aliases that could hide erased text.
func uniqueExpiredJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errExpiredLegacy
		}
		token, err := decoder.Token()
		if err != nil {
			return errExpiredLegacy
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err = decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || !expiredMemberName(key) || seen[key] {
					return errExpiredLegacy
				}
				seen[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim('}') {
				return errExpiredLegacy
			}
		case '[':
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return errExpiredLegacy
			}
		default:
			return errExpiredLegacy
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errExpiredLegacy
	}
	return nil
}

func expiredMemberName(key string) bool {
	if len(key) == 0 || len(key) > 64 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, char := range key {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}
