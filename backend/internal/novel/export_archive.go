package novel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

const MaxExportArchiveBytes = 256 * 1024 * 1024

func (b ExportBook) Markdown() string {
	heading := func(s string) string {
		return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "\r", " ", "\n", " ", "#", "\\#", "`", "\\`").Replace(s)
	}
	var out strings.Builder
	out.WriteString("# " + heading(b.Title) + "\n\n" + b.Author + "\n\n" + b.Description + "\n\n" + b.Notice + "\n")
	volume := ""
	for _, chapter := range b.Chapters {
		if chapter.Volume != volume {
			out.WriteString("\n## " + heading(chapter.Volume) + "\n")
			volume = chapter.Volume
		}
		out.WriteString("\n### " + heading(chapter.Title) + "\n\n" + chapter.Text + "\n")
	}
	return out.String()
}

func validArchiveBook(b ExportBook) bool {
	if b.ID == "" || b.Title == "" || b.Author == "" || b.Language == "" || len(b.Chapters) == 0 {
		return false
	}
	for _, v := range []string{b.ID, b.Title, b.Author, b.Language, b.Description, b.Notice} {
		if !utf8.ValidString(v) {
			return false
		}
	}
	size := 0
	for _, c := range b.Chapters {
		size += len(c.Text)
		if !utf8.ValidString(c.Text) || !utf8.ValidString(c.Title) || !utf8.ValidString(c.Volume) || size > 32*1024*1024 {
			return false
		}
	}
	return true
}

// Archive is a self-contained author edition, independent of mutable resource
// heads and short-lived model execution materials. No file paths are extracted.
func (b ExportBook) Archive(metadata []byte) ([]byte, error) {
	if !validArchiveBook(b) || !json.Valid(metadata) || len(metadata) > 16*1024*1024 {
		return nil, domain.ErrValidation
	}
	cover := b.Cover
	b.Cover = nil
	if cover != nil {
		if _, err := cover.validate(); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > 208*1024*1024 {
		return nil, domain.ErrValidation
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	entries := []struct {
		name string
		data []byte
	}{{"book.json", raw}, {"edition.json", metadata}}
	if cover != nil {
		entries = append(entries, struct {
			name string
			data []byte
		}{"cover", cover.Data})
	}
	for _, entry := range entries {
		w, e := z.Create(entry.name)
		if e != nil {
			return nil, e
		}
		if _, e = w.Write(entry.data); e != nil {
			return nil, e
		}
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxExportArchiveBytes {
		return nil, domain.ErrValidation
	}
	return out.Bytes(), nil
}

func ReadExportArchive(raw []byte) (ExportBook, []byte, error) {
	var book ExportBook
	fail := func() (ExportBook, []byte, error) { return ExportBook{}, nil, domain.ErrValidation }
	if len(raw) == 0 || len(raw) > MaxExportArchiveBytes {
		return fail()
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(z.File) < 2 || len(z.File) > 3 {
		return fail()
	}
	entries := map[string][]byte{}
	var total uint64
	for _, f := range z.File {
		limit := uint64(0)
		switch f.Name {
		case "book.json":
			limit = 208 * 1024 * 1024
		case "edition.json":
			limit = 16 * 1024 * 1024
		case "cover":
			limit = 32 * 1024 * 1024
		default:
			return fail()
		}
		total += f.UncompressedSize64
		if _, duplicate := entries[f.Name]; duplicate || f.UncompressedSize64 > limit || total > MaxExportArchiveBytes {
			return fail()
		}
		r, e := f.Open()
		if e != nil {
			return fail()
		}
		data, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		r.Close()
		if e != nil || uint64(len(data)) != f.UncompressedSize64 {
			return fail()
		}
		entries[f.Name] = data
	}
	dec := json.NewDecoder(bytes.NewReader(entries["book.json"]))
	dec.DisallowUnknownFields()
	if dec.Decode(&book) != nil || dec.Decode(new(any)) != io.EOF || book.Cover != nil || !validArchiveBook(book) || !json.Valid(entries["edition.json"]) {
		return fail()
	}
	if cover, ok := entries["cover"]; ok {
		book.Cover, err = NewExportCover(cover)
		if err != nil {
			return fail()
		}
	}
	return book, entries["edition.json"], nil
}
