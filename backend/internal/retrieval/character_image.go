package retrieval

import (
	"bytes"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxCharacterImageBytes = 16 * 1024 * 1024

// CharacterImage describes an immutable uploaded reference, not an approved
// depiction. Approval and branch-specific versions belong to Release.
type CharacterImage struct {
	Generated   *CharacterImageOrigin `json:"generated,omitempty"`
	CharacterID string                `json:"character_id"`
	Look        string                `json:"look"`
	View        string                `json:"view"`
	Description string                `json:"description"`
	Source      string                `json:"source"`
	Rights      string                `json:"rights"`
	MIME        string                `json:"mime"`
	Width       int                   `json:"width"`
	Height      int                   `json:"height"`
}

type CharacterImageOrigin struct {
	GenerationID  string `json:"generation_id"`
	Model         string `json:"model"`
	CharacterHash string `json:"character_hash"`
}

func InspectCharacterImage(input CharacterImage, data []byte) (ImportReport, error) {
	invalid := func() (ImportReport, error) { return ImportReport{}, ErrImport }
	if len(data) == 0 || len(data) > MaxCharacterImageBytes {
		return invalid()
	}
	for _, v := range []struct {
		text     string
		max      int
		required bool
	}{
		{input.CharacterID, 160, true}, {input.Look, 120, true},
		{input.Description, 4000, false}, {input.Source, 1000, true}, {input.Rights, 1000, true},
	} {
		if !utf8.ValidString(v.text) || utf8.RuneCountInString(v.text) > v.max || strings.ContainsRune(v.text, 0) || (v.required && strings.TrimSpace(v.text) == "") {
			return invalid()
		}
	}
	if !slices.Contains([]string{"portrait", "front", "side", "back", "three_view", "expression", "outfit", "prop"}, input.View) {
		return invalid()
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !slices.Contains([]string{"png", "jpeg", "webp"}, format) || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 24_000_000 {
		return invalid()
	}
	// Decode the complete image as well: a valid header alone is not a file.
	if _, decoded, err := image.Decode(bytes.NewReader(data)); err != nil || decoded != format {
		return invalid()
	}
	input.MIME, input.Width, input.Height = "image/"+format, config.Width, config.Height
	input.Look = strings.TrimSpace(input.Look)
	return ImportReport{Parser: "character-image-v1", Complete: true, CharacterImage: &input, Units: []ImportUnit{}}, nil
}
