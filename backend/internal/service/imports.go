package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"strings"
)

type SourceImport struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	Hash   string                 `json:"hash"`
	Size   int64                  `json:"size"`
	Report retrieval.ImportReport `json:"report"`
}
type ImportCandidateInput struct {
	Policy         *domain.KnowledgePolicy `json:"policy,omitempty"`
	Text           string                  `json:"text"`
	ExpectedIPHead string                  `json:"expected_ip_head"`
	OperationID    string                  `json:"operation_id"`
	AcceptPartial  bool                    `json:"accept_partial"`
}

func (s *IP) ImportSource(ctx context.Context, a domain.Actor, project, branch, name, op string, data []byte) (SourceImport, error) {
	var result SourceImport
	if !validName(name, 160) || !validName(op, 120) || strings.ContainsAny(name, "/\\\x00") {
		return result, domain.ErrValidation
	}
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	_, err = branchTx(ctx, tx, project, branch)
	tx.Rollback()
	if err != nil {
		return result, err
	}
	report, err := retrieval.Extract(ctx, name, data)
	if err != nil {
		var documentError *retrieval.DocumentError
		if errors.As(err, &documentError) {
			return result, fmt.Errorf("%w：%s", domain.ErrValidation, documentError.Error())
		}
		return result, domain.ErrValidation
	}
	return s.storeSourceImport(ctx, a, project, branch, name, op, data, report, "")
}

func (s *IP) storeSourceImport(ctx context.Context, a domain.Actor, project, branch, name, op string, data []byte, report retrieval.ImportReport, extraIdentity string) (SourceImport, error) {
	var result SourceImport
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return result, err
	}
	fingerprint := digest(project + "\x00" + branch + "\x00" + name + "\x00" + digest(string(data)))
	if extraIdentity != "" {
		fingerprint = digest(fingerprint + "\x00" + extraIdentity)
	}
	var id, stored string
	id, stored, err = requestReceiptTx(ctx, tx, a, "source_imports", op, false)
	if err == nil {
		if stored != fingerprint {
			return result, domain.ErrConflict
		}
		return importTx(ctx, tx, a.ID, project, branch, id)
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	if report.CharacterImage != nil {
		if err := requireImageCharacterTx(ctx, tx, a.ID, project, branch, report.CharacterImage.CharacterID); err != nil {
			return result, err
		}
	}
	if report.NovelImage != nil {
		book, err := readNovelTx(ctx, tx, a, report.NovelImage.BookID)
		if err != nil {
			return result, err
		}
		if book.ProjectID != project || book.BranchID != branch {
			return result, domain.ErrForbidden
		}
	}
	if report.NovelEdition != nil {
		book, err := readNovelTx(ctx, tx, a, report.NovelEdition.BookID)
		if err != nil {
			return result, err
		}
		if book.ProjectID != project || book.BranchID != branch {
			return result, domain.ErrForbidden
		}
	}
	blob, err := objectwriter.Write(ctx, tx, s.Blobs, project, data)
	if err != nil {
		return result, err
	}
	result = SourceImport{ID: NewID(), Name: name, Hash: blob.Hash, Size: blob.Size, Report: report}
	raw, _ := json.Marshal(report)
	_, err = tx.ExecContext(ctx, "INSERT INTO source_imports(id,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?)", result.ID, project, branch, a.ID, name, blob.Hash, blob.Size, raw, fingerprint, op)
	if err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "source.import", result.ID); err != nil {
		return result, err
	}
	if err = s.captureImportSourceTx(ctx, tx, project, "source_imports", result.ID); err != nil {
		return result, err
	}
	if report.Parser == retrieval.ManuscriptParser {
		// An existing author-draft retention root keeps the original bytes and
		// report together. It does not turn imported prose into knowledge/canon.
		b, e := branchTx(ctx, tx, project, branch)
		if e != nil {
			return result, e
		}
		content := domain.ImportDraftContent{BaseIPHead: b.Head, OperationID: "manuscript-source-" + result.ID, Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}
		payload, e := json.Marshal(content)
		if e != nil {
			return result, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO import_drafts(import_id,actor_id,project_id,revision,payload,content_hash) VALUES(?,?,?,1,?,?)", result.ID, a.ID, project, payload, digest(string(payload))); e != nil {
			return result, e
		}
		if e = s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "import_drafts", Primary: map[string]string{"import_id": result.ID, "actor_id": a.ID}}); e != nil {
			return result, e
		}
	}
	return result, tx.Commit()
}
func importTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (SourceImport, error) {
	var result SourceImport
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,filename,source_hash,source_size,report FROM source_imports WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?", id, project, branch, actor).Scan(&result.ID, &result.Name, &result.Hash, &result.Size, &raw)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	if err := rejectExpiredRuntime(raw, "source_imports.report"); err != nil {
		return result, err
	}
	if err := requireRuntimeObjectTx(ctx, tx, project, "source_imports", id, "source_hash", result.Hash); err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result.Report)
	return result, err
}
func (s *IP) ReadImport(ctx context.Context, a domain.Actor, project, branch, id string) (SourceImport, error) {
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return SourceImport{}, err
	}
	defer tx.Rollback()
	return importTx(ctx, tx, a.ID, project, branch, id)
}
func (s *IP) ImportCandidate(ctx context.Context, a domain.Actor, project, branch, id string, input ImportCandidateInput) (domain.Release, error) {
	var empty domain.Release
	if input.Policy != nil && input.Policy.Validate() != nil {
		return empty, domain.ErrValidation
	}
	source, err := s.ReadImport(ctx, a, project, branch, id)
	if err != nil {
		return empty, err
	}
	if (!source.Report.Complete && !input.AcceptPartial) || strings.TrimSpace(input.Text) == "" || !validName(input.OperationID, 100) {
		return empty, domain.ErrValidation
	}
	resource, err := s.CreateResource(ctx, a, project, ResourceInput{BranchID: branch, Name: source.Name, Kind: "source", OperationID: "import-resource-" + id})
	if err != nil {
		return empty, err
	}
	// The same import is a single source revision. Editing an already published
	// resource uses the normal revision-aware editor, never this initial import.
	return s.PrepareRelease(ctx, a, project, domain.ReleaseInput{BranchID: branch, ExpectedIPHead: input.ExpectedIPHead, OperationID: input.OperationID, Updates: []domain.ResourceUpdate{{ResourceID: resource.ID, ExpectedHead: "", Text: input.Text, SourceImportID: id, PartialAcknowledged: input.AcceptPartial, Policy: input.Policy}}})
}
