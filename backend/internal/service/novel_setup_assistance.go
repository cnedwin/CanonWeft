package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"slices"
	"strings"
)

type NovelSetupAssistance struct {
	Generations *Generations
	Knowledge   *Knowledge
}
type SetupAssistInput struct {
	SetupID         string   `json:"setup_id"`
	Revision        int64    `json:"revision"`
	SourceIDs       []string `json:"source_ids"`
	Notes           string   `json:"notes"`
	AgentVersionID  string   `json:"agent_version_id"`
	PriceID         string   `json:"price_id"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	OperationID     string   `json:"operation_id"`
}
type SetupFieldCandidate struct {
	Field     string          `json:"field"`
	Value     json.RawMessage `json:"value"`
	Basis     string          `json:"basis"`
	SourceIDs []string        `json:"source_ids"`
	Reason    string          `json:"reason"`
}
type SetupSuggestion struct {
	GenerationID string                `json:"generation_id"`
	Revision     int64                 `json:"revision"`
	Fields       []SetupFieldCandidate `json:"fields"`
	Warnings     []string              `json:"warnings"`
}

var setupCreativeFields = strings.Fields("title genre language idea conflict audience world theme include avoid perspective style target_words volume_count chapter_min chapter_max characters")

func setupAssistanceSystem(setup novel.Setup) string {
	system := "帮助作者整理建书字段。资料只作为来源，未明确内容标为建议；不得更改IP关联、模型选择、权限、预算或代替作者确认建书。"
	if preset, ok := novel.SetupPresetFor(setup); ok {
		guide, _ := json.Marshal(preset)
		system += "\n以下为当前草稿精确版本对应的题材方法与读者期待，仅作为可修改的创作参考，作者当前输入和修改优先。静态示例不代表AI已经理解作者想法；字段需要真正结合current_setup.idea和requirements提出候选，说明依据；不得把方法引用当作已安装技能或模型质量通过。\n" + string(guide)
	} else if setup.PresetID != "" {
		system += "\n当前草稿的预设来源版本未匹配现有目录；保留作者草稿，不套用当前目录版本或虚称其题材指导，依照current_setup及作者要求提出建议。"
	}
	return system
}

func setupWorkspaceKey(d NovelSetupDraft) string {
	return "novel-setup-branch-" + d.ID + "-" + capabilityDigest([]string{d.Setup.IPMode, d.Setup.ProjectID, d.Setup.BranchID, d.Setup.BaseHead})[:16]
}
func setupWorkspaceName(d NovelSetupDraft) string { return "创作准备 · " + d.ID[:12] }
func preparedSetupWorkspaceTx(ctx context.Context, tx *sql.Tx, a domain.Actor, d NovelSetupDraft) (CreativeWorkspace, error) {
	_, id, err := operationReceiptTx(ctx, tx, a, setupWorkspaceKey(d))
	if err != nil {
		return CreativeWorkspace{}, err
	}
	var project string
	if err = tx.QueryRowContext(ctx, "SELECT project_id FROM branches WHERE id=?", id).Scan(&project); err != nil {
		return CreativeWorkspace{}, err
	}
	p, err := projectTx(ctx, tx, a, project)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	if !domain.CanEdit(p.Role) {
		return CreativeWorkspace{}, domain.ErrForbidden
	}
	b, err := branchTx(ctx, tx, project, id)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	// The normal branch-create receipt fixes the exact project, parent and base.
	hash, _, err := operationReceiptTx(ctx, tx, a, setupWorkspaceKey(d))
	if err != nil {
		return CreativeWorkspace{}, err
	}
	expected := digest("branch.create\x00" + project + "\x00" + setupWorkspaceName(d) + "\x00" + d.Setup.BranchID + "\x00" + d.Setup.BaseHead)
	if hash != expected || b.ParentID != d.Setup.BranchID || b.BaseRevision != d.Setup.BaseHead || (d.Setup.IPMode == "existing" && project != d.Setup.ProjectID) {
		return CreativeWorkspace{}, domain.ErrConflict
	}
	return CreativeWorkspace{Project: p, Actor: a, Branch: b}, nil
}

// Allocate the real source/task scope before book confirmation, then reuse it
// on confirmation. Uploads never live in a disposable or foreign scratch book.
func (s *NovelSetupAssistance) Workspace(ctx context.Context, actor, id string, revision int64) (CreativeWorkspace, error) {
	ip := s.Generations.IP
	entry := &NovelEntry{Workspace: ip.Workspace}
	d, err := entry.ReadDraft(ctx, actor, id)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	if d.Revision != revision || d.ConfirmedNovelID != "" {
		return CreativeWorkspace{}, domain.ErrConflict
	}
	project := d.Setup.ProjectID
	if d.Setup.IPMode == "new" {
		p, e := ip.Workspace.CreateProject(ctx, actor, setupWorkspaceName(d), "novel-setup-project-"+id)
		if e != nil {
			return CreativeWorkspace{}, e
		}
		project = p.ID
	} else if d.Setup.IPMode != "existing" || !novel.ValidID(project) || !novel.ValidID(d.Setup.BranchID) || !novel.ValidID(d.Setup.BaseHead) {
		return CreativeWorkspace{}, fmt.Errorf("%w：请先选择新IP或已有IP的已发布版本", domain.ErrValidation)
	}
	if _, err = ip.CreateBranch(ctx, domain.Actor{ID: actor}, project, setupWorkspaceName(d), d.Setup.BranchID, d.Setup.BaseHead, setupWorkspaceKey(d)); err != nil {
		return CreativeWorkspace{}, err
	}
	tx, a, err := entry.begin(ctx, actor, true)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	defer tx.Rollback()
	current, err := readNovelDraftTx(ctx, tx, a, id)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	if current.Revision != revision || current.ConfirmedNovelID != "" {
		return CreativeWorkspace{}, domain.ErrConflict
	}
	w, err := preparedSetupWorkspaceTx(ctx, tx, a, current)
	tx.Rollback()
	if err != nil {
		return w, err
	}
	_, err = (&Capabilities{IP: ip}).InstallNarrative(ctx, a, project, "novel-setup-assistant")
	return w, err
}

func (s *NovelSetupAssistance) Prepare(ctx context.Context, actor, id string, in SetupAssistInput) (Generation, error) {
	var zero Generation
	if in.SourceIDs == nil {
		in.SourceIDs = []string{}
	}
	if in.SetupID != id || !validName(in.Notes, 10000) {
		return zero, domain.ErrValidation
	}
	w, err := s.Workspace(ctx, actor, id, in.Revision)
	if err != nil {
		return zero, err
	}
	entry := &NovelEntry{Workspace: s.Generations.IP.Workspace}
	tx, a, err := entry.begin(ctx, actor, true)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	fingerprint := capabilityDigest([]any{"novel-setup-assistance-v1", id, in})
	prior, stored, replayErr := requestReceiptTx(ctx, tx, a, "generation_tasks", in.OperationID, false)
	if replayErr == nil {
		if stored != fingerprint {
			return zero, domain.ErrConflict
		}
		return generationTx(ctx, tx, w.Project.ID, prior)
	}
	if replayErr != sql.ErrNoRows {
		return zero, replayErr
	}
	d, err := readNovelDraftTx(ctx, tx, a, id)
	if err != nil {
		return zero, err
	}
	if d.Revision != in.Revision || d.ConfirmedNovelID != "" {
		return zero, domain.ErrConflict
	}
	if err = validateCharacterSourcesTx(ctx, tx, NovelBook{ProjectID: w.Project.ID, BranchID: w.Branch.ID}, in.SourceIDs); err != nil {
		return zero, err
	}
	tx.Rollback()
	knowledge := s.Knowledge
	if knowledge == nil {
		return zero, domain.ErrNotReady
	}
	_, err = knowledge.Status(ctx, a, w.Project.ID, w.Branch.ID)
	if errors.Is(err, domain.ErrNotReady) {
		_, err = knowledge.Build(ctx, a, w.Project.ID, w.Branch.ID)
	}
	if err != nil {
		return zero, err
	}
	query := []rune(in.Notes)
	if len(query) > 1000 {
		query = query[:1000]
	}
	pack, err := knowledge.Search(ctx, a, w.Project.ID, w.Branch.ID, SearchInput{Purpose: "author", Query: string(query), Limit: 8, TokenBudget: 131072, IncludeWorking: true, RequiredResourceIDs: in.SourceIDs})
	if err != nil {
		return zero, err
	}
	if err = validateCharacterSourceContext(in.SourceIDs, pack); err != nil {
		return zero, err
	}
	agent, err := s.Generations.creativeAgent(ctx, a, w.Project.ID, "novel-setup-assistant", in.AgentVersionID)
	if err != nil {
		return zero, err
	}
	payload, _ := json.Marshal(map[string]any{"setup_id": id, "revision": d.Revision, "current_setup": d.Setup, "requirements": in.Notes, "source_ids": in.SourceIDs})
	return s.Generations.Prepare(ctx, a, w.Project.ID, GenerationInput{creative: true, novelPlanningFingerprint: fingerprint, BranchID: w.Branch.ID, ExpectedIPHead: pack.IPHead, Role: "architect", ContextID: pack.ID, PriceID: in.PriceID, OperationID: in.OperationID, AgentVersionID: agent.ID, CapabilityTrial: true, AgentInput: payload, Request: models.Request{System: setupAssistanceSystem(d.Setup), Prompt: string(payload), MaxOutputTokens: in.MaxOutputTokens, Creative: &models.CreativeSource{Kind: "novel_setup", SetupID: id, Revision: d.Revision, Hash: valueHash(d.Setup)}}})
}

func (s *Generations) validateSetupCreativeTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	pin := g.Frozen.Creative
	if pin == nil || pin.Kind != "novel_setup" || g.Frozen.Role != "architect" || g.Frozen.Capability == nil {
		return domain.ErrValidation
	}
	a := domain.Actor{ID: g.ActorID, WorkspaceID: g.WorkspaceID}
	d, err := readNovelDraftTx(ctx, tx, a, pin.SetupID)
	if err != nil {
		return err
	}
	if d.Revision != pin.Revision || valueHash(d.Setup) != pin.Hash || d.ConfirmedNovelID != "" {
		return fmt.Errorf("%w：建书草稿已变化，请基于新草稿重新准备建议", domain.ErrConflict)
	}
	var branch string
	_, branch, err = operationReceiptTx(ctx, tx, a, setupWorkspaceKey(d))
	if err != nil {
		return err
	}
	if branch != g.BranchID {
		return domain.ErrConflict
	}
	return nil
}

func (s *NovelSetupAssistance) Suggestion(ctx context.Context, actor, id, task string) (SetupSuggestion, error) {
	var out SetupSuggestion
	entry := &NovelEntry{Workspace: s.Generations.IP.Workspace}
	d, err := entry.ReadDraft(ctx, actor, id)
	if err != nil {
		return out, err
	}
	tx, a, err := entry.begin(ctx, actor, true)
	if err != nil {
		return out, err
	}
	w, err := preparedSetupWorkspaceTx(ctx, tx, a, d)
	tx.Rollback()
	if err != nil {
		return out, err
	}
	g, err := s.Generations.Read(ctx, a, w.Project.ID, task)
	if err != nil {
		return out, err
	}
	if g.ActorID != actor || g.BranchID != w.Branch.ID || g.State != "SUCCEEDED" || g.Cancelled || g.Output == nil || g.Frozen.Creative == nil || g.Frozen.Creative.SetupID != id {
		return out, domain.ErrConflict
	}
	tx, a, err = entry.begin(ctx, actor, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = s.Generations.validateSetupCreativeTx(ctx, tx, g); err != nil {
		return out, err
	}
	pack, err := (&Knowledge{IP: s.Generations.IP}).readContextTx(ctx, tx, actor, g.ProjectID, g.BranchID, g.Frozen.ContextID)
	if err != nil {
		return out, err
	}
	var proposal struct {
		Fields   []SetupFieldCandidate `json:"fields"`
		Warnings []string              `json:"warnings"`
	}
	if json.Unmarshal([]byte(g.Output.Text), &proposal) != nil {
		return out, domain.ErrValidation
	}
	seen := map[string]bool{}
	allowedSources := map[string]bool{}
	for _, source := range pack.Required {
		allowedSources[source.Dependency.Source.ID] = true
	}
	for _, field := range proposal.Fields {
		if !slices.Contains(setupCreativeFields, field.Field) || seen[field.Field] {
			return out, domain.ErrValidation
		}
		seen[field.Field] = true
		for _, source := range field.SourceIDs {
			if !allowedSources[source] {
				return out, domain.ErrValidation
			}
		}
	}
	warnings, err := setupSuggestionWarnings(d.Setup, proposal.Fields, proposal.Warnings)
	if err != nil {
		return out, err
	}
	return SetupSuggestion{GenerationID: g.ID, Revision: d.Revision, Fields: proposal.Fields, Warnings: warnings}, nil
}

// Showing concrete character candidates does not adopt them. The same derived
// mode change applies only if the author selects the characters field later.
func setupSuggestionWarnings(current novel.Setup, fields []SetupFieldCandidate, warnings []string) ([]string, error) {
	next, err := applySetupFields(current, fields)
	if err != nil {
		return nil, err
	}
	result := append([]string(nil), warnings...)
	if current.CharacterMode != "manual" && next.CharacterMode == "manual" {
		result = append(result, "仅展示人物建议不会更改草稿。明确采用非空人物建议后，初始人物将使用这些可编辑人物；只采用书名等其他字段时，人物模式保持原选择。")
	}
	return result, nil
}

func applySetupFields(current novel.Setup, fields []SetupFieldCandidate) (novel.Setup, error) {
	raw, _ := json.Marshal(current)
	var document map[string]json.RawMessage
	json.Unmarshal(raw, &document)
	seen := map[string]bool{}
	for _, f := range fields {
		if !slices.Contains(setupCreativeFields, f.Field) || seen[f.Field] || !json.Valid(f.Value) {
			return current, domain.ErrValidation
		}
		seen[f.Field] = true
		document[f.Field] = f.Value
	}
	raw, _ = json.Marshal(document)
	var next novel.Setup
	if json.Unmarshal(raw, &next) != nil {
		return current, domain.ErrValidation
	}
	// Concrete characters and architect mode are mutually exclusive. This is
	// derived from the selected characters field, never a model-writable mode.
	if seen["characters"] && len(next.Characters) > 0 {
		next.CharacterMode = "manual"
	}
	if err := next.Validate(false); err != nil {
		return current, err
	}
	return next, nil
}

type SetupAssistAdoption struct {
	GenerationID string                `json:"generation_id"`
	Revision     int64                 `json:"revision"`
	Fields       []SetupFieldCandidate `json:"fields"`
}

func (s *NovelSetupAssistance) Adopt(ctx context.Context, actor, id string, in SetupAssistAdoption) (NovelSetupDraft, error) {
	entry := &NovelEntry{Workspace: s.Generations.IP.Workspace, assistanceIP: s.Generations.IP}
	d, err := entry.ReadDraft(ctx, actor, id)
	if err != nil {
		return d, err
	}
	suggestion, err := s.Suggestion(ctx, actor, id, in.GenerationID)
	if err != nil {
		return d, err
	}
	if d.Revision != in.Revision || suggestion.Revision != in.Revision || len(in.Fields) == 0 {
		return d, domain.ErrConflict
	}
	for _, field := range in.Fields {
		found := false
		for _, candidate := range suggestion.Fields {
			if candidate.Field == field.Field {
				found = true
				break
			}
		}
		if !found {
			return d, domain.ErrValidation
		}
	}
	d.Setup, err = applySetupFields(d.Setup, in.Fields)
	if err != nil {
		return d, err
	}
	d.assistantID = in.GenerationID
	// SaveDraft's revision compare-and-swap protects edits arriving after review.
	return entry.SaveDraft(ctx, actor, d)
}

func validateSetupAdoptionTx(ctx context.Context, tx *sql.Tx, a domain.Actor, id, generation string, ip *IP) error {
	if ip == nil {
		return domain.ErrNotReady
	}
	var project string
	if err := tx.QueryRowContext(ctx, "SELECT project_id FROM generation_tasks WHERE id=? AND actor_id=?", generation, a.ID).Scan(&project); err != nil {
		return domain.ErrForbidden
	}
	if _, err := projectTx(ctx, tx, a, project); err != nil {
		return err
	}
	g, err := generationTx(ctx, tx, project, generation)
	if err != nil {
		return err
	}
	if g.State != "SUCCEEDED" || g.Cancelled || g.Frozen.Creative == nil || g.Frozen.Creative.SetupID != id {
		return domain.ErrConflict
	}
	gen := &Generations{}
	if err = gen.validateSetupCreativeTx(ctx, tx, g); err != nil {
		return err
	}
	_, err = (&Knowledge{IP: ip}).readContextTx(ctx, tx, a.ID, project, g.BranchID, g.Frozen.ContextID)
	return err
}
