package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type NameDatum struct {
	ID          string `json:"entity_id"`
	Label       string `json:"label"`
	Language    string `json:"label_language"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type WikidataNames struct{ client *http.Client }

func NewWikidataNames(transport http.RoundTripper) *WikidataNames {
	return &WikidataNames{client: publicClient(transport, "www.wikidata.org")}
}
func nameLanguage(language string) bool {
	return language == "zh" || language == "en" || language == "ja" || language == "fr" || language == "de"
}
func (p *WikidataNames) get(ctx context.Context, params url.Values) ([]byte, error) {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("maxlag", "5")
	raw, err := publicRead(ctx, p.client, "https://www.wikidata.org/w/api.php?"+params.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, ErrSource
	}
	if len(envelope.Error) > 0 {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(envelope.Error, &failure) == nil && len(failure.Code) <= 80 {
			if failure.Code == "maxlag" {
				paceMu.Lock()
				publicBackoff["www.wikidata.org"] = time.Now().Add(time.Minute)
				paceMu.Unlock()
			}
			return nil, fmt.Errorf("%w: Wikidata %s", ErrSource, failure.Code)
		}
		return nil, ErrSource
	}
	return raw, nil
}
func (p *WikidataNames) Search(ctx context.Context, language, query string) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if !nameLanguage(language) || query == "" || utf8.RuneCountInString(query) > 240 {
		return nil, ErrSource
	}
	raw, err := p.get(ctx, url.Values{"action": {"query"}, "list": {"search"}, "srnamespace": {"0"}, "srlimit": {"5"}, "srprop": {"snippet|titlesnippet"}, "uselang": {language}, "srsearch": {"(" + query + ") haswbstatement:P31=Q101352|P31=Q202444|P31=Q12308941|P31=Q11879590"}})
	if err != nil {
		return nil, err
	}
	var response struct {
		Query struct {
			Search []struct {
				Title        string `json:"title"`
				TitleSnippet string `json:"titlesnippet"`
				Snippet      string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Query.Search) > 5 {
		return nil, ErrSource
	}
	hits := []Hit{}
	for _, result := range response.Query.Search {
		id, e := strconv.ParseInt(strings.TrimPrefix(result.Title, "Q"), 10, 64)
		if e != nil || id < 1 || result.Title != "Q"+strconv.FormatInt(id, 10) {
			return nil, ErrSource
		}
		snippet, e := plainHTML(result.Snippet)
		if e != nil {
			return nil, e
		}
		title, e := plainHTML(result.TitleSnippet)
		if e != nil {
			return nil, e
		}
		if title == "" || utf8.RuneCountInString(title) > 500 {
			title = result.Title
		}
		hits = append(hits, Hit{PageID: id, Title: title, Snippet: snippet, URL: "https://www.wikidata.org/wiki/" + result.Title})
	}
	return hits, nil
}
func (p *WikidataNames) Collect(ctx context.Context, language string, id int64) (Document, error) {
	var doc Document
	if !nameLanguage(language) || id < 1 {
		return doc, ErrSource
	}
	entityID := "Q" + strconv.FormatInt(id, 10)
	raw, err := p.get(ctx, url.Values{"action": {"wbgetentities"}, "ids": {entityID}, "props": {"info|labels|descriptions|claims"}, "languages": {language}})
	if err != nil {
		return doc, err
	}
	var response struct {
		Entities map[string]struct {
			ID       string `json:"id"`
			Revision int64  `json:"lastrevid"`
			Labels   map[string]struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			} `json:"labels"`
			Descriptions map[string]struct {
				Value string `json:"value"`
			} `json:"descriptions"`
			Claims map[string][]struct {
				Rank string `json:"rank"`
				Main struct {
					SnakType string `json:"snaktype"`
					Data     struct {
						Value struct {
							ID string `json:"id"`
						} `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return doc, ErrSource
	}
	entity, ok := response.Entities[entityID]
	if !ok || entity.ID != entityID || entity.Revision < 1 {
		return doc, ErrSource
	}
	kind := ""
	for _, claim := range entity.Claims["P31"] {
		if claim.Rank == "deprecated" || claim.Main.SnakType != "value" {
			continue
		}
		switch claim.Main.Data.Value.ID {
		case "Q101352":
			kind = "surname"
		case "Q202444", "Q12308941", "Q11879590":
			if kind == "" {
				kind = "given_name"
			}
		}
	}
	label := entity.Labels[language]
	if kind == "" || label.Language != language || strings.TrimSpace(label.Value) == "" || utf8.RuneCountInString(label.Value) > 120 {
		return doc, fmt.Errorf("%w: name type or requested label missing", ErrSource)
	}
	description := entity.Descriptions[language].Value
	if len([]rune(description)) > 1200 {
		description = string([]rune(description)[:1200])
	}
	name := NameDatum{ID: entityID, Label: label.Value, Language: language, Kind: kind, Description: description}
	hash := sha256.Sum256(raw)
	doc = Document{Raw: raw, Text: fmt.Sprintf("%s (%s)\n类型：%s\n%s\n标签语言不代表文化或年代；条目分类和读音仍需作者核对。", name.Label, entityID, kind, description), TextComplete: true, Provenance: Provenance{Provider: NamesSource, Language: language, PageID: id, RevisionID: entity.Revision, Title: label.Value, URL: fmt.Sprintf("https://www.wikidata.org/w/index.php?oldid=%d", entity.Revision), CapturedAt: time.Now().UTC().Format(time.RFC3339), License: "CC0-1.0", LicenseURL: "https://creativecommons.org/publicdomain/zero/1.0/", Attribution: "Wikidata contributors; exact entity revision retained", Version: "sha256:" + hex.EncodeToString(hash[:]), Names: []NameDatum{name}}}
	return doc, nil
}
