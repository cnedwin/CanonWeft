package novel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBookExportAdoptedCover(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 300, 450))
	for y := 0; y < 450; y++ {
		for x := 0; x < 300; x++ {
			pixels.Set(x, y, color.RGBA{30, uint8(40 + y/3), uint8(80 + x/3), 255})
		}
	}
	var raw bytes.Buffer
	if e := png.Encode(&raw, pixels); e != nil {
		t.Fatal(e)
	}
	book := ExportBook{ID: "cover-test", Title: "雨夜钟楼", Author: "测试作者", Language: "zh-Hans", Chapters: []ExportChapter{{Volume: "卷一", Title: "第一章", Text: "钟声越过石桥，雨水沿着窗棂落下。"}}}
	// Decode the exported shape so this regression first proves cover omission.
	value, _ := json.Marshal(map[string]any{"Cover": map[string]any{"Data": raw.Bytes(), "MIME": "image/png", "Width": 300, "Height": 450}})
	if e := json.Unmarshal(value, &book); e != nil {
		t.Fatal(e)
	}
	epub, e := book.EPUB(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(epub), int64(len(epub)))
	if e != nil {
		t.Fatal(e)
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		files[f.Name], e = io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	if !bytes.Equal(files["EPUB/cover.png"], raw.Bytes()) {
		t.Fatal("EPUB omitted or changed selected cover pixels")
	}
	opf := string(files["EPUB/package.opf"])
	if !strings.Contains(opf, `properties="cover-image"`) || !strings.Contains(opf, `<spine><itemref idref="cover"/><itemref idref="front"/>`) || !bytes.Contains(files["EPUB/cover.xhtml"], []byte(`src="cover.png"`)) {
		t.Fatal("cover missing manifest, reading order or image page")
	}
	pdf, e := book.PDF(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("PDF missing cover image")
	}
	if path := os.Getenv("STUDIO_PDF_COVER_PROOF"); path != "" {
		if e = os.WriteFile(path, pdf, 0600); e != nil {
			t.Fatal(e)
		}
	}
	value, _ = json.Marshal(map[string]any{"Cover": map[string]any{"Data": []byte("<svg onload='bad()'/>"), "MIME": "image/png", "Width": 300, "Height": 450}})
	_ = json.Unmarshal(value, &book)
	if _, e = book.EPUB(time.Now()); e == nil {
		t.Fatal("EPUB accepted forged cover")
	}
	if _, e = book.PDF(context.Background()); e == nil {
		t.Fatal("PDF accepted forged cover")
	}
}
