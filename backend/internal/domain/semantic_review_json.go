package domain

import (
	"bytes"
	"encoding/json"
	"io"
)

// Duplicate members have no unambiguous meaning for a signed report. Reject
// them before Go's struct decoder could silently choose the last value.
func validateReviewJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return ErrValidation
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrValidation
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				token, err = decoder.Token()
				if err != nil {
					return ErrValidation
				}
				key, ok := token.(string)
				if !ok || keys[key] {
					return ErrValidation
				}
				keys[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim('}') {
				return ErrValidation
			}
		case '[':
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return ErrValidation
			}
		default:
			return ErrValidation
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrValidation
	}
	return nil
}
