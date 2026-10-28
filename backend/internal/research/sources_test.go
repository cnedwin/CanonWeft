package research

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResearchSourceLicenseAndIdentity(t *testing.T) {
	for _, input := range []string{
		`<license href="https://creativecommons.org/licenses/by-nc/4.0/"/>`,
		`<license href="https://creativecommons.org/licenses/by-nd/4.0/"/>`,
		`<copyright-statement href="https://creativecommons.org/licenses/by/4.0/"/>`,
		`<license href="https://creativecommons.org.evil.test/licenses/by/4.0/"/>`,
		`<license href="https://user@creativecommons.org/licenses/by/4.0/"/>`,
	} {
		if license, _ := permittedLicense(input); license != "" {
			t.Fatal("unusable license admitted", input)
		}
	}
	license, link := permittedLicense(`<license><license-p>Reusable <ext-link xlink:href="http://creativecommons.org/licenses/by/4.0/">CC-BY</ext-link></license-p></license>`)
	if license != "CC-BY-4.0" || link != "https://creativecommons.org/licenses/by/4.0/" {
		t.Fatal("reusable article license not recognized", license, link)
	}
	if ValidHit(EuropePMCSource, "en", Hit{PageID: 42, URL: "https://en.wikipedia.org/?curid=42"}) || ValidHit(NamesSource, "zh", Hit{PageID: 42, URL: "http://www.wikidata.org/wiki/Q42"}) || ValidSourceLanguage(EuropePMCSource, "zh") || ValidSource("arbitrary-url") {
		t.Fatal("source scope widened")
	}
}

func TestOpenResearchSourceAdapters(t *testing.T) {
	xml := `<article><front><article-meta><article-id pub-id-type="pmc">PMC42</article-id><article-id pub-id-type="doi">10.1234/example</article-id><title-group><article-title>Wound care</article-title></title-group><contrib-group><contrib><name><given-names>Ada</given-names><surname>Example</surname></name></contrib></contrib-group><pub-date><year>2024</year></pub-date><permissions><license xlink:href="https://creativecommons.org/licenses/by/4.0/"/></permissions><abstract><p>Research abstract.</p></abstract></article-meta></front><body><sec><title>Methods</title><p>Professional detail.</p><script>never execute</script></sec></body></article>`
	nameType := "Q11879590"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") == "" {
			t.Fatal("missing collector identity")
		}
		var body string
		switch r.URL.Host {
		case "www.ebi.ac.uk":
			if strings.HasSuffix(r.URL.Path, "/search") {
				if r.URL.Query().Get("pageSize") != "5" || !strings.Contains(r.URL.Query().Get("query"), "OPEN_ACCESS:y") {
					t.Fatal("unbounded or closed-access search")
				}
				body = `{"resultList":{"result":[{"pmcid":"PMC42","title":"Wound care","authorString":"Ada Example","firstPublicationDate":"2024-01-01","isOpenAccess":"Y","abstractText":"<p>Abstract.</p>"}]}}`
			} else {
				body = xml
			}
		case "www.wikidata.org":
			if r.URL.Query().Get("action") == "query" {
				if !strings.Contains(r.URL.Query().Get("srsearch"), "haswbstatement:P31=Q101352|P31=Q202444|P31=Q12308941|P31=Q11879590") {
					t.Fatal("name-only search filter missing")
				}
				body = `{"query":{"search":[{"title":"Q123456","titlesnippet":"<span>Elizabeth</span>","snippet":"Elizabeth · given name"}]}}`
			} else {
				body = `{"entities":{"Q123456":{"id":"Q123456","lastrevid":71,"labels":{"en":{"language":"en","value":"Elizabeth"}},"descriptions":{"en":{"value":"given name"}},"claims":{"P31":[{"rank":"normal","mainsnak":{"snaktype":"value","datavalue":{"value":{"id":"` + nameType + `"}}}}]}}}}`
			}
		default:
			t.Fatal("unexpected source host", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	catalog := NewCatalog(transport)
	pmc, err := catalog.Source(EuropePMCSource)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := pmc.Search(context.Background(), "en", "wound care")
	if err != nil || len(hits) != 1 || !ValidHit(EuropePMCSource, "en", hits[0]) {
		t.Fatal(hits, err)
	}
	doc, err := pmc.Collect(context.Background(), "en", 42)
	if err != nil || doc.License != "CC-BY-4.0" || doc.RevisionID != 0 || doc.Version == "" || doc.DOI != "10.1234/example" || !strings.Contains(doc.Attribution, "Ada") || strings.Contains(doc.Text, "never execute") || string(doc.Raw) != xml {
		t.Fatal("article provenance/extraction", doc.Provenance, err)
	}
	xml = strings.ReplaceAll(xml, "PMC42", "PMC43")
	if _, err = pmc.Collect(context.Background(), "en", 42); err == nil {
		t.Fatal("wrong article identity admitted")
	}
	names, err := catalog.Source(NamesSource)
	if err != nil {
		t.Fatal(err)
	}
	hits, err = names.Search(context.Background(), "en", "Elizabeth")
	if err != nil || len(hits) != 1 || hits[0].Title != "Elizabeth" || !ValidHit(NamesSource, "en", hits[0]) {
		t.Fatal(hits, err)
	}
	doc, err = names.Collect(context.Background(), "en", 123456)
	if err != nil || len(doc.Names) != 1 || doc.Names[0].Kind != "given_name" || doc.License != "CC0-1.0" || doc.RevisionID != 71 || doc.Names[0].Label != "Elizabeth" {
		t.Fatal("name source identity", doc.Provenance, err)
	}
	nameType = "Q5"
	if _, err = names.Collect(context.Background(), "en", 123456); err == nil {
		t.Fatal("person biography admitted as name material")
	}
}

func TestNewResearchSourcesLive(t *testing.T) {
	if os.Getenv("STUDIO_LIVE_RESEARCH") != "1" {
		t.Skip("opt-in free public API check")
	}
	catalog := NewCatalog(nil)
	for _, item := range []struct{ Source, Query string }{{EuropePMCSource, "wound healing"}, {NamesSource, "Elizabeth"}} {
		t.Run(item.Source, func(t *testing.T) {
			provider, err := catalog.Source(item.Source)
			if err != nil {
				t.Fatal(err)
			}
			hits, err := provider.Search(context.Background(), "en", item.Query)
			if err != nil || len(hits) == 0 {
				t.Fatal("free search", err)
			}
			for _, hit := range hits {
				doc, e := provider.Collect(context.Background(), "en", hit.PageID)
				if e != nil {
					continue
				}
				t.Logf("source=%s id=%d title=%s license=%s version=%s bytes=%d", doc.Provider, doc.PageID, doc.Title, doc.License, doc.Version, len(doc.Raw))
				return
			}
			t.Fatal("no reusable original could be collected from bounded search results")
		})
	}
}

func TestSourceBackoffDoesNotBlockOtherProviders(t *testing.T) {
	defer func() { paceMu.Lock(); delete(publicBackoff, "www.ebi.ac.uk"); paceMu.Unlock() }()
	calls := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status := 200
		if r.URL.Host == "www.ebi.ac.uk" {
			status = 503
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	if _, err := publicRead(context.Background(), publicClient(transport, "www.ebi.ac.uk"), "https://www.ebi.ac.uk/europepmc/webservices/rest/search", "application/json"); err == nil {
		t.Fatal("server failure ignored")
	}
	if _, err := publicRead(context.Background(), publicClient(transport, "www.ebi.ac.uk"), "https://www.ebi.ac.uk/europepmc/webservices/rest/search", "application/json"); err == nil || calls != 1 {
		t.Fatal("cooling source fetched again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := publicRead(ctx, publicClient(transport, "www.wikidata.org"), "https://www.wikidata.org/w/api.php", "application/json"); err != nil || calls != 2 {
		t.Fatal("one host's cooldown blocked another source", err)
	}
}
