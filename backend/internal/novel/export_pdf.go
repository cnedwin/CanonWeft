package novel

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/signintech/gopdf"
	"novelstudio/internal/domain"
)

// Original, unmodified OFL font. See fonts/OFL.txt and fonts/SOURCE.md.
// Embedding removes dependence on system fonts and permits offline export.
//
//go:embed fonts/LXGWWenKai-Regular.ttf
var novelPDFFont []byte

// Bound concurrent font parsing and long-book layout without holding DB locks.
var pdfExportSlot = make(chan struct{}, 1)

type pdfWidthKey struct {
	size float64
	char rune
}
type novelPDFLayout struct {
	pdf     gopdf.GoPdf
	ctx     context.Context
	y       float64
	missing rune
	widths  map[pdfWidthKey]float64
}

const pdfLeft = 56.0
const pdfTop = 64.0
const pdfBottom = 776.0
const pdfWidth = 483.0

func (b ExportBook) PDF(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case pdfExportSlot <- struct{}{}:
		defer func() { <-pdfExportSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if len(b.Chapters) == 0 || strings.TrimSpace(b.Title) == "" {
		return nil, domain.ErrValidation
	}
	if b.Cover != nil {
		if _, e := b.Cover.validate(); e != nil {
			return nil, e
		}
	}
	p := &novelPDFLayout{ctx: ctx, widths: map[pdfWidthKey]float64{}}
	p.pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	if e := p.pdf.AddTTFFontDataWithOption("novel", novelPDFFont, gopdf.TtfOption{OnGlyphNotFound: func(r rune) {
		if p.missing == 0 {
			p.missing = r
		}
	}}); e != nil {
		return nil, e
	}
	p.pdf.SetInfo(gopdf.PdfInfo{Title: b.Title, Author: b.Author, Subject: b.Description, Creator: "砚台 · 小说工作室", Producer: "gopdf"})
	if b.Cover != nil {
		// The cover is a dedicated, unnumbered page; contain, never stretch/crop.
		p.pdf.AddPage()
		p.pdf.AddOutline("封面")
		scale := math.Min(483/float64(b.Cover.Width), 714/float64(b.Cover.Height))
		w, h := float64(b.Cover.Width)*scale, float64(b.Cover.Height)*scale
		holder, e := gopdf.ImageHolderByBytes(b.Cover.Data)
		if e != nil {
			return nil, e
		}
		if e = p.pdf.ImageByHolder(holder, (595-w)/2, (842-h)/2, &gopdf.Rect{W: w, H: h}); e != nil {
			return nil, e
		}
	}
	if e := p.page(); e != nil {
		return nil, e
	}
	p.y = 140
	for _, part := range []struct {
		text string
		size float64
	}{{b.Title, 26}, {b.Author, 14}, {b.Description, 12}, {b.Notice, 10}} {
		if e := p.paragraphs(part.text, part.size, ""); e != nil {
			return nil, e
		}
		p.y += 20
	}
	if e := p.page(); e != nil {
		return nil, e
	}
	p.pdf.AddOutline("目录")
	if e := p.paragraphs("目录", 22, ""); e != nil {
		return nil, e
	}
	p.y += 16
	volume := ""
	for i, c := range b.Chapters {
		if volume != c.Volume {
			volume = c.Volume
			if e := p.paragraphs(volume, 15, ""); e != nil {
				return nil, e
			}
			p.y += 8
		}
		if e := p.paragraphs(c.Title, 12, fmt.Sprintf("chapter-%d", i)); e != nil {
			return nil, e
		}
	}
	for i, c := range b.Chapters {
		if e := p.page(); e != nil {
			return nil, e
		}
		p.pdf.SetXY(pdfLeft, p.y)
		p.pdf.SetAnchor(fmt.Sprintf("chapter-%d", i))
		p.pdf.AddOutlineWithPosition(c.Title)
		if e := p.paragraphs(c.Volume, 11, ""); e != nil {
			return nil, e
		}
		p.y += 10
		if e := p.paragraphs(c.Title, 20, ""); e != nil {
			return nil, e
		}
		p.y += 18
		if e := p.paragraphs(c.Text, 12, ""); e != nil {
			return nil, e
		}
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := p.glyphError(); e != nil {
		return nil, e
	}
	return p.pdf.GetBytesPdfReturnErr()
}

func (p *novelPDFLayout) glyphError() error {
	if p.missing != 0 {
		return fmt.Errorf("%w：PDF字体缺少字符 %q（U+%04X），请改用EPUB/TXT保留原文", domain.ErrValidation, p.missing, p.missing)
	}
	return nil
}

func (p *novelPDFLayout) page() error {
	if e := p.ctx.Err(); e != nil {
		return e
	}
	if p.pdf.GetNumberOfPages() >= 20000 {
		return fmt.Errorf("%w：PDF超过支持页数，请按卷导出", domain.ErrValidation)
	}
	p.pdf.AddPage()
	if e := p.pdf.SetFont("novel", "", 9); e != nil {
		return e
	}
	p.pdf.SetTextColor(100, 100, 100)
	p.pdf.SetXY(280, 813)
	if e := p.pdf.Text(fmt.Sprintf("第 %d 页", p.pdf.GetNumberOfPages())); e != nil {
		return e
	}
	p.pdf.SetTextColor(25, 25, 25)
	p.y = pdfTop
	return p.glyphError()
}

func (p *novelPDFLayout) paragraphs(text string, size float64, anchor string) error {
	if !validXMLText(text) {
		return fmt.Errorf("%w：正文包含不支持的控制字符", domain.ErrValidation)
	}
	text = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\t", "    ")
	if e := p.pdf.SetFont("novel", "", size); e != nil {
		return e
	}
	leading := size * 1.8
	for _, paragraph := range strings.Split(text, "\n") {
		if e := p.ctx.Err(); e != nil {
			return e
		}
		lines, e := p.wrap(paragraph, size)
		if e != nil {
			return e
		}
		for i, line := range lines {
			// Keep at least two lines of a paragraph together when a page has space
			// for only one; chapter titles always start on a new page.
			if p.y > pdfBottom || (i == 0 && len(lines) > 1 && p.y+leading > pdfBottom) {
				if e = p.page(); e != nil {
					return e
				}
				if e = p.pdf.SetFont("novel", "", size); e != nil {
					return e
				}
			}
			p.pdf.SetXY(pdfLeft, p.y)
			if e = p.pdf.Text(line); e != nil {
				return e
			}
			if e = p.glyphError(); e != nil {
				return e
			}
			if anchor != "" {
				p.pdf.AddInternalLink(anchor, pdfLeft, p.y-size, pdfWidth, leading)
			}
			p.y += leading
		}
		p.y += 5
	}
	return nil
}

func (p *novelPDFLayout) wrap(text string, size float64) ([]string, error) {
	chars := []rune(text)
	if len(chars) == 0 {
		return []string{""}, nil
	}
	widths := make([]float64, len(chars))
	for i, r := range chars {
		key := pdfWidthKey{size, r}
		w, ok := p.widths[key]
		if !ok {
			var e error
			w, e = p.pdf.MeasureTextWidth(string(r))
			if e != nil {
				return nil, e
			}
			if e = p.glyphError(); e != nil {
				return nil, e
			}
			p.widths[key] = w
		}
		widths[i] = w
	}
	lines := []string{}
	for start := 0; start < len(chars); {
		end := start
		width := 0.0
		lastBreak := start
		for end < len(chars) && width+widths[end] <= pdfWidth {
			width += widths[end]
			end++
			if end < len(chars) && pdfCanBreak(chars[end-1], chars[end]) {
				lastBreak = end
			}
		}
		if end == start {
			end++
		} else if end < len(chars) && lastBreak > start {
			end = lastBreak
		}
		lines = append(lines, string(chars[start:end]))
		start = end
	}
	return lines, nil
}

func pdfCanBreak(left, right rune) bool {
	if strings.ContainsRune("（([《〈「『【〔〖〘〚", left) || strings.ContainsRune("，。！？；：、）)]》〉」』】〕〗〙〛…—%", right) {
		return false
	}
	if (left == '…' && right == '…') || (left == '—' && right == '—') {
		return false
	}
	cjk := func(r rune) bool {
		return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
	}
	return unicode.IsSpace(left) || cjk(left) || cjk(right) || left == '-'
}
