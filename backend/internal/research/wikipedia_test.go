package research

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// Opt-in, free public API check; normal tests are deterministic and offline.
func TestWikipediaLivePublicAPI(t *testing.T) {
	if os.Getenv("STUDIO_LIVE_RESEARCH") != "1" {
		t.Skip("explicit live public API check only")
	}
	w := NewWikipedia(nil)
	hits, err := w.Search(context.Background(), "en", "maritime customs Song dynasty")
	if err != nil || len(hits) == 0 {
		t.Fatalf("live search: %v", err)
	}
	doc, err := w.Collect(context.Background(), "en", hits[0].PageID)
	t.Logf("First result: %s, page %d", hits[0].Title, hits[0].PageID)
	if err != nil || len(doc.Raw) == 0 || doc.RevisionID < 1 {
		t.Fatalf("live collection: %v", err)
	}
	t.Logf("Live public API: %s, revision %d, raw %d bytes, extracted %d bytes, %s", doc.Title, doc.RevisionID, len(doc.Raw), len(doc.Text), doc.URL)
}

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWikipediaBoundedCollection(t *testing.T) {
	calls := 0
	f := NewWikipedia(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "zh.wikipedia.org" || r.URL.Path != "/w/api.php" || r.Header.Get("User-Agent") == "" {
			t.Fatal("unapproved endpoint", r.URL)
		}
		body := `{"query":{"search":[{"pageid":42,"title":"市舶司","snippet":"<span>宋代</span>港口机构<script>evil()</script>"}]}}`
		if r.URL.Query().Get("action") == "parse" {
			body = `{"parse":{"pageid":42,"revid":71,"title":"市舶司","text":"<div><p>港口机构。</p><script>evil()</script><p>办理商舶事务。<a href=\"https://museum.example/source\">史料</a></p></div>"}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	items, err := f.Search(context.Background(), "zh", "宋代 港口")
	if err != nil || len(items) != 1 || strings.Contains(items[0].Snippet, "evil") {
		t.Fatalf("search %+v %v", items, err)
	}
	source, err := f.Collect(context.Background(), "zh", 42)
	if err != nil || source.RevisionID != 71 || !strings.Contains(source.Text, "办理商舶事务") || strings.Contains(source.Text, "evil") || !strings.Contains(source.Text, "https://museum.example/source") || !strings.Contains(source.URL, "oldid=71") || len(source.Raw) == 0 {
		t.Fatalf("collect %+v %v", source, err)
	}
	if _, err = f.Search(context.Background(), "127.0.0.1", "x"); err == nil {
		t.Fatal("host injection accepted")
	}
	if _, err = f.Collect(context.Background(), "zh", -1); err == nil {
		t.Fatal("invalid page accepted")
	}
	if calls != 2 {
		t.Fatal("invalid input reached network", calls)
	}
}

func TestWikipediaRejectsRemoteFailures(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"maxlag"}}`, `{"parse":{"pageid":99,"revid":1,"title":"wrong","text":"text"}}`, `{"parse":{"pageid":42,"revid":0,"title":"no revision","text":"text"}}`, strings.Repeat("x", 2*1024*1024+1)} {
		f := NewWikipedia(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}))
		if _, err := f.Collect(context.Background(), "zh", 42); err == nil {
			t.Fatal("bad response accepted")
		}
	}
}

func TestWikipediaLongTextKeepsOriginalAndMarksPartial(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"parse": map[string]any{"pageid": 42, "revid": 71, "title": "长资料", "text": "<p>" + strings.Repeat("商舶🌊", 40000) + "</p>"}})
	w := NewWikipedia(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
	doc, err := w.Collect(context.Background(), "zh", 42)
	if err != nil || doc.TextComplete || doc.Text == "" || len(doc.Text) > 200*1024 || !utf8.ValidString(doc.Text) || len(doc.Raw) != len(raw) {
		t.Fatalf("partial source was discarded or corrupt: %v", err)
	}
}
