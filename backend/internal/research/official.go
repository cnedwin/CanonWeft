package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

type Official struct{ transport http.RoundTripper }

func NewOfficial(transport http.RoundTripper) *Official { return &Official{transport: transport} }

func (p *Official) Search(_ context.Context, language, query string) ([]Hit, error) {
	if language != "zh" && language != "en" {
		return nil, ErrSource
	}
	canonical, err := CanonicalOfficialURL(query)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(canonical)
	return []Hit{{PageID: OfficialReferenceID(canonical), Title: "官方原文 · " + u.Host, Snippet: "仅定位批准范围内的链接；采集时检查robots，并保存网页指纹与有限摘录。", URL: canonical}}, nil
}

func (*Official) Collect(context.Context, string, int64) (Document, error) {
	return Document{}, ErrSource
}

func (p *Official) CollectURL(ctx context.Context, language, target string) (Document, error) {
	var doc Document
	canonical, err := CanonicalOfficialURL(target)
	if err != nil || (language != "zh" && language != "en") {
		return doc, ErrSource
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	u, _ := url.Parse(canonical)
	client := publicClient(p.transport, u.Host)
	robots, headers, err := publicResponse(ctx, client, "https://"+u.Host+"/robots.txt", "text/plain")
	if err != nil {
		var status publicStatusError
		if !errors.As(err, &status) || status != 404 {
			return doc, fmt.Errorf("%w: robots request: %v", ErrSource, err)
		}
		robots = nil
	} else {
		media, _, _ := mime.ParseMediaType(headers.Get("Content-Type"))
		if media == "" {
			media, _, _ = mime.ParseMediaType(http.DetectContentType(robots))
		}
		if media != "text/plain" || !utf8.Valid(robots) {
			return doc, fmt.Errorf("%w: robots format", ErrSource)
		}
	}
	allowed, delay, err := robotsPermission(string(robots), u.EscapedPath())
	if err != nil || !allowed {
		return doc, fmt.Errorf("%w: robots policy", ErrSource)
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return doc, ctx.Err()
		}
	}
	raw, headers, err := publicResponse(ctx, client, canonical, "text/html,application/xhtml+xml")
	if err != nil {
		return doc, fmt.Errorf("%w: article request: %v", ErrSource, err)
	}
	media, _, _ := mime.ParseMediaType(headers.Get("Content-Type"))
	if media != "text/html" && media != "application/xhtml+xml" {
		return doc, fmt.Errorf("%w: article format", ErrSource)
	}
	reader, err := charset.NewReader(bytes.NewReader(raw), headers.Get("Content-Type"))
	if err != nil {
		return doc, ErrSource
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, 4*1024*1024+1))
	if err != nil || len(decoded) > 4*1024*1024 {
		return doc, ErrSource
	}
	title, date, quote, err := officialExcerpt(decoded)
	if err != nil {
		return doc, fmt.Errorf("%w: article body not recognized: %v", ErrSource, err)
	}
	bodyHash := sha256.Sum256(raw)
	robotsHash := sha256.Sum256(robots)
	captured := time.Now().UTC().Format(time.RFC3339)
	// Unknown whole-page rights never become a reusable HTML archive. This is
	// explicitly an excerpt receipt, retaining the full response's fingerprint.
	receipt := struct {
		Schema      string `json:"schema"`
		URL         string `json:"url"`
		Title       string `json:"title"`
		CapturedAt  string `json:"captured_at"`
		PublishedAt string `json:"published_at,omitempty"`
		PageHash    string `json:"page_sha256"`
		RobotsHash  string `json:"robots_sha256"`
		Excerpt     string `json:"excerpt"`
		Extent      string `json:"capture_extent"`
	}{"official-reference-receipt-v1", canonical, title, captured, date, hex.EncodeToString(bodyHash[:]), hex.EncodeToString(robotsHash[:]), quote, "metadata_excerpt"}
	stored, err := json.Marshal(receipt)
	if err != nil {
		return doc, err
	}
	doc = Document{Raw: stored, Text: "网页有限摘录（最多160字，非全文）：\n" + quote + "\n\n请打开官方链接核对上下文、原始发布者、时点及适用范围；网页标示日期不等于法律生效日期。当前保存文件为采集凭据，并非原始HTML全文。", TextComplete: false, Provenance: Provenance{Provider: OfficialSource, Language: language, PageID: OfficialReferenceID(canonical), Title: title, URL: canonical, CapturedAt: captured, PublishedAt: date, Version: "sha256:" + receipt.PageHash, Extent: "metadata_excerpt", License: "unspecified; reference excerpt, whole-page reuse not granted", Attribution: u.Host + "; consult original attribution and rights notice"}}
	return doc, nil
}

func officialExcerpt(raw []byte) (string, string, string, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", ErrSource
	}
	var content *html.Node
	title, date := "", ""
	contentScore := 0
	foundHeading := false
	containers := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[strings.ToLower(a.Key)] = a.Val
			}
			id, class := strings.ToLower(attrs["id"]), strings.ToLower(attrs["class"])
			if len(containers) < 12 && (id != "" || attrs["role"] == "main") {
				marker := []rune(n.Data + "#" + id + "[role=" + attrs["role"] + "]")
				if len(marker) > 160 {
					marker = marker[:160]
				}
				containers = append(containers, string(marker))
			}
			score := 0
			if n.Data == "main" || id == "main-content" || id == "main-col" {
				score = 1
			}
			if n.Data == "article" {
				score = 2
			}
			if id == "ucap-content" || id == "zoom" || id == "article-content" || id == "block-system-main" || strings.Contains(class, "trs_editor") || strings.Contains(class, "article-content") {
				score = 3
			}
			if score > contentScore {
				content, contentScore = n, score
			}
			if (title == "" && n.Data == "title") || (!foundHeading && n.Data == "h1") {
				var b bytes.Buffer
				if html.Render(&b, n) == nil {
					title, _ = plainHTML(b.String())
					if n.Data == "h1" {
						foundHeading = true
					}
				}
			}
			if n.Data == "meta" && date == "" {
				key := strings.ToLower(attrs["name"] + attrs["property"])
				if key == "pubdate" || key == "publishdate" || key == "article:published_time" || key == "pubtime" {
					date = strings.TrimSpace(attrs["content"])
				}
			}
			if n.Data == "script" || n.Data == "style" || n.Data == "nav" || n.Data == "footer" || n.Data == "header" || n.Data == "aside" {
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if content == nil || strings.TrimSpace(title) == "" {
		diagnosticTitle := []rune(title)
		if len(diagnosticTitle) > 80 {
			diagnosticTitle = diagnosticTitle[:80]
		}
		return "", "", "", fmt.Errorf("%w: container=%t title=%q markers=%q", ErrSource, content != nil, string(diagnosticTitle), containers)
	}
	var body bytes.Buffer
	var prune func(*html.Node)
	prune = func(n *html.Node) {
		for child := n.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.ElementNode && (child.Data == "nav" || child.Data == "header" || child.Data == "footer" || child.Data == "aside" || child.Data == "form") {
				n.RemoveChild(child)
			} else {
				prune(child)
			}
			child = next
		}
	}
	prune(content)
	if html.Render(&body, content) != nil {
		return "", "", "", ErrSource
	}
	text, _, err := extractHTML(body.String())
	if err != nil || strings.TrimSpace(text) == "" {
		return "", "", "", ErrSource
	}
	if len([]rune(title)) > 500 {
		title = string([]rune(title)[:500])
	}
	if len(date) > 100 {
		date = ""
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 160 {
		text = string([]rune(text)[:160])
	}
	return title, date, text, nil
}
