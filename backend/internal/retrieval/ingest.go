package retrieval

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"novelstudio/internal/research"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrImport = errors.New("文件格式、编码或大小不符合要求")

const maxExtracted = 256 * 1024

type ImportUnit struct {
	Locator    string   `json:"locator"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Text       string   `json:"text,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}
type ImportReport struct {
	Research       *research.Provenance `json:"research,omitempty"`
	NovelEdition   *NovelEdition        `json:"novel_edition,omitempty"`
	NovelImage     *NovelImage          `json:"novel_image,omitempty"`
	CharacterImage *CharacterImage      `json:"character_image,omitempty"`
	Parser         string               `json:"parser"`
	Complete       bool                 `json:"complete"`
	Extracted      int                  `json:"extracted"`
	Pending        int                  `json:"pending"`
	Text           string               `json:"text"`
	Units          []ImportUnit         `json:"units"`
}

func Extract(ctx context.Context, name string, data []byte) (ImportReport, error) {
	report := ImportReport{Parser: "bounded-native-v1", Units: []ImportUnit{}}
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if len(data) == 0 || len(data) > 32*1024*1024 {
		return report, ErrImport
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md":
		if !utf8.Valid(data) || bytes.Contains(data, []byte{0}) || len(data) > maxExtracted {
			return report, ErrImport
		}
		text := strings.TrimPrefix(string(data), "\ufeff")
		if strings.TrimSpace(text) != "" {
			report.Units = append(report.Units, ImportUnit{Locator: "file:whole", Kind: "text", Status: "extracted", Text: text})
		} else {
			report.Units = append(report.Units, ImportUnit{Locator: "file:whole", Kind: "text", Status: "pending", Reason: "文件没有可读文本"})
		}
	case ".docx":
		if err := extractDOCX(ctx, data, &report); err != nil {
			return report, err
		}
	case ".pdf", ".png", ".jpg", ".jpeg", ".webp":
		var err error
		report, err = extractDocument(ctx, name, data)
		if err != nil {
			return report, err
		}
	default:
		return report, ErrImport
	}
	texts := []string{}
	for _, unit := range report.Units {
		if unit.Status == "extracted" {
			report.Extracted++
			texts = append(texts, unit.Text)
		} else {
			report.Pending++
		}
	}
	report.Text = strings.Join(texts, "\n\n")
	if len(report.Text) > maxExtracted {
		return report, ErrImport
	}
	report.Complete = report.Pending == 0 && report.Extracted > 0
	return report, nil
}

func extractDOCX(ctx context.Context, data []byte, report *ImportReport) error {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ErrImport
	}
	if len(archive.File) > 2048 {
		return ErrImport
	}
	files := append([]*zip.File(nil), archive.File...)
	sort.Slice(files, func(i, j int) bool {
		if files[i].Name == "word/document.xml" && files[j].Name != "word/document.xml" {
			return true
		}
		if files[j].Name == "word/document.xml" {
			return false
		}
		return files[i].Name < files[j].Name
	})
	var total uint64
	found := false
	seen := map[string]bool{}
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.Contains(file.Name, "\\") || path.IsAbs(file.Name) || strings.HasPrefix(path.Clean(file.Name), "../") || file.Mode()&os.ModeSymlink != 0 || seen[file.Name] {
			return ErrImport
		}
		seen[file.Name] = true
		total += file.UncompressedSize64
		if total > 64*1024*1024 {
			return ErrImport
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(file.Name, "word/media/") || strings.HasPrefix(file.Name, "word/embeddings/") || strings.Contains(file.Name, "vbaProject") {
			report.Units = append(report.Units, ImportUnit{Locator: file.Name, Kind: "attachment", Status: "pending", Reason: "图片或嵌入对象未识别，未执行内容"})
			continue
		}
		isDocument := file.Name == "word/document.xml"
		isTextPart := isDocument || file.Name == "word/footnotes.xml" || file.Name == "word/endnotes.xml" || file.Name == "word/comments.xml" || strings.HasPrefix(file.Name, "word/header") || strings.HasPrefix(file.Name, "word/footer")
		if !isTextPart {
			continue
		}
		if file.UncompressedSize64 > 4*1024*1024 {
			return ErrImport
		}
		if isDocument {
			found = true
		}
		stream, openErr := file.Open()
		if openErr != nil {
			return ErrImport
		}
		raw, readErr := io.ReadAll(io.LimitReader(stream, 4*1024*1024+1))
		stream.Close()
		if readErr != nil || len(raw) > 4*1024*1024 {
			return ErrImport
		}
		if err = extractXML(file.Name, raw, report); err != nil {
			return err
		}
	}
	if !found {
		return ErrImport
	}
	return nil
}
func extractXML(part string, data []byte, report *ImportReport) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var paragraph strings.Builder
	inText := false
	number := 0
	special := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrImport
		}
		switch value := token.(type) {
		case xml.Directive:
			return ErrImport
		case xml.StartElement:
			switch value.Name.Local {
			case "p":
				paragraph.Reset()
				number++
			case "t":
				inText = true
			case "tab":
				paragraph.WriteString("\t")
			case "br":
				paragraph.WriteString("\n")
			case "del", "ins", "instrText", "altChunk", "object", "oMath":
				special++
				report.Units = append(report.Units, ImportUnit{Locator: fmt.Sprintf("%s#special-%d", part, special), Kind: value.Name.Local, Status: "pending", Reason: "修订、域或复杂对象需人工核对"})
			}
		case xml.EndElement:
			if value.Name.Local == "t" {
				inText = false
			}
			if value.Name.Local == "p" && strings.TrimSpace(paragraph.String()) != "" {
				report.Units = append(report.Units, ImportUnit{Locator: fmt.Sprintf("%s#paragraph-%d", part, number), Kind: "paragraph", Status: "extracted", Text: paragraph.String()})
				paragraph.Reset()
			}
		case xml.CharData:
			if inText {
				paragraph.Write(value)
				if paragraph.Len() > maxExtracted {
					return ErrImport
				}
			}
		}
		if len(report.Units) > 10000 {
			return ErrImport
		}
	}
	return nil
}
