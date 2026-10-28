package research

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOfficialURLAndRobotsBoundaries(t *testing.T) {
	for _, target := range []string{"http://www.gov.cn/zhengce/a.htm", "https://www.gov.cn.evil.test/zhengce/a.htm", "https://user@www.gov.cn/zhengce/a.htm", "https://www.gov.cn:443/zhengce/a.htm", "https://www.gov.cn/zhengce/a.htm?token=private", "https://www.gov.cn/zhengce/%2e%2e/admin", "https://127.0.0.1/zhengce/a.htm", "https://www.gov.cn/other/a.htm", "https://www.gov.cn/zhengce/a%2fb.htm"} {
		if _, err := CanonicalOfficialURL(target); err == nil {
			t.Fatal("unapproved URL accepted", target)
		}
	}
	canonical, err := CanonicalOfficialURL("https://www.gov.cn/zhengce/a.htm#section")
	if err != nil || canonical != "https://www.gov.cn/zhengce/a.htm" || OfficialReferenceID(canonical) > (1<<53)-1 {
		t.Fatal(canonical, err)
	}
	for _, item := range []struct {
		text, path string
		allowed    bool
	}{
		{"User-agent: *\nDisallow: /\n", "/zhengce/a.htm", false},
		{"User-agent: *\nDisallow: /\nAllow: /zhengce/\n", "/zhengce/a.htm", true},
		{"User-agent: *\nDisallow: /zhengce/*private$\n", "/zhengce/x-private", false},
		{"User-agent: *\nDisallow: /\n\nUser-agent: NovelStudio-Research\nAllow: /zhengce/\n", "/zhengce/a.htm", true},
		{"User-agent: *\nAllow: /zhengce/\nDisallow: /zhengce/\n", "/zhengce/a.htm", true},
	} {
		allowed, _, err := robotsPermission(item.text, item.path)
		if err != nil || allowed != item.allowed {
			t.Fatal(item, allowed, err)
		}
	}
	for _, delay := range []string{"NaN", "Inf", "-1", "11"} {
		if _, _, err := robotsPermission("User-agent: *\nCrawl-delay: "+delay, "/zhengce/a.htm"); err == nil {
			t.Fatal("unbounded or invalid delay accepted", delay)
		}
	}
}

func TestOfficialCaptureReceiptAndDenial(t *testing.T) {
	const target = "https://www.gov.cn/zhengce/test.htm"
	page := `<html><head><meta charset="utf-8"><title>网站题名</title><meta name="PubDate" content="2024-01-01"></head><body><nav>NAV_CANARY</nav><main><aside>ASIDE_CANARY</aside><h1>制度原文</h1><div id="UCAP-CONTENT"><p>` + strings.Repeat("核对原始制度年代。", 30) + `</p><script>SCRIPT_CANARY</script><p>FULLTEXT_CANARY</p></div></main></body></html>`
	denied, redirect := false, false
	calls := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, body, kind := 200, page, "text/html; charset=utf-8"
		if r.URL.Path == "/robots.txt" {
			body = "User-agent: *\nAllow: /zhengce/\n"
			kind = "text/plain"
			if denied {
				body = "User-agent: *\nDisallow: /\n"
			}
		}
		if redirect && r.URL.Path != "/robots.txt" {
			status = 302
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{kind}, "Location": []string{"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	provider := NewOfficial(transport)
	hits, err := provider.Search(context.Background(), "zh", target)
	if err != nil || len(hits) != 1 || calls != 0 || !ValidHit(OfficialSource, "zh", hits[0]) {
		t.Fatal("URL discovery fetched or lost scope", err)
	}
	doc, err := provider.CollectURL(context.Background(), "zh", target)
	if err != nil || doc.TextComplete || doc.Extent != "metadata_excerpt" || doc.Title != "制度原文" || doc.PublishedAt != "2024-01-01" || !strings.HasPrefix(doc.Version, "sha256:") || calls != 2 {
		t.Fatal("official capture", doc.Provenance, err)
	}
	var receipt struct {
		Excerpt  string `json:"excerpt"`
		PageHash string `json:"page_sha256"`
		Extent   string `json:"capture_extent"`
	}
	if json.Unmarshal(doc.Raw, &receipt) != nil || len([]rune(receipt.Excerpt)) > 160 || receipt.PageHash == "" || receipt.Extent != "metadata_excerpt" || strings.Contains(string(doc.Raw), "FULLTEXT_CANARY") || strings.Contains(doc.Text, "SCRIPT_CANARY") || strings.Contains(doc.Text, "NAV_CANARY") || strings.Contains(doc.Text, "ASIDE_CANARY") {
		t.Fatal("receipt retained forbidden/full content")
	}
	denied = true
	if _, err = provider.CollectURL(context.Background(), "zh", target); err == nil || calls != 3 {
		t.Fatal("robots denial fetched article", err, calls)
	}
	denied, redirect = false, true
	if _, err = provider.CollectURL(context.Background(), "zh", target); err == nil || calls != 5 {
		t.Fatal("redirect followed", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := calls
	if _, err = provider.CollectURL(ctx, "zh", target); err == nil || calls != before {
		t.Fatal("cancelled request fetched", err)
	}
	if _, delay, err := robotsPermission("User-agent: *\nCrawl-delay: 1\n", "/zhengce/test.htm"); err != nil || delay != time.Second {
		t.Fatal("crawl delay", delay, err)
	}
}

func TestOfficialLegacyBodyMarkers(t *testing.T) {
	page := `<html><head><title>Archive transcription</title></head><body><header id="title-bar">Navigation</header><div id="main-col"><p id="print-logo">PRINT_LOGO_CANARY</p><section id="block-system-main"><p>Verified transcription container.</p></section></div><aside>SIDEBAR_CANARY</aside></body></html>`
	title, _, excerpt, err := officialExcerpt([]byte(page))
	if err != nil || title != "Archive transcription" || excerpt != "Verified transcription container." {
		t.Fatal("legacy official article container", title, excerpt, err)
	}
}
