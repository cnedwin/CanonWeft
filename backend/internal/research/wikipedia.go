// Package research collects untrusted public reference material, never story canon.
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"novelstudio/internal/skills"
)

var ErrSource = errors.New("公开资料暂不可读取；可稍后重试或上传已取得的资料")

type Hit struct {
	PageID  int64  `json:"page_id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

type Provenance struct {
	Extent      string      `json:"capture_extent,omitempty"`
	Version     string      `json:"source_version,omitempty"`
	PublishedAt string      `json:"published_at,omitempty"`
	DOI         string      `json:"doi,omitempty"`
	Names       []NameDatum `json:"names,omitempty"`
	Provider    string      `json:"provider"`
	Language    string      `json:"language"`
	PageID      int64       `json:"page_id"`
	RevisionID  int64       `json:"revision_id"`
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	CapturedAt  string      `json:"captured_at"`
	License     string      `json:"license"`
	LicenseURL  string      `json:"license_url"`
	Attribution string      `json:"attribution"`
	Query       string      `json:"query"`
}

type Document struct {
	Provenance
	Raw          []byte
	Text         string
	TextComplete bool
}

type Wikipedia struct{ client *http.Client }

// A single in-process lane bounds requests across users and collector instances.
var networkLane = make(chan struct{}, 1)
var paceMu sync.Mutex
var nextRequest time.Time

func validLanguage(language string) bool {
	return language == "zh" || language == "en" || language == "ja"
}

func NewWikipedia(transport http.RoundTripper) *Wikipedia {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			language := strings.TrimSuffix(host, ".wikipedia.org")
			if err != nil || port != "443" || !validLanguage(language) || host != language+".wikipedia.org" {
				return nil, ErrSource
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 {
				return nil, ErrSource
			}
			for _, ip := range ips {
				if !skills.PublicAddress(ip) {
					return nil, ErrSource
				}
			}
			return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		transport = t
	}
	return &Wikipedia{client: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrSource }}}
}

func (w *Wikipedia) get(ctx context.Context, language string, values url.Values) ([]byte, error) {
	if !validLanguage(language) {
		return nil, ErrSource
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	select {
	case networkLane <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-networkLane }()
	paceMu.Lock()
	delay := time.Until(nextRequest)
	paceMu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	values.Set("format", "json")
	values.Set("formatversion", "2")
	values.Set("maxlag", "5")
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/w/api.php?"+values.Encode(), nil)
	if err != nil {
		return nil, ErrSource
	}
	r.Header.Set("User-Agent", "NovelStudio-Research/1.0 (local author reference collector)")
	r.Header.Set("Accept", "application/json")
	resp, err := w.client.Do(r)
	paceMu.Lock()
	nextRequest = time.Now().Add(time.Second)
	paceMu.Unlock()
	if err != nil {
		return nil, ErrSource
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		backoff := 60 * time.Second
		if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 60 && seconds < 86400 {
			backoff = time.Duration(seconds) * time.Second
		}
		paceMu.Lock()
		nextRequest = time.Now().Add(backoff)
		paceMu.Unlock()
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ErrSource
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, ErrSource
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Error) > 0 {
		return nil, ErrSource
	}
	return raw, nil
}

func (w *Wikipedia) Search(ctx context.Context, language, query string) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 240 {
		return nil, ErrSource
	}
	raw, err := w.get(ctx, language, url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {query}, "srnamespace": {"0"}, "srlimit": {"5"}, "srprop": {"snippet"}})
	if err != nil {
		return nil, err
	}
	var response struct {
		Query struct {
			Search []struct {
				PageID  int64  `json:"pageid"`
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Query.Search) > 5 {
		return nil, ErrSource
	}
	hits := []Hit{}
	for _, p := range response.Query.Search {
		if p.PageID < 1 || p.Title == "" {
			return nil, ErrSource
		}
		snippet, err := plainHTML(p.Snippet)
		if err != nil {
			return nil, err
		}
		hits = append(hits, Hit{PageID: p.PageID, Title: p.Title, Snippet: snippet, URL: fmt.Sprintf("https://%s.wikipedia.org/?curid=%d", language, p.PageID)})
	}
	return hits, nil
}

func (w *Wikipedia) Collect(ctx context.Context, language string, pageID int64) (Document, error) {
	var doc Document
	if pageID < 1 {
		return doc, ErrSource
	}
	raw, err := w.get(ctx, language, url.Values{"action": {"parse"}, "pageid": {strconv.FormatInt(pageID, 10)}, "prop": {"text|revid"}, "disableeditsection": {"1"}, "disabletoc": {"1"}})
	if err != nil {
		return doc, err
	}
	var response struct {
		Parse struct {
			PageID   int64  `json:"pageid"`
			Revision int64  `json:"revid"`
			Title    string `json:"title"`
			Text     string `json:"text"`
		} `json:"parse"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Parse.PageID != pageID || response.Parse.Revision < 1 || response.Parse.Title == "" {
		return doc, fmt.Errorf("%w: incomplete revision identity (page %d, revision %d)", ErrSource, response.Parse.PageID, response.Parse.Revision)
	}
	text, complete, err := extractHTML(response.Parse.Text)
	if err != nil || text == "" {
		return doc, fmt.Errorf("%w: text extraction (HTML bytes %d)", ErrSource, len(response.Parse.Text))
	}
	doc = Document{Raw: raw, Text: text, Provenance: Provenance{Provider: "wikipedia", Language: language, PageID: pageID, RevisionID: response.Parse.Revision, Title: response.Parse.Title, URL: fmt.Sprintf("https://%s.wikipedia.org/w/index.php?oldid=%d", language, response.Parse.Revision), CapturedAt: time.Now().UTC().Format(time.RFC3339), License: "CC BY-SA 4.0; exceptions and quoted material require separate review", LicenseURL: "https://creativecommons.org/licenses/by-sa/4.0/", Attribution: "Wikipedia contributors; contributor history is available from the source page"}}
	doc.TextComplete = complete
	return doc, nil
}

// No HTML is rendered or executed. Links remain visible for primary-source follow-up.
// A partial page is explicitly marked; callers retain its original response.
func plainHTML(input string) (string, error) {
	text, complete, err := extractHTML(input)
	if err != nil || !complete {
		return "", ErrSource
	}
	return text, nil
}

func extractHTML(input string) (string, bool, error) {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", false, ErrSource
	}
	var b strings.Builder
	limited := errors.New("text limit")
	appendText := func(text string) error {
		remaining := 200*1024 - b.Len()
		if len(text) <= remaining {
			b.WriteString(text)
			return nil
		}
		for remaining > 0 && !utf8.ValidString(text[:remaining]) {
			remaining--
		}
		b.WriteString(text[:remaining])
		return limited
	}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "iframe", "svg", "form":
				return nil
			}
		}
		if n.Type == html.TextNode {
			if err := appendText(n.Data); err != nil {
				return err
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "br":
				return appendText("\n")
			case "td", "th":
				return appendText("\t")
			case "a":
				for _, a := range n.Attr {
					if a.Key == "href" {
						u, e := url.Parse(a.Val)
						if e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil {
							if err := appendText(" (" + u.String() + ")"); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	}
	err = walk(root)
	if err != nil && !errors.Is(err, limited) {
		return "", false, ErrSource
	}
	lines := []string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), err == nil, nil
}
