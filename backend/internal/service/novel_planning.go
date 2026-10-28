package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelPlanning struct{ IP *IP }
type NovelPlanDraft struct {
	Resource      domain.Resource         `json:"resource"`
	Revision      int64                   `json:"revision"`
	BaseHead      string                  `json:"base_head"`
	BaseNeedsSave bool                    `json:"base_needs_save,omitempty"`
	Hash          string                  `json:"hash"`
	Document      novel.PlanningDocument  `json:"document"`
	Published     *novel.PlanningDocument `json:"published,omitempty"`
	Ready         bool                    `json:"ready"`
	Reason        string                  `json:"reason"`
}
type NovelPlanningOverview struct {
	Stage     string            `json:"stage"`
	Reason    string            `json:"reason"`
	Resources []domain.Resource `json:"resources"`
	Sources   []domain.Resource `json:"sources"`
}

func (s *NovelPlanning) begin(ctx context.Context, actor, id string, write bool) (*sql.Tx, domain.Actor, NovelBook, error) {
	return (&NovelEntry{Workspace: s.IP.Workspace}).settingsTx(ctx, actor, id, write)
}
func (s *NovelPlanning) Open(ctx context.Context, actor, id, kind, scope string) (domain.Resource, error) {
	if !novel.PlanIdentity(kind, scope) {
		return domain.Resource{}, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, id, true)
	if err != nil {
		return domain.Resource{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind=? AND name=?", book.ProjectID, book.BranchID, novel.PlanKind(kind), planResourceName(kind, scope)).Scan(&existing)
	if err == nil {
		return resourceTx(ctx, tx, book.ProjectID, existing)
	}
	if err != sql.ErrNoRows {
		return domain.Resource{}, err
	}
	r := domain.Resource{ID: NewID(), BranchID: book.BranchID, Kind: novel.PlanKind(kind), Name: planResourceName(kind, scope), Revision: 1}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", r.ID, book.ProjectID, book.BranchID, r.Name, r.Kind); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "resource.create", r.ID); err != nil {
		return r, err
	}
	if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", r.ID); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func planIdentityFromResource(r domain.Resource) (string, string, error) {
	kind := strings.TrimPrefix(r.Kind, "novel_plan_")
	scope := ""
	if kind == "volume" || kind == "detail" {
		scope = strings.TrimPrefix(r.Name, novel.PlanKindName(kind)+" · ")
	}
	if !novel.PlanIdentity(kind, scope) || r.Name != planResourceName(kind, scope) {
		return "", "", domain.ErrValidation
	}
	return kind, scope, nil
}
func (s *NovelPlanning) draftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, resource string) (NovelPlanDraft, *planningCheck, error) {
	var result NovelPlanDraft
	r, err := resourceTx(ctx, tx, book.ProjectID, resource)
	if err != nil {
		return result, nil, err
	}
	kind, scope, err := planIdentityFromResource(r)
	if err != nil || r.BranchID != book.BranchID {
		return result, nil, domain.ErrForbidden
	}
	c, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return result, nil, err
	}
	d, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, r.ID)
	if err != nil {
		return result, c, err
	}
	doc := novel.PlanningDocument{Schema: 1, Kind: kind, Scope: scope, SettingsRevision: c.settingsRevision, SettingsHash: c.settingsHash, IPHead: c.head, Inputs: []domain.DependencyRef{}, Title: novel.PlanKindName(kind)}
	result.Resource = r
	result.Revision = d.Revision
	result.BaseHead = d.BaseHead
	if r.Head != "" {
		canonical, e := c.document(r)
		if e != nil {
			return result, c, e
		}
		result.Published = &canonical
		doc = canonical
		if d.Revision == 0 {
			result.BaseHead = r.Head
		}
	} else if kind == "world" {
		doc.Summary = book.Setup.Idea
		doc.Mainline = book.Setup.Conflict
		doc.Theme = book.Setup.Theme
		doc.Setting = book.Setup.World
	}
	if d.Revision > 0 {
		doc, err = decodeNovelPlan([]byte(d.Text))
		if err != nil {
			return result, c, err
		}
		// A lost successful publication reply may leave an identical private
		// draft. It is safe to edit from the exact content now at the head.
		if r.Hash != "" && digest(d.Text) == r.Hash {
			result.BaseHead = r.Head
			result.BaseNeedsSave = d.BaseHead != r.Head
		}
	}
	if doc.Kind != kind || doc.Scope != scope {
		return result, c, domain.ErrNotReady
	}
	result.Document = doc
	raw, _ := json.Marshal(doc)
	result.Hash = digest(string(raw))
	if result.BaseHead != r.Head {
		result.Reason = "其他版本已被采用，请先核对当前正式规划"
	} else if e := c.validate(r, doc); e != nil {
		result.Reason = e.Error()
	} else {
		result.Ready = true
	}
	return result, c, nil
}
func (s *NovelPlanning) Read(ctx context.Context, actor, id, resource string) (NovelPlanDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, id, false)
	if err != nil {
		return NovelPlanDraft{}, err
	}
	defer tx.Rollback()
	result, _, err := s.draftTx(ctx, tx, a, book, resource)
	return result, err
}
func (s *NovelPlanning) Save(ctx context.Context, actor, id, resource string, in NovelPlanDraft) (NovelPlanDraft, error) {
	if err := in.Document.Validate(false); err != nil {
		return in, err
	}
	tx, a, book, err := s.begin(ctx, actor, id, true)
	if err != nil {
		return in, err
	}
	r, err := resourceTx(ctx, tx, book.ProjectID, resource)
	tx.Rollback()
	if err != nil {
		return in, err
	}
	if r.BranchID != book.BranchID || r.Kind != novel.PlanKind(in.Document.Kind) || r.Name != planResourceName(in.Document.Kind, in.Document.Scope) {
		return in, domain.ErrForbidden
	}
	raw, _ := json.Marshal(in.Document)
	stored, err := s.IP.SaveResourceDraft(ctx, a, book.ProjectID, resource, ResourceDraft{Revision: in.Revision, BaseHead: in.BaseHead, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}})
	if err != nil {
		return in, err
	}
	in.Revision = stored.Revision
	in.Hash = digest(string(raw))
	in.Resource = r
	in.BaseNeedsSave = false
	return in, nil
}
func (s *NovelPlanning) Basis(ctx context.Context, actor, id, resource string, revision int64, sources []string) (NovelPlanDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, id, true)
	if err != nil {
		return NovelPlanDraft{}, err
	}
	defer tx.Rollback()
	result, c, err := s.draftTx(ctx, tx, a, book, resource)
	if err != nil {
		return result, err
	}
	if result.Revision != revision {
		return result, domain.ErrConflict
	}
	d := &result.Document
	d.SettingsRevision = c.settingsRevision
	d.SettingsHash = c.settingsHash
	d.IPHead = c.head
	d.Inputs = []domain.DependencyRef{}
	seen := map[string]bool{}
	add := func(r domain.Resource) {
		if !seen[r.ID] {
			d.Inputs = append(d.Inputs, planPin(r))
			seen[r.ID] = true
		}
	}
	require := func(kind, scope string) error {
		r, _, e := c.current(kind, scope)
		if e == nil {
			add(r)
		}
		return e
	}
	if d.Kind != "world" {
		if err = require("world", ""); err != nil {
			return result, err
		}
	}
	if d.Kind == "book" {
		for _, v := range d.Volumes {
			if err = require("volume", v.ID); err != nil {
				return result, err
			}
		}
	}
	if d.Kind == "detail" {
		r, outline, e := c.current("book", "")
		if e != nil {
			return result, e
		}
		add(r)
		found := false
		for _, v := range outline.Volumes {
			parent, volume, e := c.current("volume", v.ID)
			if e != nil {
				return result, e
			}
			for _, chapter := range volume.Chapters {
				if chapter.ID == d.Scope {
					add(parent)
					found = true
				}
			}
		}
		if !found {
			return result, fmt.Errorf("%w：请先将此章纳入已确认的全书安排", domain.ErrConflict)
		}
	}
	if len(sources) > 250 {
		return result, domain.ErrValidation
	}
	for _, source := range sources {
		r, e := resourceTx(ctx, tx, book.ProjectID, source)
		if e != nil {
			return result, e
		}
		if r.Head == "" || r.BranchID != book.BranchID || isNovelPlan(r.Kind) {
			return result, domain.ErrValidation
		}
		add(r)
	}
	raw, _ := json.Marshal(d)
	result.Hash = digest(string(raw))
	result.BaseHead = result.Resource.Head
	result.Ready = false
	result.Reason = "已更新依据，请核对规划内容后再确认"
	return result, nil
}
func (s *NovelPlanning) Prepare(ctx context.Context, actor, id, resource string, revision int64, hash, operation string) (domain.Release, error) {
	tx, a, book, err := s.begin(ctx, actor, id, true)
	if err != nil {
		return domain.Release{}, err
	}
	d, _, err := s.draftTx(ctx, tx, a, book, resource)
	tx.Rollback()
	if err != nil {
		return domain.Release{}, err
	}
	if !d.Ready || d.Revision != revision || d.Hash != hash || revision < 1 {
		return domain.Release{}, fmt.Errorf("%w：%s", domain.ErrConflict, d.Reason)
	}
	raw, _ := json.Marshal(d.Document)
	dependencies := d.Document.Dependencies()
	return s.IP.PrepareRelease(ctx, a, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: d.Document.IPHead, OperationID: operation, Updates: []domain.ResourceUpdate{{ResourceID: resource, ExpectedHead: d.BaseHead, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}, Dependencies: &dependencies}}})
}
func (s *NovelPlanning) Confirm(ctx context.Context, actor, id, resource, release, hash string, accept bool) (domain.Release, error) {
	if !accept {
		return domain.Release{}, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, id, true)
	if err != nil {
		return domain.Release{}, err
	}
	r, err := resourceTx(ctx, tx, book.ProjectID, resource)
	tx.Rollback()
	if err != nil {
		return domain.Release{}, err
	}
	if !isNovelPlan(r.Kind) || r.BranchID != book.BranchID {
		return domain.Release{}, domain.ErrForbidden
	}
	candidate, err := s.IP.ReadRelease(ctx, a, book.ProjectID, release)
	if err != nil {
		return candidate, err
	}
	if candidate.Hash != hash || candidate.Manifest.BranchID != book.BranchID || len(candidate.Manifest.Artifacts) != 1 || candidate.Manifest.Artifacts[0].ResourceID != resource {
		return candidate, domain.ErrConflict
	}
	if candidate.Status == "PUBLISHED" {
		return candidate, nil
	}
	if candidate.Status == "PREPARED" {
		if candidate, err = s.IP.AdoptRelease(ctx, a, book.ProjectID, release); err != nil {
			return candidate, err
		}
	}
	return s.IP.PublishRelease(ctx, a, book.ProjectID, release)
}
func (s *NovelPlanning) Overview(ctx context.Context, actor, id string) (NovelPlanningOverview, error) {
	result := NovelPlanningOverview{Stage: "WORLD_PLANNING", Resources: []domain.Resource{}, Sources: []domain.Resource{}}
	tx, _, book, err := s.begin(ctx, actor, id, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	c, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,kind,head_revision,content_hash,revision FROM resources WHERE project_id=? AND branch_id=? ORDER BY kind,name,id LIMIT 10001", book.ProjectID, book.BranchID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		r := domain.Resource{BranchID: book.BranchID}
		if err = rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Head, &r.Hash, &r.Revision); err != nil {
			rows.Close()
			return result, err
		}
		if isNovelPlan(r.Kind) {
			result.Resources = append(result.Resources, r)
		} else if r.Head != "" {
			result.Sources = append(result.Sources, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Resources)+len(result.Sources) > 10000 {
		return result, fmt.Errorf("%w：本书资料超过当前展示范围，不能截断后判断规划完成", domain.ErrNotReady)
	}
	if _, _, err = c.current("world", ""); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	result.Stage = "OUTLINE_PLANNING"
	if _, _, err = c.current("book", ""); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	result.Stage = "DETAIL_PLANNING"
	result.Reason = "全书及分卷章节安排已确认，可按章完善细纲。每章正文仍须本章细纲确认且依据有效。"
	return result, nil
}
