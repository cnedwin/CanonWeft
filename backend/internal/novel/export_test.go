package novel

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

func TestBookExportEPUBPreservesChineseAndSafeMarkup(t *testing.T) {
	book := ExportBook{ID: "book-123", Title: "雨夜<&>", Author: "林 & 舟", Language: "zh-Hans", Description: "第一部\n追查真相", Notice: "草稿：尚未完成卷审", Chapters: []ExportChapter{{Volume: "卷一", Title: "第一章", Text: "门上写着 <script>alert(1)</script>。\n\n第二段 & 原样。"}, {Volume: "卷二", Title: "第二章", Text: "后续正文。"}}}
	data, err := book.EPUB(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if z.File[0].Name != "mimetype" || z.File[0].Method != zip.Store {
		t.Fatal("EPUB mimetype must be the first stored entry")
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		files[f.Name] = string(raw)
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".opf") || strings.HasSuffix(f.Name, ".xhtml") {
			dec := xml.NewDecoder(bytes.NewReader(raw))
			for {
				_, e = dec.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatalf("%s: %v", f.Name, e)
				}
			}
		}
	}
	if files["mimetype"] != "application/epub+zip" {
		t.Fatal("wrong mimetype")
	}
	if !strings.Contains(files["EPUB/chapter-1.xhtml"], "&lt;script&gt;") || strings.Contains(files["EPUB/chapter-1.xhtml"], "<script>") {
		t.Fatal("unsafe manuscript markup")
	}
	if !strings.Contains(files["EPUB/nav.xhtml"], "卷二") || !strings.Contains(files["EPUB/package.opf"], `idref="chapter-2"`) || !strings.Contains(files["EPUB/front.xhtml"], book.Notice) {
		t.Fatal("missing navigation, reading order or draft notice")
	}
	txt := book.TXT()
	if !strings.Contains(txt, "<script>alert(1)</script>") || strings.Index(txt, "第一章") > strings.Index(txt, "第二章") {
		t.Fatal("TXT changed manuscript or order")
	}
	book.Chapters[0].Text = "illegal\x00text"
	if _, e := book.EPUB(time.Now()); e == nil {
		t.Fatal("invalid XML silently damaged manuscript")
	}
}
