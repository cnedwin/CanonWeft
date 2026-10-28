package novel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestBookMarkdownAndArchiveContract(t *testing.T) {
	book := ExportBook{ID: "version-one", Title: "雨夜 *旧稿*", Author: "作者", Language: "zh-Hans", Description: "简介", Notice: "作者存档", Chapters: []ExportChapter{{Volume: "卷一", Title: "雨夜", Text: "原始正文 *不可改写*。\n下一段。"}}}
	md, ok := any(book).(interface{ Markdown() string })
	if !ok {
		t.Fatal("Markdown export is missing")
	}
	text := md.Markdown()
	if !strings.Contains(text, "# 雨夜 \\*旧稿\\*") || !strings.Contains(text, "## 卷一") || !strings.Contains(text, book.Chapters[0].Text) {
		t.Fatal("Markdown damaged title, order or manuscript")
	}
	pack, ok := any(book).(interface{ Archive([]byte) ([]byte, error) })
	if !ok {
		t.Fatal("independent edition archive is missing")
	}
	data, e := pack.Archive([]byte(`{"version":1}`))
	if e != nil || !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatal("archive", e)
	}
	reopened, meta, e := ReadExportArchive(data)
	if e != nil || string(meta) != `{"version":1}` || reopened.TXT() != book.TXT() {
		t.Fatal("edition changed original", e)
	}
	var pixels bytes.Buffer
	_ = png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	book.Cover, e = NewExportCover(pixels.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	data, e = book.Archive([]byte(`{"version":1}`))
	if e != nil {
		t.Fatal(e)
	}
	reopened, _, e = ReadExportArchive(data)
	if e != nil || reopened.Cover == nil || !bytes.Equal(reopened.Cover.Data, pixels.Bytes()) {
		t.Fatal("archive cover lost", e)
	}
	for _, names := range [][]string{{"book.json", "book.json"}, {"../book.json", "edition.json"}, {"book.json", "edition.json", "script.js"}} {
		var bad bytes.Buffer
		z := zip.NewWriter(&bad)
		for _, name := range names {
			w, _ := z.Create(name)
			raw, _ := json.Marshal(book)
			_, _ = w.Write(raw)
		}
		_ = z.Close()
		if _, _, e = ReadExportArchive(bad.Bytes()); e == nil {
			t.Fatal("accepted invalid archive entries", names)
		}
	}
}
