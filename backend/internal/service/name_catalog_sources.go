package service

import (
	"context"
	"database/sql"
	"fmt"

	"novelstudio/internal/domain"
	"novelstudio/internal/research"
)

func validateNameImportTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, record NameMaterial) error {
	if !lowerHex32(record.SourceImportID) {
		return domain.ErrValidation
	}
	source, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, record.SourceImportID)
	if err != nil {
		return err
	}
	if source.Report.Research == nil || source.Report.Research.Provider != research.NamesSource {
		return domain.ErrValidation
	}
	for _, name := range source.Report.Research.Names {
		if name.ID == record.ID && name.Language == record.Language {
			return nil
		}
	}
	return domain.ErrValidation
}

// Original data has already passed the collector's identity/type/license gates.
// Preview preserves prior edits and does not write or adopt the name catalog.
func (s *NovelCharacters) PreviewNameSource(ctx context.Context, actor, bookID, importID string) (NameCatalog, error) {
	return s.PreviewNameSources(ctx, actor, bookID, []string{importID})
}

func (s *NovelCharacters) PreviewNameSources(ctx context.Context, actor, bookID string, importIDs []string) (NameCatalog, error) {
	if len(importIDs) == 0 || len(importIDs) > 100 {
		return NameCatalog{}, domain.ErrValidation
	}
	seenIDs := map[string]bool{}
	for _, id := range importIDs {
		if !lowerHex32(id) || seenIDs[id] {
			return NameCatalog{}, domain.ErrValidation
		}
		seenIDs[id] = true
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return NameCatalog{}, err
	}
	defer tx.Rollback()
	out, _, err := readNameCatalogTx(ctx, tx, a, book)
	if err != nil {
		return out, err
	}
	existing := map[string]bool{}
	for _, r := range out.Document.Records {
		existing[r.ID+"\x00"+r.Language] = true
	}
	for _, importID := range importIDs {
		source, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, importID)
		if err != nil {
			return out, err
		}
		p := source.Report.Research
		if p == nil || p.Provider != research.NamesSource || p.License != "CC0-1.0" || len(p.Names) == 0 || len(p.Names) > 5 {
			return out, domain.ErrValidation
		}
		for _, name := range p.Names {
			key := name.ID + "\x00" + name.Language
			if existing[key] {
				continue
			}
			out.Document.Records = append(out.Document.Records, NameMaterial{SourceImportID: source.ID, ID: name.ID, Label: name.Label, Original: name.Label, Language: name.Language, Kind: name.Kind, URL: p.URL, License: p.License, CapturedAt: p.CapturedAt, Culture: "unknown", Era: "unknown", ReviewState: "unverified", Evidence: fmt.Sprintf("%s\n条目：%s；修订：%d；原件SHA256：%s；%s\n语言标签不证明文化、年代、流行度或读音。", name.Description, name.ID, p.RevisionID, source.Hash, p.Version)})
			existing[key] = true
		}
	}
	return out, validateNameMaterials(out.Document)
}

type NameSourcePage struct {
	Items []ResearchSourceItem `json:"items"`
	Next  string               `json:"next"`
}

// Explicit pagination keeps large research runs discoverable without loading
// unrelated sources or silently restricting the author to the latest 100 rows.
func (s *NovelCharacters) NameSources(ctx context.Context, actor, bookID, after string) (NameSourcePage, error) {
	out := NameSourcePage{Items: []ResearchSourceItem{}}
	if after != "" && !lowerHex32(after) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_imports WHERE project_id=? AND branch_id=? AND actor_id=? AND JSON_UNQUOTE(JSON_EXTRACT(report,'$.research.provider'))='wikidata_names' AND (?='' OR id>?) ORDER BY id LIMIT 51`, book.ProjectID, book.BranchID, a.ID, after, after)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(ids) > 50 {
		ids = ids[:50]
		out.Next = ids[49]
	}
	for _, id := range ids {
		source, e := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id)
		if e != nil {
			return out, e
		}
		if source.Report.Research == nil {
			return out, domain.ErrNotReady
		}
		out.Items = append(out.Items, ResearchSourceItem{ID: id, Name: source.Name, Hash: source.Hash, Source: *source.Report.Research})
	}
	return out, nil
}
