package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/research"
	"novelstudio/internal/retrieval"
)

type ResearchProvider = research.Provider

type ResearchSources struct {
	IP       *IP
	Provider ResearchProvider
}
type ResearchCollectInput struct {
	ReferenceURL string `json:"reference_url,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Language     string `json:"language"`
	PageID       int64  `json:"page_id"`
	Query        string `json:"query"`
	OperationID  string `json:"operation_id"`
}
type ResearchSourceItem struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Hash   string              `json:"hash"`
	Source research.Provenance `json:"source"`
}

func (s *ResearchSources) authorize(ctx context.Context, a domain.Actor, project, branch string) error {
	tx, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = branchTx(ctx, tx, project, branch)
	return err
}

func (s *ResearchSources) Search(ctx context.Context, a domain.Actor, project, branch, language, query string) ([]research.Hit, error) {
	return s.SearchIn(ctx, a, project, branch, "", language, query)
}

func (s *ResearchSources) source(source string) (ResearchProvider, error) {
	if !research.ValidSource(source) {
		return nil, domain.ErrValidation
	}
	provider := s.Provider
	if provider == nil {
		provider = research.NewCatalog(nil)
	}
	if catalog, ok := provider.(interface {
		Source(string) (research.Provider, error)
	}); ok {
		return catalog.Source(source)
	}
	if research.SourceKey(source) != research.WikipediaSource {
		return nil, domain.ErrNotReady
	}
	return provider, nil
}

func (s *ResearchSources) SearchIn(ctx context.Context, a domain.Actor, project, branch, source, language, query string) ([]research.Hit, error) {
	if err := s.authorize(ctx, a, project, branch); err != nil {
		return nil, err
	}
	if !validName(strings.TrimSpace(query), research.QueryLimit(source)) || !research.ValidSourceLanguage(source, language) {
		return nil, domain.ErrValidation
	}
	provider, err := s.source(source)
	if err != nil {
		return nil, err
	}
	items, err := provider.Search(ctx, language, query)
	if err != nil {
		return nil, fmt.Errorf("%w：%s", domain.ErrNotReady, research.ErrSource)
	}
	if len(items) > 5 {
		return nil, domain.ErrValidation
	}
	for _, hit := range items {
		if !research.ValidHit(source, language, hit) || !validName(hit.Title, 500) || len(hit.Snippet) > 16000 {
			return nil, domain.ErrValidation
		}
	}
	return items, nil
}

func (s *ResearchSources) Collect(ctx context.Context, a domain.Actor, project, branch string, in ResearchCollectInput) (SourceImport, error) {
	var empty SourceImport
	if !validName(in.OperationID, 120) || !validName(in.Query, research.QueryLimit(in.Provider)) || in.PageID < 1 || !research.ValidSourceLanguage(in.Provider, in.Language) {
		return empty, domain.ErrValidation
	}
	if in.Provider == research.OfficialSource {
		canonical, e := research.CanonicalOfficialURL(in.ReferenceURL)
		if e != nil || research.OfficialReferenceID(canonical) != in.PageID {
			return empty, domain.ErrValidation
		}
		in.ReferenceURL = canonical
	} else if in.ReferenceURL != "" {
		return empty, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return empty, err
	}
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		tx.Rollback()
		return empty, err
	}
	id, _, err := requestReceiptTx(ctx, tx, a, "source_imports", in.OperationID, false)
	if err == nil {
		prior, e := importTx(ctx, tx, a.ID, project, branch, id)
		tx.Rollback()
		if e != nil {
			return empty, e
		}
		p := prior.Report.Research
		if p == nil || p.Provider != research.SourceKey(in.Provider) || p.Language != in.Language || p.PageID != in.PageID || p.Query != in.Query {
			return empty, domain.ErrConflict
		}
		if in.Provider == research.OfficialSource && p.URL != in.ReferenceURL {
			return empty, domain.ErrConflict
		}
		return prior, nil
	}
	tx.Rollback()
	if err != sql.ErrNoRows {
		return empty, err
	}
	provider, err := s.source(in.Provider)
	if err != nil {
		return empty, err
	}
	var doc research.Document
	if in.Provider == research.OfficialSource {
		collector, ok := provider.(interface {
			CollectURL(context.Context, string, string) (research.Document, error)
		})
		if !ok {
			return empty, domain.ErrNotReady
		}
		doc, err = collector.CollectURL(ctx, in.Language, in.ReferenceURL)
	} else {
		doc, err = provider.Collect(ctx, in.Language, in.PageID)
	}
	if err != nil {
		return empty, fmt.Errorf("%w：%s", domain.ErrNotReady, research.ErrSource)
	}
	doc.Query = in.Query
	if doc.Provider != research.SourceKey(in.Provider) || doc.PageID != in.PageID || doc.Language != in.Language || len(doc.Raw) == 0 {
		return empty, domain.ErrConflict
	}
	if in.Provider == research.OfficialSource && (doc.URL != in.ReferenceURL || doc.Extent != "metadata_excerpt") {
		return empty, domain.ErrConflict
	}
	text := fmt.Sprintf("研究候选：%s\n来源：%s\n采集时间：%s\n查询：%s\n署名：%s\n许可：%s\n许可说明：%s\n\n这是百科来源的文字提取，不是独立事实核验。公式、图表及排版未完整保留，请对照原页；引用和第三方材料须另核许可。\n\n%s", doc.Title, doc.URL, doc.CapturedAt, in.Query, doc.Attribution, doc.License, doc.LicenseURL, doc.Text)
	report := retrieval.ImportReport{Research: &doc.Provenance, Parser: "wikipedia-parse-text-v1", Complete: doc.TextComplete, Extracted: 1, Text: text, Units: []retrieval.ImportUnit{{Locator: doc.URL, Kind: "web-text", Status: "extracted", Text: text}}}
	if !doc.TextComplete {
		report.Pending = 1
		report.Units = append(report.Units, retrieval.ImportUnit{Locator: doc.URL + "#text-limit", Kind: "web-text", Status: "pending", Reason: "条目过长，只提取前200KiB文字；完整原始数据已保留，请核对遗漏后明确接受部分内容。"})
		if doc.Provider == research.OfficialSource {
			report.Units[1].Locator = doc.URL
			report.Units[1].Reason = "只保存元数据、网页指纹和有限摘录的采集凭据，未保存HTML全文；请核对原网页和适用范围，明确接受部分材料后再采用。"
		}
	}
	name := fmt.Sprintf("wikipedia-%s-%d-%d.json", in.Language, doc.PageID, doc.RevisionID)
	if doc.Provider != research.WikipediaSource {
		ext := "json"
		if doc.Provider == research.EuropePMCSource {
			ext = "xml"
		}
		name = fmt.Sprintf("%s-%s-%d-%s.%s", doc.Provider, in.Language, doc.PageID, capabilityDigest(doc.Provenance)[:12], ext)
		text = fmt.Sprintf("研究候选：%s\n来源：%s\n来源类型：%s\n采集时间：%s\n出版时间：%s\nDOI：%s\n来源版本：%s\n查询：%s\n署名：%s\n许可：%s\n许可说明：%s\n\n收录不等于结论已核实；请核对年代、地域、适用条件、文章状态和原件的许可范围。不得把现实资料自动采用为故事正史。\n\n%s", doc.Title, doc.URL, doc.Provider, doc.CapturedAt, doc.PublishedAt, doc.DOI, doc.Version, in.Query, doc.Attribution, doc.License, doc.LicenseURL, doc.Text)
		report.Parser = doc.Provider + "-text-v1"
		report.Text = text
		report.Units[0].Text = text
	}
	// Preserve exact API bytes as the original; only the separately stored report
	// contains extracted, attributed text. No remote HTML is rendered or executed.
	return s.IP.storeSourceImport(ctx, a, project, branch, name, in.OperationID, doc.Raw, report, capabilityDigest(in))
}

func (s *ResearchSources) List(ctx context.Context, a domain.Actor, project, branch string) ([]ResearchSourceItem, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,filename,source_hash,JSON_EXTRACT(report,'$.research') FROM source_imports WHERE project_id=? AND branch_id=? AND actor_id=? AND JSON_UNQUOTE(JSON_EXTRACT(report,'$.research.provider')) IN ('wikipedia','europe_pmc','wikidata_names','official_reference') ORDER BY created_at DESC,id DESC LIMIT 100`, project, branch, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ResearchSourceItem{}
	for rows.Next() {
		var item ResearchSourceItem
		var raw []byte
		if err = rows.Scan(&item.ID, &item.Name, &item.Hash, &raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &item.Source) != nil {
			return nil, domain.ErrNotReady
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
