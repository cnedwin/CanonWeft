package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/presets"
	"slices"
	"strings"
	"unicode/utf8"
)

type CreativeWorkspace struct {
	Project domain.Project `json:"project"`
	Actor   domain.Actor   `json:"actor"`
	Branch  Branch         `json:"branch"`
}
type VisualAssistInput struct {
	Novel           *models.NovelImageTarget `json:"novel,omitempty"`
	ExpectedIPHead  string                   `json:"expected_ip_head"`
	CharacterID     string                   `json:"character_id"`
	Look            string                   `json:"look"`
	References      []string                 `json:"references"`
	PreviousBrief   string                   `json:"previous_brief"`
	Notes           string                   `json:"notes"`
	PriceID         string                   `json:"price_id"`
	AgentVersionID  string                   `json:"agent_version_id"`
	MaxOutputTokens int                      `json:"max_output_tokens"`
	OperationID     string                   `json:"operation_id"`
}
type VisualProposal struct {
	Novel             *models.NovelImageTarget `json:"novel,omitempty"`
	CharacterID       string                   `json:"character_id"`
	Look              string                   `json:"look"`
	VisualBrief       string                   `json:"visual_brief"`
	RenderPrompt      string                   `json:"render_prompt"`
	PreserveFeatures  []string                 `json:"preserve_features"`
	ProposedDetails   []string                 `json:"proposed_details"`
	OpenQuestions     []string                 `json:"open_questions"`
	ConsistencyChecks []string                 `json:"consistency_checks"`
}
type VisualSuggestion struct {
	References    []models.ImageReference `json:"references"`
	GenerationID  string                  `json:"generation_id"`
	CharacterHash string                  `json:"character_hash"`
	Proposal      VisualProposal          `json:"proposal"`
}

func (s *Generations) VisualAssistantWorkspace(ctx context.Context, a domain.Actor, project, branch string) (CreativeWorkspace, error) {
	tx, a, p, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return CreativeWorkspace{}, err
	}
	b, err := branchTx(ctx, tx, project, branch)
	tx.Rollback()
	if err != nil {
		return CreativeWorkspace{}, err
	}
	_, err = (&Capabilities{IP: s.IP}).InstallNarrative(ctx, a, project, "character-visual-director")
	return CreativeWorkspace{Project: p, Actor: a, Branch: b}, err
}

func (s *Generations) creativeAgent(ctx context.Context, a domain.Actor, project, preset, selected string) (domain.CapabilityRef, error) {
	installed, err := (&Capabilities{IP: s.IP}).InstallNarrative(ctx, a, project, preset)
	if err != nil {
		return domain.CapabilityRef{}, err
	}
	for _, ref := range installed.Versions {
		if ref.Kind == "agent" {
			if selected == "" {
				return ref, nil
			}
			break
		}
	}
	v, err := (&Capabilities{IP: s.IP}).Read(ctx, a, project, selected)
	if err != nil {
		return domain.CapabilityRef{}, err
	}
	bundle, err := presets.Bundle(preset)
	if err != nil {
		return domain.CapabilityRef{}, err
	}
	if v.Definition.Kind != "agent" || capabilityDigest(v.Definition.InputSchema) != capabilityDigest(json.RawMessage(bundle.Find("input.schema.json"))) || capabilityDigest(v.Definition.OutputSchema) != capabilityDigest(json.RawMessage(bundle.Find("output.schema.json"))) {
		return domain.CapabilityRef{}, domain.ErrValidation
	}
	return domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: "agent"}, nil
}

func (s *Generations) PrepareVisualAssistant(ctx context.Context, a domain.Actor, project, branch string, in VisualAssistInput) (Generation, error) {
	var zero Generation
	if in.Novel != nil {
		return zero, domain.ErrValidation
	}
	if !validName(in.Look, 120) || !validName(in.Notes, 10000) || !utf8.ValidString(in.PreviousBrief) || utf8.RuneCountInString(in.PreviousBrief) > 16000 || len(in.References) > 16 {
		return zero, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	basis, err := characterImageBasisTx(ctx, tx, a.ID, project, branch, in.CharacterID)
	if err != nil {
		return zero, err
	}
	if utf8.RuneCount(basis) > 16000 {
		return zero, fmt.Errorf("%w：完整人设超出视觉方案容量，请先整理本轮造型相关设定", domain.ErrValidation)
	}
	var values struct {
		Character  domain.Entity      `json:"character"`
		Assertions []domain.Assertion `json:"assertions"`
	}
	if json.Unmarshal(basis, &values) != nil {
		return zero, domain.ErrNotReady
	}
	locked := []string{}
	addLocked := func(text string) {
		r := []rune(text)
		for len(r) > 0 {
			n := min(500, len(r))
			locked = append(locked, string(r[:n]))
			r = r[n:]
		}
	}
	if values.Character.Locked {
		addLocked(values.Character.Name + "：" + values.Character.Description)
	}
	for _, item := range values.Assertions {
		if item.Locked {
			addLocked(item.Predicate + "：" + item.Value)
		}
	}
	if len(locked) > 100 {
		return zero, domain.ErrValidation
	}
	pin := &models.CharacterImageTask{CharacterID: in.CharacterID, Look: in.Look, CharacterHash: digest(string(basis)), Basis: string(basis), References: []models.ImageReference{}}
	references := []map[string]string{}
	seen := map[string]bool{}
	for _, id := range in.References {
		if seen[id] {
			return zero, domain.ErrValidation
		}
		seen[id] = true
		item, e := characterImageTx(ctx, tx, a.ID, project, branch, id)
		if e != nil {
			return zero, e
		}
		m := item.Report.CharacterImage
		if m.CharacterID != in.CharacterID {
			return zero, domain.ErrValidation
		}
		pin.References = append(pin.References, models.ImageReference{ID: id, Hash: item.Hash})
		references = append(references, map[string]string{"id": id, "hash": item.Hash, "look": m.Look, "description": m.Description})
	}
	tx.Rollback()
	agent, err := s.creativeAgent(ctx, a, project, "character-visual-director", in.AgentVersionID)
	if err != nil {
		return zero, err
	}
	payload, _ := json.Marshal(map[string]any{"character_id": in.CharacterID, "look": in.Look, "name": values.Character.Name, "canonical_appearance": string(basis), "locked_features": locked, "instructions": in.Notes, "reference_images": references, "previous_brief": in.PreviousBrief})
	return s.Prepare(ctx, a, project, GenerationInput{creative: true, BranchID: branch, ExpectedIPHead: in.ExpectedIPHead, Role: "asset_designer", PriceID: in.PriceID, OperationID: in.OperationID, AgentVersionID: agent.ID, CapabilityTrial: true, AgentInput: payload, Request: models.Request{System: "整理角色视觉文字方案。你收到的是角色设定和参考图说明，并未收到图像像素；不得声称看到了参考图。所有方案只供作者核对，不能修改角色身份。", Prompt: string(payload), MaxOutputTokens: in.MaxOutputTokens, Creative: &models.CreativeSource{Kind: "character_visual", Visual: pin}}})
}

func (s *Generations) validateCreativeSourceTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	source := g.Frozen.Creative
	if source == nil {
		return nil
	}
	if source.Kind == "novel_setup" {
		return s.validateSetupCreativeTx(ctx, tx, g)
	}
	if source.Kind == "novel_visual" {
		if source.Visual == nil || source.Visual.Novel == nil || g.Frozen.Role != "asset_designer" || g.Frozen.Capability == nil {
			return domain.ErrValidation
		}
		return s.validateNovelVisualBasisTx(ctx, tx, g, source.Visual)
	}
	if source.Kind != "character_visual" || source.Visual == nil || g.Frozen.Role != "asset_designer" || g.Frozen.Capability == nil {
		return domain.ErrValidation
	}
	pin := source.Visual
	basis, err := characterImageBasisTx(ctx, tx, g.ActorID, g.ProjectID, g.BranchID, pin.CharacterID)
	if err != nil {
		return err
	}
	if digest(string(basis)) != pin.CharacterHash || digest(pin.Basis) != pin.CharacterHash {
		return fmt.Errorf("%w：角色设定已变化，请重新完善视觉方案", domain.ErrConflict)
	}
	for _, ref := range pin.References {
		item, err := characterImageTx(ctx, tx, g.ActorID, g.ProjectID, g.BranchID, ref.ID)
		if err != nil {
			return err
		}
		if item.Hash != ref.Hash || item.Report.CharacterImage.CharacterID != pin.CharacterID {
			return domain.ErrConflict
		}
	}
	return nil
}

func (s *Generations) VisualSuggestion(ctx context.Context, a domain.Actor, project, branch, id string) (VisualSuggestion, error) {
	var out VisualSuggestion
	g, err := s.Read(ctx, a, project, id)
	if err != nil {
		return out, err
	}
	if g.State != "SUCCEEDED" || g.Cancelled || g.Output == nil || g.BranchID != branch || g.Frozen.Creative == nil || g.Frozen.Creative.Kind != "character_visual" || g.Frozen.Creative.Visual == nil {
		return out, domain.ErrConflict
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if a.ID != g.ActorID {
		return out, domain.ErrForbidden
	}
	if err = s.validateCreativeSourceTx(ctx, tx, g); err != nil {
		return out, err
	}
	var p VisualProposal
	decoder := json.NewDecoder(strings.NewReader(g.Output.Text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || p.CharacterID != g.Frozen.Creative.Visual.CharacterID || p.Look != g.Frozen.Creative.Visual.Look || strings.TrimSpace(p.RenderPrompt) == "" {
		return out, domain.ErrValidation
	}
	// Input and output contracts have already been checked by the provider
	// adapter. Explicitly reject losing an author's locked source feature here.
	var basis struct {
		Character  domain.Entity      `json:"character"`
		Assertions []domain.Assertion `json:"assertions"`
	}
	if json.Unmarshal([]byte(g.Frozen.Creative.Visual.Basis), &basis) != nil {
		return out, domain.ErrNotReady
	}
	for _, item := range basis.Assertions {
		feature := item.Predicate + "：" + item.Value
		if item.Locked && utf8.RuneCountInString(feature) <= 500 && !slices.Contains(p.PreserveFeatures, feature) {
			return out, fmt.Errorf("%w：视觉方案遗漏锁定设定，请修改后重新生成", domain.ErrConflict)
		}
	}
	return VisualSuggestion{GenerationID: g.ID, CharacterHash: g.Frozen.Creative.Visual.CharacterHash, References: g.Frozen.Creative.Visual.References, Proposal: p}, nil
}
