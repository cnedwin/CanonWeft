package retrieval

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

func TestCharacterImageWebPPixels(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAkAAAAdQrYIUr/+BiOh/AAA=")
	input := CharacterImage{CharacterID: "hero", Look: "青衣", View: "front", Source: "测试自绘", Rights: "原创", MIME: "image/png", Width: 999}
	report, err := InspectCharacterImage(input, data)
	if err != nil || report.CharacterImage.MIME != "image/webp" || report.CharacterImage.Width != 3 || report.CharacterImage.Height != 2 {
		t.Fatal("WebP actual pixels rejected", report, err)
	}
	if _, err = InspectCharacterImage(input, data[:len(data)-8]); err == nil {
		t.Fatal("truncated WebP accepted")
	}
}

func TestCharacterImageChecksActualPixelsAndMetadata(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	input := CharacterImage{CharacterID: "hero", Look: "青衣", View: "front", Source: "本人绘制", Rights: "本人原创", MIME: "text/html", Width: 999}
	report, err := InspectCharacterImage(input, data.Bytes())
	if err != nil || report.CharacterImage.MIME != "image/png" || report.CharacterImage.Width != 3 || report.CharacterImage.Height != 2 || report.Text != "" {
		t.Fatal("invalid inspection", report, err)
	}
	for _, bad := range [][]byte{[]byte("<svg onload='bad()'></svg>"), data.Bytes()[:33], append([]byte("<html>"), data.Bytes()...)} {
		if _, err := InspectCharacterImage(input, bad); err == nil {
			t.Fatal("accepted incomplete or non-raster file")
		}
	}
	input.View = "unknown"
	if _, err := InspectCharacterImage(input, data.Bytes()); err == nil {
		t.Fatal("accepted unknown view")
	}
	input.View, input.CharacterID = "back", ""
	if _, err := InspectCharacterImage(input, data.Bytes()); err == nil {
		t.Fatal("accepted missing identity")
	}
}
