package service

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/presets"
)

func (s *NovelBookReviews) install(ctx context.Context, actor domain.Actor, project string, count int, selected string) (domain.CapabilityRef, domain.CapabilityRef, error) {
	var empty domain.CapabilityRef
	ip := s.Generations.IP
	bundle, err := presets.Bundle("novel-book-review")
	if err != nil {
		return empty, empty, err
	}
	prefix := "preset.novel-book-review.v1." + project
	pkg, err := (&Skills{IP: ip, presetStep: prefix + ".package"}).InstallPreset(ctx, actor, project, "novel-book-review", prefix+".package")
	if err != nil {
		return empty, empty, err
	}
	save := func(key string, def domain.CapabilityDefinition) (domain.CapabilityRef, error) {
		v, e := (&Capabilities{IP: ip, presetStep: prefix + key}).Save(ctx, actor, project, CapabilitySave{OperationID: prefix + key, Definition: def})
		return domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: def.Kind}, e
	}
	method, err := save(".method", domain.CapabilityDefinition{Kind: "skill", Key: "novel-book-review-method", Name: "跨章与整卷审稿方法", Description: "完整原文、六维检查、逐字证据和有界承接状态；可另存编辑。", Instructions: string(bundle.Find("SKILL.md")), InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{}, PackageID: pkg.ID, PackageHash: pkg.Report.Hash})
	if err != nil {
		return empty, empty, err
	}
	agent, err := save(".agent", domain.CapabilityDefinition{Kind: "agent", Key: "novel-book-review", Name: "整卷审稿 Critic", Description: "逐段审读实际章稿，保留早期报告与不确定项，不自动改稿。", Instructions: "按完整方法审读当前固定全文；previous仅为前段候选。所有问题引用原文，不能推测未读正文或承诺平台审核通过。", InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{method}, Role: "critic"})
	if err != nil {
		return empty, empty, err
	}
	if selected != "" {
		v, e := (&Capabilities{IP: ip}).Read(ctx, actor, project, selected)
		if e != nil {
			return empty, empty, e
		}
		if v.Definition.Kind != "agent" || v.Definition.Role != "critic" || capabilityDigest(v.Definition.InputSchema) != capabilityDigest(json.RawMessage(bundle.Find("input.schema.json"))) || capabilityDigest(v.Definition.OutputSchema) != capabilityDigest(json.RawMessage(bundle.Find("output.schema.json"))) {
			return empty, empty, domain.ErrValidation
		}
		agent = domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: "agent"}
	}
	steps := []domain.RecipeStep{}
	for i := 0; i < count; i++ {
		step := domain.RecipeStep{Key: fmt.Sprintf("review-%d", i+1), AgentID: agent.ID, After: []string{}}
		if i > 0 {
			step.After = append(step.After, fmt.Sprintf("review-%d", i))
		}
		steps = append(steps, step)
	}
	schema := json.RawMessage(`{"type":"object"}`)
	key := fmt.Sprintf(".recipe-%d-%s", count, agent.ID)
	r, err := save(key, domain.CapabilityDefinition{Kind: "recipe", Key: fmt.Sprintf("novel-review-%d-%s", count, agent.ID), Name: fmt.Sprintf("整卷审稿·%d段", count), Description: "顺序传递审稿状态，所有分段结果保留；允许暂停和恢复。", Instructions: "完整覆盖指定正文，问题保留原文证据；不自动修改作品或发布。", InputSchema: schema, OutputSchema: schema, Dependencies: []domain.CapabilityRef{agent}, Steps: steps})
	return agent, r, err
}
