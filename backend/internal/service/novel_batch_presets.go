package service

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/presets"
)

func (s *NovelBatch) install(ctx context.Context, actor domain.Actor, project string, count int) (domain.CapabilityRef, domain.CapabilityRef, error) {
	var empty domain.CapabilityRef
	ip := s.Writer.Generations.IP
	bundle, err := presets.Bundle("novel-batch-writer")
	if err != nil {
		return empty, empty, err
	}
	prefix := "preset.novel-batch-writer.v1." + project
	pkg, err := (&Skills{IP: ip, presetStep: prefix + ".package"}).InstallPreset(ctx, actor, project, "novel-batch-writer", prefix+".package")
	if err != nil {
		return empty, empty, err
	}
	save := func(key string, def domain.CapabilityDefinition) (domain.CapabilityRef, error) {
		v, e := (&Capabilities{IP: ip, presetStep: prefix + key}).Save(ctx, actor, project, CapabilitySave{OperationID: prefix + key, Definition: def})
		return domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: def.Kind}, e
	}
	method, err := save(".method", domain.CapabilityDefinition{Kind: "skill", Key: "novel-batch-writer-method", Name: "连续章节与自然文风方法", Description: "完整写作与累计候选状态契约；作者可另存编辑版本。", Instructions: string(bundle.Find("SKILL.md")), InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{}, PackageID: pkg.ID, PackageHash: pkg.Report.Hash})
	if err != nil {
		return empty, empty, err
	}
	agent, err := save(".agent", domain.CapabilityDefinition{Kind: "agent", Key: "novel-batch-writer", Name: "连续章节 Writer", Description: "顺序写作和自然文风自检；输出始终是候选，不能替代独立审稿。", Instructions: "遵循固定方法。previous 是同批前章未采用候选，不是正史。按当前细纲写作，保留状态证据和人物知情边界。", InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{method}, Role: "writer"})
	if err != nil {
		return empty, empty, err
	}
	steps := []domain.RecipeStep{}
	for i := 0; i < count; i++ {
		step := domain.RecipeStep{Key: fmt.Sprintf("chapter-%d", i+1), AgentID: agent.ID, After: []string{}}
		if i > 0 {
			step.After = append(step.After, fmt.Sprintf("chapter-%d", i))
		}
		steps = append(steps, step)
	}
	schema := json.RawMessage(`{"type":"object"}`)
	r, err := save(fmt.Sprintf(".recipe-%d", count), domain.CapabilityDefinition{Kind: "recipe", Key: fmt.Sprintf("novel-batch-%d", count), Name: fmt.Sprintf("连续创作%d章", count), Description: "逐章传递候选状态；停止不自动发布，结果需作者采用。", Instructions: "依已确认细纲顺序执行，尊重费用上限和取消；不并行写相互依赖的章节。", InputSchema: schema, OutputSchema: schema, Dependencies: []domain.CapabilityRef{agent}, Steps: steps})
	return agent, r, err
}
