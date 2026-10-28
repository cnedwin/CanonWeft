package research

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type Provider interface {
	Search(context.Context, string, string) ([]Hit, error)
	Collect(context.Context, string, int64) (Document, error)
}

const WikipediaSource = "wikipedia"
const EuropePMCSource = "europe_pmc"
const NamesSource = "wikidata_names"

func QueryLimit(source string) int {
	if source == OfficialSource {
		return 2000
	}
	return 240
}

func SourceKey(source string) string {
	if source == "" {
		return WikipediaSource
	}
	return source
}

func ValidSource(source string) bool {
	switch SourceKey(source) {
	case WikipediaSource, EuropePMCSource, NamesSource, OfficialSource:
		return true
	}
	return false
}

func ValidSourceLanguage(source, language string) bool {
	switch SourceKey(source) {
	case WikipediaSource:
		return validLanguage(language)
	case EuropePMCSource:
		return language == "en"
	case NamesSource:
		return nameLanguage(language)
	case OfficialSource:
		return language == "zh" || language == "en"
	}
	return false
}

type Catalog struct{ sources map[string]Provider }

func NewCatalog(transport http.RoundTripper) *Catalog {
	return &Catalog{sources: map[string]Provider{
		WikipediaSource: NewWikipedia(transport), EuropePMCSource: NewEuropePMC(transport), NamesSource: NewWikidataNames(transport), OfficialSource: NewOfficial(transport),
	}}
}

func (c *Catalog) Source(source string) (Provider, error) {
	p := c.sources[SourceKey(source)]
	if p == nil {
		return nil, ErrSource
	}
	return p, nil
}

func (c *Catalog) Search(ctx context.Context, language, query string) ([]Hit, error) {
	return c.sources[WikipediaSource].Search(ctx, language, query)
}
func (c *Catalog) Collect(ctx context.Context, language string, id int64) (Document, error) {
	return c.sources[WikipediaSource].Collect(ctx, language, id)
}

func ValidHit(source, language string, hit Hit) bool {
	u, err := url.Parse(hit.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || hit.PageID < 1 {
		return false
	}
	switch SourceKey(source) {
	case WikipediaSource:
		return validLanguage(language) && u.Host == language+".wikipedia.org"
	case EuropePMCSource:
		return language == "en" && hit.URL == "https://europepmc.org/article/PMC/"+strconv.FormatInt(hit.PageID, 10)
	case NamesSource:
		return nameLanguage(language) && hit.URL == "https://www.wikidata.org/wiki/Q"+strconv.FormatInt(hit.PageID, 10)
	case OfficialSource:
		canonical, err := CanonicalOfficialURL(hit.URL)
		return err == nil && canonical == hit.URL && ValidSourceLanguage(source, language) && OfficialReferenceID(canonical) == hit.PageID
	}
	return false
}
