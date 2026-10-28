package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type EuropePMC struct{ client *http.Client }

func NewEuropePMC(transport http.RoundTripper) *EuropePMC {
	return &EuropePMC{client: publicClient(transport, "www.ebi.ac.uk")}
}

func (p *EuropePMC) Search(ctx context.Context, language, query string) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if language != "en" || query == "" || utf8.RuneCountInString(query) > 240 {
		return nil, ErrSource
	}
	params := url.Values{"query": {"(" + query + ") AND OPEN_ACCESS:y AND IN_PMC:y"}, "format": {"json"}, "pageSize": {"5"}, "resultType": {"core"}}
	raw, err := publicRead(ctx, p.client, "https://www.ebi.ac.uk/europepmc/webservices/rest/search?"+params.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	var response struct {
		ResultList struct {
			Results []struct {
				PMCID      string `json:"pmcid"`
				Title      string `json:"title"`
				Authors    string `json:"authorString"`
				Published  string `json:"firstPublicationDate"`
				OpenAccess string `json:"isOpenAccess"`
				Abstract   string `json:"abstractText"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.ResultList.Results) > 5 {
		return nil, ErrSource
	}
	hits := []Hit{}
	seen := map[int64]bool{}
	for _, result := range response.ResultList.Results {
		id, e := strconv.ParseInt(strings.TrimPrefix(result.PMCID, "PMC"), 10, 64)
		if e != nil || id < 1 || !strings.HasPrefix(result.PMCID, "PMC") || result.OpenAccess != "Y" || seen[id] {
			continue
		}
		if result.Title == "" || utf8.RuneCountInString(result.Title) > 500 {
			return nil, ErrSource
		}
		abstract, e := plainHTML(result.Abstract)
		if e != nil {
			return nil, e
		}
		if len([]rune(abstract)) > 1200 {
			abstract = string([]rune(abstract)[:1200]) + "…"
		}
		hits = append(hits, Hit{PageID: id, Title: result.Title, Snippet: result.Published + " · " + result.Authors + "\n" + abstract, URL: fmt.Sprintf("https://europepmc.org/article/PMC/%d", id)})
		seen[id] = true
	}
	return hits, nil
}

type xmlText struct {
	Inner string `xml:",innerxml"`
}
type pmcArticle struct {
	XMLName xml.Name `xml:"article"`
	Front   struct {
		Meta struct {
			IDs []struct {
				Kind  string `xml:"pub-id-type,attr"`
				Value string `xml:",chardata"`
			} `xml:"article-id"`
			Title        xmlText `xml:"title-group>article-title"`
			Contributors []struct {
				Name struct {
					Given   string `xml:"given-names"`
					Surname string `xml:"surname"`
				} `xml:"name"`
			} `xml:"contrib-group>contrib"`
			Dates []struct {
				Year  string `xml:"year"`
				Month string `xml:"month"`
				Day   string `xml:"day"`
			} `xml:"pub-date"`
			Permissions xmlText `xml:"permissions"`
			Abstract    xmlText `xml:"abstract"`
		} `xml:"article-meta"`
	} `xml:"front"`
	Body xmlText `xml:"body"`
}

// Only explicit licenses allowing commercial reuse and adaptation enter the
// reusable full-text store. An open-access search flag is not a license grant.
func permittedLicense(input string) (string, string) {
	decoder := xml.NewDecoder(strings.NewReader("<permissions>" + input + "</permissions>"))
	depth, licenseDepth := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ""
		}
		if _, ok := token.(xml.EndElement); ok {
			if depth == licenseDepth {
				licenseDepth = 0
			}
			depth--
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		depth++
		if start.Name.Local == "license" {
			licenseDepth = depth
		}
		if licenseDepth == 0 {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local != "href" {
				continue
			}
			u, e := url.Parse(strings.TrimSpace(attr.Value))
			if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != "creativecommons.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				continue
			}
			path := strings.TrimSuffix(u.Path, "/")
			if path == "/publicdomain/zero/1.0" {
				return "CC0-1.0", "https://creativecommons.org/publicdomain/zero/1.0/"
			}
			for _, version := range []string{"2.0", "2.5", "3.0", "4.0"} {
				if path == "/licenses/by/"+version {
					return "CC-BY-" + version, "https://creativecommons.org/licenses/by/" + version + "/"
				}
			}
		}
	}
	return "", ""
}

func (p *EuropePMC) Collect(ctx context.Context, language string, id int64) (Document, error) {
	var doc Document
	if language != "en" || id < 1 {
		return doc, ErrSource
	}
	raw, err := publicRead(ctx, p.client, fmt.Sprintf("https://www.ebi.ac.uk/europepmc/webservices/rest/PMC%d/fullTextXML", id), "application/xml")
	if err != nil {
		return doc, err
	}
	var article pmcArticle
	if xml.Unmarshal(raw, &article) != nil || article.XMLName.Local != "article" {
		return doc, ErrSource
	}
	matched := false
	doi := ""
	for _, identifier := range article.Front.Meta.IDs {
		if identifier.Kind == "pmc" && strings.TrimPrefix(strings.TrimSpace(identifier.Value), "PMC") == strconv.FormatInt(id, 10) {
			matched = true
		}
		if identifier.Kind == "doi" {
			doi = strings.TrimSpace(identifier.Value)
		}
	}
	license, licenseURL := permittedLicense(article.Front.Meta.Permissions.Inner)
	if !matched || license == "" {
		return doc, fmt.Errorf("%w: article identity or reusable license missing", ErrSource)
	}
	title, err := plainHTML(article.Front.Meta.Title.Inner)
	if err != nil || title == "" || utf8.RuneCountInString(title) > 500 {
		return doc, ErrSource
	}
	text, complete, err := extractHTML(article.Front.Meta.Abstract.Inner + "\n" + article.Body.Inner)
	if err != nil || strings.TrimSpace(text) == "" || strings.TrimSpace(article.Body.Inner) == "" {
		return doc, ErrSource
	}
	authors := []string{}
	for _, author := range article.Front.Meta.Contributors {
		name := strings.TrimSpace(author.Name.Given + " " + author.Name.Surname)
		if name != "" {
			authors = append(authors, name)
		}
	}
	attribution := strings.Join(authors, ", ")
	if attribution == "" {
		attribution = "Article authors; consult original author list"
	}
	date := ""
	for _, d := range article.Front.Meta.Dates {
		if d.Year != "" {
			date = strings.Trim(strings.Join([]string{d.Year, d.Month, d.Day}, "-"), "-")
			break
		}
	}
	hash := sha256.Sum256(raw)
	doc = Document{Raw: raw, Text: text, TextComplete: complete, Provenance: Provenance{Provider: EuropePMCSource, Language: language, PageID: id, Title: title, URL: fmt.Sprintf("https://europepmc.org/article/PMC/%d", id), CapturedAt: time.Now().UTC().Format(time.RFC3339), License: license, LicenseURL: licenseURL, Attribution: attribution, Version: "sha256:" + hex.EncodeToString(hash[:]), PublishedAt: date, DOI: doi}}
	return doc, nil
}
