package service

import (
	"context"

	"novelstudio/internal/domain"
	"novelstudio/presets"
)

// Preset receipts preserve the original installation without resetting edited heads.
func (s *NovelChapters) InstallAgents(ctx context.Context, actor, bookID, chapter string) (CapabilityInstall, error) {
	return s.installChapterAgents(ctx, actor, bookID, chapter, []string{"writer", "bookkeeper"})
}

func (s *NovelChapters) InstallPolish(ctx context.Context, actor, bookID, chapter string) (CapabilityInstall, error) {
	return s.installChapterAgents(ctx, actor, bookID, chapter, []string{"polish"})
}

func (s *NovelChapters) installChapterAgents(ctx context.Context, actor, bookID, chapter string, roles []string) (CapabilityInstall, error) {
	result := CapabilityInstall{Versions: []domain.CapabilityRef{}}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return result, err
	}
	_, err = s.draftTx(ctx, tx, a, book, chapter)
	tx.Rollback()
	if err != nil {
		return result, err
	}
	for _, role := range roles {
		name := "novel-chapter-" + role
		polish := role == "polish"
		if polish {
			name, role = "fanqie-editorial-polish", "writer"
		}
		bundle, err := presets.Bundle(name)
		if err != nil {
			return result, err
		}
		prefix := "preset." + name + ".v1." + book.ProjectID
		pkg, err := (&Skills{IP: s.IP, presetStep: prefix + ".package"}).InstallPreset(ctx, a, book.ProjectID, name, prefix+".package")
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
		title := "章节写作"
		if polish {
			title = "番茄投稿前润稿"
		}
		if role == "bookkeeper" {
			title = "独立剧情记账"
		}
		method, err := save("method", domain.CapabilityDefinition{Kind: "skill", Key: name + "-method", Name: title + "方法", Description: "完整预置方法，可编辑为新版本；安装不代表真实模型质量通过。", Instructions: string(bundle.Find("SKILL.md")), InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{}, PackageID: pkg.ID, PackageHash: pkg.Report.Hash})
		if err != nil {
			return result, err
		}
		agent, err := save("agent", domain.CapabilityDefinition{Kind: "agent", Key: name, Name: title + " Agent", Description: "使用服务器固定的本章输入，提出待作者审阅的候选。", Instructions: "遵循固定依赖中的" + title + "方法。只使用本次输入允许的资料，保留人物身份和原文证据；不执行工具、不修改权限、不替作者采用或发布。", InputSchema: bundle.Find("input.schema.json"), OutputSchema: bundle.Find("output.schema.json"), Dependencies: []domain.CapabilityRef{method}, Role: role})
		if err != nil {
			return result, err
		}
		if result.RootID == "" {
			result.RootID = agent.ID
		}
	}
	return result, nil
}
