package novel

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"image/png"

	_ "golang.org/x/image/webp"
	"novelstudio/internal/domain"
)

// ExportCover holds local, verified pixels. It never fetches remote URLs.
type ExportCover struct {
	Data          []byte
	MIME          string
	Width, Height int
}

// NewExportCover preserves PNG/JPEG bytes and converts WebP only in the export
// copy, retaining its original dimensions. Source-import bytes remain immutable.
func NewExportCover(data []byte) (*ExportCover, error) {
	cfg, format, err := decodeCoverConfig(data)
	if err != nil {
		return nil, err
	}
	pixels, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format {
		return nil, domain.ErrValidation
	}
	if format == "webp" {
		var buf bytes.Buffer
		if err = png.Encode(&buf, pixels); err != nil {
			return nil, err
		}
		data, format = buf.Bytes(), "png"
		if len(data) > 32*1024*1024 {
			return nil, domain.ErrValidation
		}
	}
	return &ExportCover{Data: data, MIME: "image/" + format, Width: cfg.Width, Height: cfg.Height}, nil
}

func decodeCoverConfig(data []byte) (image.Config, string, error) {
	if len(data) == 0 || len(data) > 32*1024*1024 {
		return image.Config{}, "", domain.ErrValidation
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "webp") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 24_000_000 {
		return image.Config{}, "", domain.ErrValidation
	}
	return cfg, format, nil
}

func (c *ExportCover) validate() (string, error) {
	cfg, format, err := decodeCoverConfig(c.Data)
	if err != nil || format == "webp" || c.MIME != "image/"+format || cfg.Width != c.Width || cfg.Height != c.Height {
		return "", domain.ErrValidation
	}
	if _, decoded, e := image.Decode(bytes.NewReader(c.Data)); e != nil || decoded != format {
		return "", domain.ErrValidation
	}
	if format == "jpeg" {
		return "jpg", nil
	}
	return "png", nil
}
