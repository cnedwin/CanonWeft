package retrieval

import "slices"

type NovelImage struct {
	BookID      string                `json:"book_id"`
	Kind        string                `json:"kind"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Source      string                `json:"source"`
	Rights      string                `json:"rights"`
	MIME        string                `json:"mime"`
	Width       int                   `json:"width"`
	Height      int                   `json:"height"`
	Generated   *CharacterImageOrigin `json:"generated,omitempty"`
}

func InspectNovelImage(input NovelImage, data []byte) (ImportReport, error) {
	if !slices.Contains([]string{"cover", "scene"}, input.Kind) || len(input.BookID) != 32 {
		return ImportReport{}, ErrImport
	}
	// Reuse strict raster/metadata inspection; this temporary validation value
	// is never persisted as a character identity or imported into the IP library.
	r, err := InspectCharacterImage(CharacterImage{CharacterID: input.BookID, Look: input.Name, View: "portrait", Description: input.Description, Source: input.Source, Rights: input.Rights}, data)
	if err != nil {
		return ImportReport{}, err
	}
	input.Name = r.CharacterImage.Look
	input.MIME, input.Width, input.Height = r.CharacterImage.MIME, r.CharacterImage.Width, r.CharacterImage.Height
	return ImportReport{Parser: "novel-image-v1", Complete: true, NovelImage: &input, Units: []ImportUnit{}}, nil
}
