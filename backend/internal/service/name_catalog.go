package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"reflect"
	"slices"
	"strings"
	"time"
)

type NameCatalog struct {
	ProjectID  string        `json:"project_id"`
	BranchID   string        `json:"branch_id"`
	ResourceID string        `json:"resource_id"`
	Revision   int64         `json:"revision"`
	Hash       string        `json:"hash"`
	Document   NameMaterials `json:"document"`
}
type nameCatalogDocument struct {
	Schema    string        `json:"schema"`
	ActorID   string        `json:"actor_id"`
	Materials NameMaterials `json:"materials"`
}

func nameCatalogID(actor string, book NovelBook) string {
	return novel.CharacterDesignID(book.BranchID, "name-catalog", actor)
}

func readNameCatalogTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, book NovelBook) (NameCatalog, ResourceDraft, error) {
	id := nameCatalogID(actor.ID, book)
	out := NameCatalog{ResourceID: id, ProjectID: book.ProjectID, BranchID: book.BranchID}
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err == domain.ErrForbidden {
		materials, e := nameMaterials("", "")
		out.Document = materials
		out.Hash = capabilityDigest(materials)
		return out, ResourceDraft{}, e
	}
	if err != nil {
		return out, ResourceDraft{}, err
	}
	if r.BranchID != book.BranchID {
		return out, ResourceDraft{}, domain.ErrForbidden
	}
	rd, err := resourceDraftTx(ctx, tx, actor.ID, book.ProjectID, id)
	if err != nil {
		return out, rd, err
	}
	var body nameCatalogDocument
	if json.Unmarshal([]byte(rd.Text), &body) != nil || body.Schema != "name-material-catalog-v1" || body.ActorID != actor.ID {
		return out, rd, domain.ErrForbidden
	}
	out.Document = body.Materials
	out.Revision = rd.Revision
	out.Hash = capabilityDigest(body.Materials)
	return out, rd, nil
}
func (s *NovelCharacters) NameCatalog(ctx context.Context, actor, bookID string) (NameCatalog, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return NameCatalog{}, err
	}
	defer tx.Rollback()
	out, _, err := readNameCatalogTx(ctx, tx, a, book)
	return out, err
}
func validateNameMaterials(materials NameMaterials) error {
	if len(materials.Records) > 600 {
		return fmt.Errorf("%w：单个素材库最多600条，请按项目分组整理", domain.ErrValidation)
	}
	seen := map[string]bool{}
	for _, r := range materials.Records {
		key := r.ID + "\x00" + r.Language
		if !validName(r.ID, 120) || !validName(r.Label, 120) || !validName(r.Language, 30) || seen[key] || !slices.Contains([]string{"full_name", "surname", "given_name", "courtesy_name", "art_name", "nickname", "title"}, r.Kind) {
			return domain.ErrValidation
		}
		seen[key] = true
		if r.URL != "" {
			u, e := url.Parse(r.URL)
			if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
				return domain.ErrValidation
			}
		}
		if r.ReviewState != "" && !slices.Contains([]string{"unverified", "author_edited", "disputed"}, r.ReviewState) {
			return domain.ErrValidation
		}
		for _, value := range []string{r.Pronunciation, r.Original, r.Transliteration, r.Culture, r.Era, r.License, r.CapturedAt} {
			if len([]rune(value)) > 300 {
				return domain.ErrValidation
			}
		}
		if len([]rune(r.Evidence)) > 2000 || len(r.Tags) > 30 {
			return domain.ErrValidation
		}
	}
	raw, _ := json.Marshal(materials)
	if len(raw) > 240*1024 {
		return fmt.Errorf("%w：名字资料超过240KB，请减少长篇说明或分组保存", domain.ErrValidation)
	}
	return nil
}
func (s *NovelCharacters) SaveNameCatalog(ctx context.Context, actor, bookID string, in NameCatalog) (NameCatalog, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return NameCatalog{}, err
	}
	defer tx.Rollback()
	prior, rd, err := readNameCatalogTx(ctx, tx, a, book)
	if err != nil {
		return prior, err
	}
	if in.ResourceID != prior.ResourceID || in.Revision != prior.Revision || in.Hash != prior.Hash {
		return prior, domain.ErrConflict
	}
	old := map[string]NameMaterial{}
	for _, r := range prior.Document.Records {
		old[r.ID+"\x00"+r.Language] = r
	}
	for i, r := range in.Document.Records {
		if r.SourceImportID != "" {
			if err = validateNameImportTx(ctx, tx, a, book, r); err != nil {
				return prior, err
			}
		}
		if !reflect.DeepEqual(old[r.ID+"\x00"+r.Language], r) {
			in.Document.Records[i].ReviewState = "author_edited"
			in.Document.Records[i].CapturedAt = time.Now().UTC().Format(time.RFC3339)
			if r.License == "" {
				in.Document.Records[i].License = "unknown"
			}
		}
	}
	if err = validateNameMaterials(in.Document); err != nil {
		return prior, err
	}
	for _, r := range in.Document.Records {
		if r.License != in.Document.License {
			in.Document.License = "mixed; see record license"
			break
		}
	}
	if prior.Revision == 0 {
		if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", prior.ResourceID, book.ProjectID, book.BranchID, "名字素材库", "note"); err != nil {
			return prior, err
		}
		if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", prior.ResourceID); err != nil {
			return prior, err
		}
		branch, e := branchTx(ctx, tx, book.ProjectID, book.BranchID)
		if e != nil {
			return prior, e
		}
		rd.BaseHead = branch.Head
	}
	raw, err := json.Marshal(nameCatalogDocument{Schema: "name-material-catalog-v1", ActorID: actor, Materials: in.Document})
	if err != nil {
		return prior, err
	}
	rd.Text = string(raw)
	rd.Policy = &domain.KnowledgePolicy{Space: "working", Visibility: "author"}
	rd, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, prior.ResourceID, rd)
	if err != nil {
		return prior, err
	}
	if err = audit(ctx, tx, a, "name.catalog.save", prior.ResourceID); err != nil {
		return prior, err
	}
	in.Revision = rd.Revision
	in.ProjectID, in.BranchID = book.ProjectID, book.BranchID
	in.Hash = capabilityDigest(in.Document)
	return in, tx.Commit()
}
func (s *NovelCharacters) ensureNameCatalog(ctx context.Context, actor, bookID string) (NameCatalog, error) {
	out, err := s.NameCatalog(ctx, actor, bookID)
	if err != nil || out.Revision > 0 {
		return out, err
	}
	out, err = s.SaveNameCatalog(ctx, actor, bookID, out)
	if err == domain.ErrConflict {
		return s.NameCatalog(ctx, actor, bookID)
	}
	return out, err
}
func (s *NovelCharacters) OpenNameCatalog(ctx context.Context, actor, bookID string) (NameCatalog, error) {
	return s.ensureNameCatalog(ctx, actor, bookID)
}
func nameCatalogForDesignTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, d novel.CharacterDesign) (NameCatalog, error) {
	out, _, err := readNameCatalogTx(ctx, tx, a, book)
	if err != nil {
		return out, err
	}
	if d.Naming == nil || d.Naming.Catalog == nil {
		return out, fmt.Errorf("%w：请重新保存起名条件以固定名字资料版本", domain.ErrConflict)
	}
	p := d.Naming.Catalog
	if out.Revision < 1 || p.ResourceID != out.ResourceID || p.Revision != out.Revision || p.Hash != out.Hash {
		return out, fmt.Errorf("%w：名字素材库已变化，请重新保存起名条件", domain.ErrConflict)
	}
	return out, nil
}
func (s *NovelCharacters) PreviewNameImport(ctx context.Context, actor, bookID, text string) (NameCatalog, error) {
	out, err := s.NameCatalog(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	var incoming NameMaterials
	if len(text) > 240*1024 || json.Unmarshal([]byte(text), &incoming) != nil || incoming.Records == nil {
		return out, domain.ErrValidation
	}
	for i := range incoming.Records {
		r := &incoming.Records[i]
		if r.ID == "" {
			r.ID = novel.CharacterDesignID("import", r.Language, r.Label+"\x00"+r.Kind+"\x00"+r.URL)
		}
		if r.License == "" {
			r.License = incoming.License
		}
		if r.CapturedAt == "" {
			r.CapturedAt = incoming.CapturedAt
		}
		r.ReviewState = "unverified"
	}
	if err = validateNameMaterials(incoming); err != nil {
		return out, err
	}
	existing := map[string]bool{}
	for _, r := range out.Document.Records {
		existing[r.ID+"\x00"+r.Language] = true
	}
	for _, r := range incoming.Records {
		key := r.ID + "\x00" + r.Language
		if existing[key] {
			return out, fmt.Errorf("%w：导入记录ID已存在，请在库中编辑或使用新的ID", domain.ErrConflict)
		}
		existing[key] = true
		out.Document.Records = append(out.Document.Records, r)
	}
	return out, validateNameMaterials(out.Document)
}
func filterNameMaterials(materials NameMaterials, language, query string) NameMaterials {
	materials.Records = slices.DeleteFunc(slices.Clone(materials.Records), func(r NameMaterial) bool {
		return (language != "" && language != r.Language) || !strings.Contains(novel.NameKey(strings.Join([]string{r.Label, r.Pronunciation, r.Original, r.Transliteration, r.Culture, r.Era, strings.Join(r.Tags, " ")}, " ")), novel.NameKey(query))
	})
	return materials
}
