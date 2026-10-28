package models

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"novelstudio/internal/domain"
	"strings"
	"unicode/utf8"
)

// Integer coordinates in 1/10000 of the original image, independent of preview
// scale. The first pinned reference is the only image the mask applies to.
type ImageRectangle struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
type ImageRegion struct {
	SourceID string           `json:"source_id"`
	Width    int              `json:"width"`
	Height   int              `json:"height"`
	Areas    []ImageRectangle `json:"areas"`
	Preserve string           `json:"preserve"`
}

func (r ImageRegion) Validate() error {
	if r.SourceID == "" || len(r.SourceID) > 160 || r.Width < 1 || r.Height < 1 || r.Width > 24000000 || r.Height > 24000000 || int64(r.Width)*int64(r.Height) > 24000000 || len(r.Areas) < 1 || len(r.Areas) > 16 || strings.TrimSpace(r.Preserve) == "" || !utf8.ValidString(r.Preserve) || utf8.RuneCountInString(r.Preserve) > 4000 || strings.ContainsRune(r.Preserve, 0) {
		return domain.ErrValidation
	}
	for _, a := range r.Areas {
		if a.X < 0 || a.Y < 0 || a.X >= 10000 || a.Y >= 10000 || a.Width < 1 || a.Height < 1 || a.Width > 10000 || a.Height > 10000 || a.X+a.Width > 10000 || a.Y+a.Height > 10000 {
			return domain.ErrValidation
		}
	}
	return nil
}

func (r ImageRegion) MaskPNG() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	mask := image.NewNRGBA(image.Rect(0, 0, r.Width, r.Height))
	draw.Draw(mask, mask.Bounds(), image.NewUniform(color.NRGBA{A: 255}), image.Point{}, draw.Src)
	for _, a := range r.Areas {
		// Floor the top/left and ceil the bottom/right to retain even a narrow
		// user-selected area. Never make an invisible empty mask after scaling.
		rect := image.Rect(a.X*r.Width/10000, a.Y*r.Height/10000, ((a.X+a.Width)*r.Width+9999)/10000, ((a.Y+a.Height)*r.Height+9999)/10000)
		draw.Draw(mask, rect, image.Transparent, image.Point{}, draw.Src)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, mask); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
