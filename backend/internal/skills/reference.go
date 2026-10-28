package skills

import (
	"encoding/json"
	"unicode/utf8"
)

// ReferenceText includes every file; binary attachments are represented by
// digest, never interpreted or executed. Field order is the existing contract.
func ReferenceText(bundle Bundle) (string, error) {
	if err := bundle.Validate(); err != nil {
		return "", err
	}
	type file struct {
		Path   string `json:"path"`
		Hash   string `json:"hash"`
		Text   string `json:"text,omitempty"`
		Binary bool   `json:"binary,omitempty"`
	}
	files := []file{}
	for _, f := range bundle.Files {
		entry := file{Path: f.Path, Hash: Hash(f.Data), Binary: !utf8.Valid(f.Data)}
		if !entry.Binary {
			entry.Text = string(f.Data)
		}
		files = append(files, entry)
	}
	raw, err := json.Marshal(files)
	return string(raw), err
}
