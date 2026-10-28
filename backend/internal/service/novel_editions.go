package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
)

type NovelEditionInput struct {
	Name        string           `json:"name"`
	OperationID string           `json:"operation_id"`
	Accept      bool             `json:"accept"`
	Export      NovelExportInput `json:"export"`
}
type NovelEditionMetadata struct {
	Author      string `json:"author"`
	Language    string `json:"language"`
	Description string `json:"description"`
}
type NovelEditionItem struct {
	ID        string `json:"id"`
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Included  int    `json:"included"`
	Missing   int    `json:"missing"`
	Published bool   `json:"published"`
	ReleaseID string `json:"release_id"`
}
type NovelEditionPage struct {
	Items []NovelEditionItem `json:"items"`
	Next  string             `json:"next"`
}
type novelEditionDocument struct {
	Schema  int                    `json:"schema"`
	Info    retrieval.NovelEdition `json:"info"`
	Preview NovelExportPreview     `json:"preview"`
}

func (s *NovelExport) editionAuthority(ctx context.Context, actor, bookID string) (domain.Actor, NovelBook, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return a, book, err
	}
	defer tx.Rollback()
	p, err := projectTx(ctx, tx, a, book.ProjectID)
	if err == nil && ((a.Role != "owner" && a.Role != "publisher") || (p.Role != "owner" && p.Role != "publisher")) {
		err = domain.ErrForbidden
	}
	return a, book, err
}

// SaveEdition is an explicit author action. The immutable archive is adopted
// through the existing Release path, including its normal publication rights.
func (s *NovelExport) SaveEdition(ctx context.Context, actor, bookID string, in NovelEditionInput) (NovelEditionItem, error) {
	var zero NovelEditionItem
	in.Name = strings.TrimSpace(in.Name)
	if !in.Accept || !validName(in.Name, 80) || strings.ContainsAny(in.Name, "/\\\x00") || !validName(in.OperationID, 120) || in.Export.Source != "published" || in.Export.EditionID != "" {
		return zero, domain.ErrValidation
	}
	a, book, err := s.editionAuthority(ctx, actor, bookID)
	if err != nil {
		return zero, err
	}
	request := capabilityDigest(struct {
		Book  string
		Input NovelEditionInput
	}{bookID, in})
	op := "novel-edition-" + digest(bookID + "\x00" + in.OperationID)[:48]
	tx, _, _, err := s.IP.begin(ctx, a, book.ProjectID, true)
	if err != nil {
		return zero, err
	}
	id, _, priorErr := requestReceiptTx(ctx, tx, a, "source_imports", op, false)
	if priorErr == nil {
		prior, e := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id)
		tx.Rollback()
		if e != nil {
			return zero, e
		}
		if prior.Report.NovelEdition == nil || prior.Report.NovelEdition.BookID != bookID || prior.Report.NovelEdition.RequestHash != request {
			return zero, domain.ErrConflict
		}
		return s.ConfirmEdition(ctx, actor, bookID, prior.ID, prior.Hash, true)
	}
	tx.Rollback()
	if priorErr != sql.ErrNoRows {
		return zero, priorErr
	}
	p, manuscript, err := s.snapshot(ctx, actor, bookID, in.Export)
	if err != nil {
		return zero, err
	}
	if err = checkNovelExport(p, in.Export); err != nil {
		return zero, err
	}
	info := retrieval.NovelEdition{BookID: bookID, Name: in.Name, Title: manuscript.Title, Author: manuscript.Author, PreviewHash: p.Hash, RequestHash: request, Included: p.Included, Missing: p.Missing}
	metadata, err := json.Marshal(novelEditionDocument{Schema: 1, Info: info, Preview: p})
	if err != nil {
		return zero, err
	}
	data, err := manuscript.Archive(metadata)
	if err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	summary, _ := json.Marshal(info)
	report := retrieval.ImportReport{Parser: "novel-edition-v1", Complete: true, NovelEdition: &info, Text: string(summary), Units: []retrieval.ImportUnit{}}
	imported, err := s.IP.storeSourceImport(ctx, a, book.ProjectID, book.BranchID, "成书版本 · "+in.Name+".nsedition", op, data, report, request)
	if err != nil {
		return zero, err
	}
	return s.ConfirmEdition(ctx, actor, bookID, imported.ID, imported.Hash, true)
}

// A save interrupted after archiving can resume without reconstructing a new
// edition from changed chapters. The expected hash is the saved archive itself.
func (s *NovelExport) ConfirmEdition(ctx context.Context, actor, bookID, id, hash string, accept bool) (NovelEditionItem, error) {
	var zero NovelEditionItem
	if !accept || !lowerHex32(id) || len(hash) != 64 {
		return zero, domain.ErrValidation
	}
	a, book, err := s.editionAuthority(ctx, actor, bookID)
	if err != nil {
		return zero, err
	}
	tx, _, _, err := s.IP.begin(ctx, a, book.ProjectID, true)
	if err != nil {
		return zero, err
	}
	item, source, err := editionSourceTx(ctx, tx, a, book, id)
	if err != nil {
		tx.Rollback()
		return zero, err
	}
	if item.Hash != hash {
		tx.Rollback()
		return zero, domain.ErrConflict
	}
	if _, _, err = s.readEditionArchive(ctx, book, source); err != nil {
		tx.Rollback()
		return zero, err
	}
	if item.Published {
		tx.Rollback()
		return item, nil
	}
	// Unadopted archives remain private to their original author.
	if _, err = importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id); err != nil {
		tx.Rollback()
		return zero, err
	}
	branch, err := branchTx(ctx, tx, book.ProjectID, book.BranchID)
	tx.Rollback()
	if err != nil {
		return zero, err
	}
	policy := &domain.KnowledgePolicy{Space: "working", Visibility: "author"}
	r, err := s.IP.ImportCandidate(ctx, a, book.ProjectID, book.BranchID, id, ImportCandidateInput{Text: source.Report.Text, ExpectedIPHead: branch.Head, OperationID: "edition-release-" + id + "-" + digest(branch.Head)[:12], Policy: policy})
	if err != nil {
		return zero, err
	}
	if r.Status == "PREPARED" {
		r, err = s.IP.AdoptRelease(ctx, a, book.ProjectID, r.ID)
		if err != nil {
			return zero, err
		}
	}
	if r.Status != "PUBLISHED" {
		r, err = s.IP.PublishRelease(ctx, a, book.ProjectID, r.ID)
		if err != nil {
			return zero, err
		}
	}
	item.Published, item.ReleaseID = true, r.ID
	return item, nil
}

func editionSourceTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (NovelEditionItem, SourceImport, error) {
	var item NovelEditionItem
	var src SourceImport
	var report []byte
	err := tx.QueryRowContext(ctx, `SELECT i.id,i.filename,i.source_hash,i.source_size,i.report,DATE_FORMAT(i.created_at,'%Y-%m-%dT%H:%i:%sZ'),COALESCE((SELECT MIN(c.release_id) FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED'),'') FROM source_imports i WHERE i.id=? AND i.project_id=? AND i.branch_id=? AND (i.actor_id=? OR EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED'))`, id, book.ProjectID, book.BranchID, a.ID).Scan(&src.ID, &src.Name, &src.Hash, &src.Size, &report, &item.CreatedAt, &item.ReleaseID)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	if err != nil {
		return item, src, err
	}
	if err = rejectExpiredRuntime(report, "source_imports.report"); err != nil {
		return item, src, err
	}
	if err = json.Unmarshal(report, &src.Report); err != nil {
		return item, src, err
	}
	info := src.Report.NovelEdition
	if src.Report.Parser != "novel-edition-v1" || !src.Report.Complete || info == nil || info.BookID != book.ID {
		return item, src, domain.ErrForbidden
	}
	if err = requireRuntimeObjectTx(ctx, tx, book.ProjectID, "source_imports", id, "source_hash", src.Hash); err != nil {
		return item, src, err
	}
	item.ID, item.Hash, item.Name, item.Title, item.Author = src.ID, src.Hash, info.Name, info.Title, info.Author
	item.Included, item.Missing, item.Published = info.Included, info.Missing, item.ReleaseID != ""
	return item, src, nil
}

func (s *NovelExport) readEditionArchive(ctx context.Context, book NovelBook, source SourceImport) (NovelExportPreview, novel.ExportBook, error) {
	var zero NovelExportPreview
	var empty novel.ExportBook
	if source.Size <= 0 || source.Size > novel.MaxExportArchiveBytes {
		return zero, empty, domain.ErrNotReady
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, book.ProjectID, source.Hash)
	if err != nil || int64(len(raw)) != source.Size || digest(string(raw)) != source.Hash {
		return zero, empty, domain.ErrNotReady
	}
	manuscript, metadata, err := novel.ReadExportArchive(raw)
	if err != nil {
		return zero, empty, err
	}
	var doc novelEditionDocument
	dec := json.NewDecoder(bytes.NewReader(metadata))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || dec.Decode(new(any)) != io.EOF || doc.Schema != 1 || source.Report.NovelEdition == nil || doc.Info != *source.Report.NovelEdition || doc.Info.BookID != book.ID || doc.Preview.Source != "published" || doc.Preview.Hash != doc.Info.PreviewHash || doc.Preview.Included != len(manuscript.Chapters) || doc.Preview.Included != doc.Info.Included || manuscript.Title != doc.Info.Title || manuscript.Author != doc.Info.Author || manuscript.ID != "urn:sha256:"+doc.Preview.Hash {
		return zero, empty, domain.ErrNotReady
	}
	p := doc.Preview
	p.Source = "edition"
	p.EditionID = source.ID
	p.EditionMetadata = &NovelEditionMetadata{Author: manuscript.Author, Language: manuscript.Language, Description: manuscript.Description}
	p.Hash = capabilityDigest(struct{ ID, Hash string }{source.ID, source.Hash})
	return p, manuscript, nil
}

func (s *NovelExport) editionSnapshot(ctx context.Context, actor, bookID, id string) (NovelExportPreview, novel.ExportBook, error) {
	var p NovelExportPreview
	var manuscript novel.ExportBook
	if !lowerHex32(id) {
		return p, manuscript, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return p, manuscript, err
	}
	defer tx.Rollback()
	item, source, err := editionSourceTx(ctx, tx, a, book, id)
	if err != nil {
		return p, manuscript, err
	}
	if !item.Published {
		return p, manuscript, fmt.Errorf("%w：该成书版本尚未保存完成", domain.ErrConflict)
	}
	return s.readEditionArchive(ctx, book, source)
}

func (s *NovelExport) Editions(ctx context.Context, actor, bookID, after string) (NovelEditionPage, error) {
	out := NovelEditionPage{Items: []NovelEditionItem{}}
	if after != "" && !lowerHex32(after) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	ids, err := queryIDs(ctx, tx, `SELECT i.id FROM source_imports i WHERE i.project_id=? AND i.branch_id=? AND i.id>? AND JSON_UNQUOTE(JSON_EXTRACT(i.report,'$.novel_edition.book_id'))=? AND (i.actor_id=? OR EXISTS(SELECT 1 FROM source_import_candidates c JOIN artifact_releases r ON r.id=c.release_id WHERE c.import_id=i.id AND r.project_id=i.project_id AND r.branch_id=i.branch_id AND r.status='PUBLISHED')) ORDER BY i.id LIMIT 101`, book.ProjectID, book.BranchID, after, book.ID, a.ID)
	if err != nil {
		return out, err
	}
	if len(ids) > 100 {
		ids = ids[:100]
		out.Next = ids[99]
	}
	for _, id := range ids {
		item, _, e := editionSourceTx(ctx, tx, a, book, id)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (s *NovelExport) EditionCover(ctx context.Context, actor, bookID, id string) ([]byte, string, error) {
	_, book, err := s.editionSnapshot(ctx, actor, bookID, id)
	if err != nil {
		return nil, "", err
	}
	if book.Cover == nil {
		return nil, "", domain.ErrNotReady
	}
	return book.Cover.Data, book.Cover.MIME, nil
}
