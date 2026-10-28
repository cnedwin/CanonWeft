package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

func planResourceName(kind, scope string) string {
	name := novel.PlanKindName(kind)
	if scope != "" {
		name += " · " + scope
	}
	return name
}
func isNovelPlan(kind string) bool { return strings.HasPrefix(kind, "novel_plan_") }
func decodeNovelPlan(raw []byte) (novel.PlanningDocument, error) {
	var doc novel.PlanningDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return doc, fmt.Errorf("%w：规划内容格式不完整", domain.ErrValidation)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return doc, domain.ErrValidation
	}
	return doc, doc.Validate(false)
}
func planPin(r domain.Resource) domain.DependencyRef {
	return domain.DependencyRef{Kind: "resource", BranchID: r.BranchID, ID: r.ID, Revision: r.Head, Hash: resourceNodeHash(r.Hash, r.Policy, r.Dependencies)}
}

type planningCheck struct {
	ip               *IP
	ctx              context.Context
	tx               *sql.Tx
	project          string
	branch           string
	head             string
	bookID           string
	settingsRevision int64
	settingsHash     string
	setup            novel.Setup
	valid            map[string]bool
	active           map[string]bool
}

func newPlanningCheck(ctx context.Context, tx *sql.Tx, ip *IP, project, branch string) (*planningCheck, error) {
	c := &planningCheck{ip: ip, ctx: ctx, tx: tx, project: project, branch: branch, valid: map[string]bool{}, active: map[string]bool{}}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT n.id,n.settings_revision,v.payload,v.content_hash FROM novels n JOIN novel_settings_versions v ON v.novel_id=n.id AND v.revision=n.settings_revision WHERE n.project_id=? AND n.branch_id=?", project, branch).Scan(&c.bookID, &c.settingsRevision, &raw, &c.settingsHash)
	if err != nil {
		return nil, err
	}
	if digest(raw) != c.settingsHash || json.Unmarshal([]byte(raw), &c.setup) != nil {
		return nil, domain.ErrNotReady
	}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return nil, err
	}
	c.head = b.Head
	return c, nil
}
func (c *planningCheck) resolve(kind, scope string) (domain.Resource, error) {
	var id string
	err := c.tx.QueryRowContext(c.ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind=? AND name=? ORDER BY id LIMIT 1", c.project, c.branch, novel.PlanKind(kind), planResourceName(kind, scope)).Scan(&id)
	if err == sql.ErrNoRows {
		return domain.Resource{}, fmt.Errorf("%w：请先完善并确认%s", domain.ErrConflict, novel.PlanKindName(kind))
	}
	if err != nil {
		return domain.Resource{}, err
	}
	return resourceTx(c.ctx, c.tx, c.project, id)
}
func (c *planningCheck) document(r domain.Resource) (novel.PlanningDocument, error) {
	if r.Head == "" {
		return novel.PlanningDocument{}, fmt.Errorf("%w：%s尚未确认发布", domain.ErrConflict, r.Name)
	}
	raw, err := c.ip.Blobs.ReadObject(c.ctx, c.project, r.Hash)
	if err != nil || digest(string(raw)) != r.Hash {
		return novel.PlanningDocument{}, domain.ErrNotReady
	}
	doc, err := decodeNovelPlan(raw)
	if err != nil {
		return doc, err
	}
	if r.Kind != novel.PlanKind(doc.Kind) || r.Name != planResourceName(doc.Kind, doc.Scope) {
		return doc, domain.ErrNotReady
	}
	return doc, nil
}
func (c *planningCheck) current(kind, scope string) (domain.Resource, novel.PlanningDocument, error) {
	r, err := c.resolve(kind, scope)
	if err != nil {
		return r, novel.PlanningDocument{}, err
	}
	d, err := c.document(r)
	if err != nil {
		return r, d, err
	}
	if c.valid[r.ID] {
		return r, d, nil
	}
	if c.active[r.ID] {
		return r, d, fmt.Errorf("%w：规划依据形成循环", domain.ErrValidation)
	}
	c.active[r.ID] = true
	err = c.validate(r, d)
	delete(c.active, r.ID)
	if err == nil {
		c.valid[r.ID] = true
	}
	return r, d, err
}
func (c *planningCheck) validateSettings(d novel.PlanningDocument) error {
	var settingsRaw, settingsHash string
	if err := c.tx.QueryRowContext(c.ctx, "SELECT payload,content_hash FROM novel_settings_versions WHERE novel_id=? AND revision=?", c.bookID, d.SettingsRevision).Scan(&settingsRaw, &settingsHash); err != nil {
		return domain.ErrConflict
	}
	var old novel.Setup
	if d.SettingsHash != settingsHash || digest(settingsRaw) != settingsHash || json.Unmarshal([]byte(settingsRaw), &old) != nil {
		return domain.ErrConflict
	}
	for _, change := range novel.SettingsChanges(old, c.setup) {
		if change.Scope == "planning" {
			return fmt.Errorf("%w：小说%s已变化，请复核规划依据", domain.ErrConflict, change.Label)
		}
	}
	if d.IPHead != c.head {
		return fmt.Errorf("%w：人物或世界设定已变化，请复核规划依据", domain.ErrConflict)
	}
	return nil
}
func (c *planningCheck) validate(r domain.Resource, d novel.PlanningDocument) error {
	if err := d.Validate(true); err != nil {
		return err
	}
	if r.Kind != novel.PlanKind(d.Kind) || r.Name != planResourceName(d.Kind, d.Scope) || r.BranchID != c.branch {
		return domain.ErrValidation
	}
	if err := c.validateSettings(d); err != nil {
		return err
	}
	required := map[string]domain.DependencyRef{}
	require := func(kind, scope string) (novel.PlanningDocument, error) {
		parent, doc, err := c.current(kind, scope)
		if err == nil {
			required[parent.ID] = planPin(parent)
		}
		return doc, err
	}
	switch d.Kind {
	case "volume":
		if _, err := require("world", ""); err != nil {
			return err
		}
	case "book":
		if _, err := require("world", ""); err != nil {
			return err
		}
		if len(d.Volumes) != c.setup.VolumeCount {
			return fmt.Errorf("%w：卷数与当前小说设置不一致，请先确认新的卷数", domain.ErrValidation)
		}
		seen := map[string]bool{}
		for _, v := range d.Volumes {
			volume, err := require("volume", v.ID)
			if err != nil {
				return err
			}
			if volume.Title != v.Title {
				return fmt.Errorf("%w：分卷标题已变化，请核对全书安排", domain.ErrConflict)
			}
			for _, chapter := range volume.Chapters {
				if seen[chapter.ID] {
					return fmt.Errorf("%w：不同卷重复使用了同一章节身份", domain.ErrValidation)
				}
				seen[chapter.ID] = true
			}
		}
		if int64(len(seen))*int64(c.setup.ChapterMax) < c.setup.TargetWords {
			return fmt.Errorf("%w：暂定章节安排不足以覆盖目标篇幅，请补全章节或调整篇幅设置", domain.ErrValidation)
		}
	case "detail":
		book, err := require("book", "")
		if err != nil {
			return err
		}
		if _, err = require("world", ""); err != nil {
			return err
		}
		found := false
		for _, v := range book.Volumes {
			parent, volume, e := c.current("volume", v.ID)
			if e != nil {
				return e
			}
			for _, chapter := range volume.Chapters {
				if chapter.ID == d.Scope {
					found = true
					required[parent.ID] = planPin(parent)
				}
			}
		}
		if !found {
			return fmt.Errorf("%w：此章不在已确认的全书章节安排中", domain.ErrConflict)
		}
		words := 0
		for _, scene := range d.Scenes {
			words += scene.Words
		}
		if words < c.setup.ChapterMin || words > c.setup.ChapterMax {
			return fmt.Errorf("%w：场景预计字数合计超出本章篇幅约定", domain.ErrValidation)
		}
	}
	for _, pin := range d.Inputs {
		input, err := resourceTx(c.ctx, c.tx, c.project, pin.ID)
		if err != nil {
			return err
		}
		if input.ID == r.ID || input.BranchID != c.branch || input.Head == "" || planPin(input) != pin {
			return fmt.Errorf("%w：采用的资料版本已变化，请重新核对", domain.ErrConflict)
		}
		if isNovelPlan(input.Kind) {
			if expected, ok := required[input.ID]; !ok || expected != pin {
				return fmt.Errorf("%w：规划上游阶段或版本不正确", domain.ErrValidation)
			}
		}
		delete(required, input.ID)
	}
	if len(required) > 0 {
		return fmt.Errorf("%w：尚未核对必需的上游规划，请更新依据后检查内容", domain.ErrConflict)
	}
	return nil
}

// Runs at preparation, adoption and publication, including the generic release
// endpoints. A JSON note or an unchecked planning release cannot advance a novel.
func (s *IP) validateNovelPlanArtifact(ctx context.Context, tx *sql.Tx, project string, r domain.Resource, ref domain.ArtifactRef, data []byte) error {
	if !isNovelPlan(r.Kind) {
		return nil
	}
	d, err := decodeNovelPlan(data)
	if err != nil {
		return err
	}
	if ref.Policy == nil || ref.Policy.Space != "working" || ref.Policy.Visibility != "author" {
		return fmt.Errorf("%w：小说规划必须保存在作者工作资料空间", domain.ErrValidation)
	}
	expected := d.Dependencies()
	if ref.Dependencies == nil || !reflect.DeepEqual(*ref.Dependencies, expected) {
		return fmt.Errorf("%w：规划依赖与内容不一致", domain.ErrValidation)
	}
	c, err := newPlanningCheck(ctx, tx, s, project, r.BranchID)
	if err != nil {
		return err
	}
	return c.validate(r, d)
}
