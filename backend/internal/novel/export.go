package novel

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

// ExportBook contains only selected manuscript text, never ledgers or hidden memory.
type ExportBook struct {
	ID, Title, Author, Language, Description, Notice string
	Chapters                                         []ExportChapter
	Cover                                            *ExportCover
}
type ExportChapter struct{ Volume, Title, Text string }

func (b ExportBook) TXT() string {
	var out strings.Builder
	out.WriteString(b.Title + "\n" + b.Author + "\n\n" + b.Description + "\n\n" + b.Notice + "\n")
	volume := ""
	for _, c := range b.Chapters {
		if c.Volume != volume {
			out.WriteString("\n" + c.Volume + "\n")
			volume = c.Volume
		}
		out.WriteString("\n" + c.Title + "\n\n" + c.Text + "\n")
	}
	return out.String()
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func validXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !(r == 9 || r == 10 || r == 13 || (r >= 0x20 && r <= 0xd7ff) || (r >= 0xe000 && r <= 0xfffd) || (r >= 0x10000 && r <= 0x10ffff)) {
			return false
		}
	}
	return true
}

// EPUB follows EPUB 3 OCF/package/navigation; all manuscript text is escaped.
// No renderer, remote resource, or external executable is involved.
func (b ExportBook) EPUB(now time.Time) ([]byte, error) {
	if len(b.Chapters) == 0 || strings.TrimSpace(b.Title) == "" || b.ID == "" || b.Language == "" {
		return nil, domain.ErrValidation
	}
	coverExtension := ""
	if b.Cover != nil {
		var err error
		coverExtension, err = b.Cover.validate()
		if err != nil {
			return nil, err
		}
	}
	fields := []string{b.ID, b.Title, b.Author, b.Language, b.Description, b.Notice}
	for _, c := range b.Chapters {
		fields = append(fields, c.Volume, c.Title, c.Text)
	}
	for _, s := range fields {
		if !validXMLText(s) {
			return nil, fmt.Errorf("%w：正文包含电子书不支持的控制字符，请先修正原稿", domain.ErrValidation)
		}
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	add := func(name, body string, method uint16) error {
		w, e := z.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if e != nil {
			return e
		}
		_, e = w.Write([]byte(body))
		return e
	}
	if e := add("mimetype", "application/epub+zip", zip.Store); e != nil {
		return nil, e
	}
	if e := add("META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate); e != nil {
		return nil, e
	}
	page := func(title, body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="` + xmlText(b.Language) + `" xml:lang="` + xmlText(b.Language) + `"><head><title>` + xmlText(title) + `</title><link rel="stylesheet" type="text/css" href="style.css"/></head><body>` + body + `</body></html>`
	}
	paragraphs := func(text string) string {
		var out strings.Builder
		for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			out.WriteString("<p>" + xmlText(p) + "</p>")
		}
		return out.String()
	}
	manifest := `<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="front" href="front.xhtml" media-type="application/xhtml+xml"/><item id="style" href="style.css" media-type="text/css"/>`
	spine := `<itemref idref="front"/>`
	nav := `<nav epub:type="toc" id="toc"><h1>目录</h1><ol><li><a href="front.xhtml">书籍信息</a></li>`
	if b.Cover != nil {
		name := "cover." + coverExtension
		manifest += `<item id="cover-image" href="` + name + `" media-type="` + b.Cover.MIME + `" properties="cover-image"/><item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>`
		spine = `<itemref idref="cover"/>` + spine
		nav = `<nav epub:type="landmarks" hidden="hidden"><ol><li><a epub:type="cover" href="cover.xhtml">封面</a></li></ol></nav>` + nav
		if e := add("EPUB/"+name, string(b.Cover.Data), zip.Store); e != nil {
			return nil, e
		}
		if e := add("EPUB/cover.xhtml", page("封面", `<section epub:type="cover" class="cover"><img src="`+name+`" alt="`+xmlText(b.Title)+`的封面"/></section>`), zip.Deflate); e != nil {
			return nil, e
		}
	}
	volume := ""
	volumeOpen := false
	for i, c := range b.Chapters {
		id := fmt.Sprintf("chapter-%d", i+1)
		manifest += `<item id="` + id + `" href="` + id + `.xhtml" media-type="application/xhtml+xml"/>`
		spine += `<itemref idref="` + id + `"/>`
		if !volumeOpen || volume != c.Volume {
			if volumeOpen {
				nav += `</ol></li>`
			}
			nav += `<li><span>` + xmlText(c.Volume) + `</span><ol>`
			volume = c.Volume
			volumeOpen = true
		}
		nav += `<li><a href="` + id + `.xhtml">` + xmlText(c.Title) + `</a></li>`
		if e := add("EPUB/"+id+".xhtml", page(c.Title, "<h1>"+xmlText(c.Title)+"</h1>"+paragraphs(c.Text)), zip.Deflate); e != nil {
			return nil, e
		}
	}
	if volumeOpen {
		nav += `</ol></li>`
	}
	nav += `</ol></nav>`
	opf := `<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="book-id">` + xmlText(b.ID) + `</dc:identifier><dc:title>` + xmlText(b.Title) + `</dc:title><dc:language>` + xmlText(b.Language) + `</dc:language><dc:creator>` + xmlText(b.Author) + `</dc:creator><dc:description>` + xmlText(b.Description) + `</dc:description><meta property="dcterms:modified">` + now.UTC().Format("2006-01-02T15:04:05Z") + `</meta></metadata><manifest>` + manifest + `</manifest><spine>` + spine + `</spine></package>`
	for _, f := range []struct{ name, body string }{
		{"EPUB/package.opf", opf},
		{"EPUB/nav.xhtml", page("目录", nav)},
		{"EPUB/front.xhtml", page(b.Title, "<h1>"+xmlText(b.Title)+"</h1><p>"+xmlText(b.Author)+"</p>"+paragraphs(b.Description)+"<aside>"+paragraphs(b.Notice)+"</aside>")},
		{"EPUB/style.css", "body{line-height:1.8;margin:5%;font-family:serif}p{white-space:pre-wrap;min-height:1em}h1{line-height:1.4}aside{font-size:.85em}.cover{text-align:center}.cover img{display:block;margin:0 auto;max-width:100%;max-height:90vh;width:auto;height:auto;object-fit:contain}"},
	} {
		if e := add(f.name, f.body, zip.Deflate); e != nil {
			return nil, e
		}
	}
	if e := z.Close(); e != nil {
		return nil, e
	}
	return buf.Bytes(), nil
}
