package service

import (
	"context"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/presets"
)

// Built-in editable copies use the same immutable capability/package receipts
// as other presets. Reopening a book never resets the author's edited versions.
func (s *NovelPlanning) InstallAgents(ctx context.Context, actor, bookID string) (CapabilityInstall, error) {
	result := CapabilityInstall{Versions: []domain.CapabilityRef{}}
	tx, a, book, err := s.begin(ctx, actor, bookID, true)
	if err != nil {
		return result, err
	}
	tx.Rollback()
	bundle, err := presets.Bundle("novel-planning")
	if err != nil {
		return result, err
	}
	prefix := "preset.novel-planning.v1." + book.ProjectID
	pkg, err := (&Skills{IP: s.IP, presetStep: prefix + ".package"}).InstallPreset(ctx, a, book.ProjectID, "novel-planning", prefix+".package")
	if err != nil {
		return result, err
	}
	save := func(step string, definition domain.CapabilityDefinition) (domain.CapabilityRef, error) {
		key := prefix + "." + step
		v, e := (&Capabilities{IP: s.IP, presetStep: key}).Save(ctx, a, book.ProjectID, CapabilitySave{OperationID: key, Definition: definition})
		ref := domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: definition.Kind}
		if e == nil {
			result.Versions = append(result.Versions, ref)
		}
		return ref, e
	}
	method, err := save("method", domain.CapabilityDefinition{Kind: "skill", Key: "novel-planning-method", Name: "小说规划协作方法", Description: "世界、全书、分卷和细纲的完整方法与修改契约，可编辑为新版本；安装不代表真实模型质量通过。", Instructions: string(bundle.Find("SKILL.md")), InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{}, PackageID: pkg.ID, PackageHash: pkg.Report.Hash})
	if err != nil {
		return result, err
	}
	for _, kind := range []string{"world", "book", "volume", "detail"} {
		var input map[string]any
		if err = json.Unmarshal(bundle.Find("input.schema.json"), &input); err != nil {
			return result, err
		}
		input["properties"].(map[string]any)["kind"] = map[string]any{"const": kind}
		schema, _ := json.Marshal(input)
		role := "planner"
		if kind == "world" {
			role = "architect"
		}
		_, err = save(kind, domain.CapabilityDefinition{Kind: "agent", Key: "novel-planning-" + kind, Name: novel.PlanKindName(kind) + " Agent", Description: "独立处理当前规划步骤，输入由小说工作台固定，输出待作者审阅的修改建议。", Instructions: "使用固定依赖中的小说规划协作方法，只处理 kind=" + kind + "。保留来源、身份、主题与高光；不执行工具，不确认作品，不自授资格。", InputSchema: schema, OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{method}, Role: role})
		if err != nil {
			return result, err
		}
	}
	result.RootID = method.ID
	return result, nil
}
