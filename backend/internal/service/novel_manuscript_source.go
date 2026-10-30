package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

type NovelManuscripts struct{ IP *IP }
type ManuscriptSource struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Hash       string   `json:"hash"`
	Size       int64    `json:"size"`
	Complete   bool     `json:"complete"`
	Extracted  int      `json:"extracted"`
	Pending    int      `json:"pending"`
	Characters int      `json:"characters"`
	Notes      []string `json:"notes"`
}

func manuscriptSourceSummary(v SourceImport) ManuscriptSource {
	out := ManuscriptSource{ID: v.ID, Name: v.Name, Hash: v.Hash, Size: v.Size, Complete: v.Report.Complete, Extracted: v.Report.Extracted, Pending: v.Report.Pending, Characters: len([]rune(v.Report.Text)), Notes: []string{}}
	for _, unit := range v.Report.Units {
		if unit.Status != "extracted" && len(out.Notes) < 20 {
			out.Notes = append(out.Notes, unit.Locator+"："+unit.Reason)
		}
	}
	return out
}

// The original is stored even when a supported parser rejects its contents.
// Parse retries produce a new immutable report; they never replace the raw file.
func (s *NovelManuscripts) Upload(ctx context.Context, actor, bookID, name, op string, data []byte) (ManuscriptSource, error) {
	if !validName(name, 160) || strings.ContainsAny(name, "/\\\x00") || !validName(op, 100) || len(data) < 1 || len(data) > 32<<20 {
		return ManuscriptSource{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return ManuscriptSource{}, err
	}
	tx.Rollback()
	report, parseErr := retrieval.ExtractManuscript(ctx, name, data)
	if ctx.Err() != nil {
		return ManuscriptSource{}, ctx.Err()
	}
	if parseErr != nil || retrieval.ValidateManuscriptReport(report) != nil {
		report = retrieval.ImportReport{Parser: retrieval.ManuscriptParser, Pending: 1, Units: []retrieval.ImportUnit{{Locator: "file:whole", Kind: "document", Status: "pending", Reason: "原稿已保留；解析未完成，请使用UTF-8 TXT/Markdown、可解析DOCX或重试。"}}}
	}
	v, err := s.IP.storeSourceImport(ctx, a, book.ProjectID, book.BranchID, name, op, data, report, "manuscript:"+book.ID)
	return manuscriptSourceSummary(v), err
}
func (s *NovelManuscripts) SourceReceipt(ctx context.Context, actor, bookID, op string) (*ManuscriptSource, error) {
	if !validName(op, 100) {
		return nil, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, _, err := requestReceiptTx(ctx, tx, a, "source_imports", op, false)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id)
	if err != nil {
		return nil, err
	}
	out := manuscriptSourceSummary(v)
	return &out, nil
}
func (s *NovelManuscripts) Sources(ctx context.Context, actor, bookID, after string) ([]ManuscriptSource, error) {
	if after != "" && !lowerHex32(after) {
		return nil, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Directory reads project metadata in SQL, never transferring all long texts
	// merely to count their characters. Expired envelopes are not import choices.
	// Primary-key order also avoids MySQL filesorting the large JSON backing row.
	rows, err := tx.QueryContext(ctx, `SELECT id,filename,source_hash,source_size,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(report,'$.complete')),'false')='true',
 COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(report,'$.extracted')) AS SIGNED),0),
 COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(report,'$.pending')) AS SIGNED),0),
 CHAR_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(report,'$.text')))
 FROM source_imports FORCE INDEX(PRIMARY) WHERE project_id=? AND branch_id=? AND actor_id=? AND id>?
 AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(report,'$.state')),'')<>'EXPIRED'
 AND JSON_TYPE(JSON_EXTRACT(report,'$.text'))='STRING' ORDER BY id LIMIT 51`, book.ProjectID, book.BranchID, a.ID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManuscriptSource{}
	for rows.Next() {
		v := ManuscriptSource{Notes: []string{}}
		if err = rows.Scan(&v.ID, &v.Name, &v.Hash, &v.Size, &v.Complete, &v.Extracted, &v.Pending, &v.Characters); err != nil {
			return nil, err
		}
		if v.Pending > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("解析有%d项待核对，已识别文字不代表完整原稿；请下载原稿核对，必要时重新解析。", v.Pending))
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *NovelManuscripts) Reparse(ctx context.Context, actor, bookID, sourceID, op string) (ManuscriptSource, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return ManuscriptSource{}, err
	}
	tx.Rollback()
	name, data, err := s.IP.ReadImportFile(ctx, a, book.ProjectID, book.BranchID, sourceID)
	if err != nil {
		return ManuscriptSource{}, err
	}
	return s.Upload(ctx, actor, bookID, name, op, data)
}
func (s *NovelManuscripts) Authorize(ctx context.Context, actor, bookID string) error {
	tx, _, _, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if tx != nil {
		tx.Rollback()
	}
	return err
}
func manuscriptSourceText(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id, hash, textHash string) (SourceImport, error) {
	source, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, id)
	if err != nil {
		return source, err
	}
	if len(source.Report.Text) > retrieval.ManuscriptMaxText || source.Hash != hash || (textHash != "" && digest(source.Report.Text) != textHash) {
		return source, fmt.Errorf("%w：原稿来源或解析文本已变化", domain.ErrConflict)
	}
	return source, nil
}
