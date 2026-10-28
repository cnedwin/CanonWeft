package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/presets"
)

type CapabilityInstall struct {
	RootID   string                 `json:"root_id"`
	Versions []domain.CapabilityRef `json:"versions"`
}

// Stable, versioned operation IDs make interrupted installation resumable and
// repeated installation harmless. Existing edits are never reset to a preset.
func (s *Capabilities) InstallResearch(ctx context.Context, a domain.Actor, project string) (CapabilityInstall, error) {
	result := CapabilityInstall{Versions: []domain.CapabilityRef{}}
	tx, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	tx.Rollback()
	keys := []string{"skill-scout", "skill-analyst", "skill-author", "skill-evaluator"}
	names := []string{"技能发现", "技能分析", "技能适配", "技能评测审阅"}
	roles := []string{"researcher", "researcher", "researcher", "critic"}
	agents := []domain.CapabilityRef{}
	steps := []domain.RecipeStep{}
	var inputSchema, outputSchema json.RawMessage
	for i, key := range keys {
		bundle, readErr := presets.Bundle(key)
		if readErr != nil {
			return result, domain.ErrNotReady
		}
		prefix := "preset.research.v1." + project + "." + key
		pkg, readErr := (&Skills{IP: s.IP, presetStep: prefix + ".package"}).InstallPreset(ctx, a, project, key, prefix+".package")
		if readErr != nil {
			return result, readErr
		}
		inputSchema = bundle.Find("input.schema.json")
		outputSchema = bundle.Find("output.schema.json")
		method := domain.CapabilityDefinition{Kind: "skill", Key: "shared-" + key + "-method", Name: names[i] + "方法", Description: "原始完整方法、契约、例子和反例；固定版本，保留独立评测门。", Instructions: string(bundle.Find("SKILL.md")), InputSchema: inputSchema, OutputSchema: outputSchema, Dependencies: []domain.CapabilityRef{}, PackageID: pkg.ID, PackageHash: pkg.Report.Hash}
		m, readErr := (&Capabilities{IP: s.IP, presetStep: prefix + ".method"}).Save(ctx, a, project, CapabilitySave{OperationID: prefix + ".method", Definition: method})
		if readErr != nil {
			return result, readErr
		}
		methodRef := domain.CapabilityRef{ID: m.ID, Hash: m.Hash, Kind: "skill"}
		result.Versions = append(result.Versions, methodRef)
		agent := domain.CapabilityDefinition{Kind: "agent", Key: "shared-" + key, Name: names[i] + " Agent", Description: "独立上下文中的" + names[i] + "职责；输出待审核候选，不能自授资格。", Instructions: "使用固定依赖中完整的" + names[i] + "方法。只处理本次输入；若缺少真实工具回执或来源，明确记为未知或未运行。保留角色独立性，不能自行发布、消费或改变资格。", InputSchema: inputSchema, OutputSchema: outputSchema, Dependencies: []domain.CapabilityRef{methodRef}, Role: roles[i]}
		v, readErr := (&Capabilities{IP: s.IP, presetStep: prefix + ".agent"}).Save(ctx, a, project, CapabilitySave{OperationID: prefix + ".agent", Definition: agent})
		if readErr != nil {
			return result, readErr
		}
		ref := domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: "agent"}
		agents = append(agents, ref)
		result.Versions = append(result.Versions, ref)
		after := []string{}
		if i > 0 {
			after = append(after, keys[i-1])
		}
		steps = append(steps, domain.RecipeStep{Key: key, AgentID: v.ID, After: after, WaitForConfirmation: true})
	}
	recipe := domain.CapabilityDefinition{Kind: "recipe", Key: "shared-skill-research", Name: "技能研究与适配工作流", Description: "发现、分析、适配、独立审阅；固定版本、逐阶段确认，最多两轮自动修订。", Instructions: "阶段间只交接已确认的候选及来源引用；评测角色只接收固定版本和公开摘要，作者不能取得保留集答案。未完成隔离、工具或模型评测时保持候选，不能生产启用。未知收费请求不自动重试。", InputSchema: inputSchema, OutputSchema: outputSchema, Dependencies: agents, Steps: steps, MaxAutomaticRevisions: 2}
	step := "preset.research.v1." + project + ".recipe"
	v, err := (&Capabilities{IP: s.IP, presetStep: step}).Save(ctx, a, project, CapabilitySave{OperationID: step, Definition: recipe})
	if err != nil {
		return result, err
	}
	result.RootID = v.ID
	result.Versions = append(result.Versions, domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: "recipe"})
	return result, nil
}

// The project row is already locked by IP.begin. Step receipt and immutable
// result commit together, so another authorized member can resume after a crash.
func presetStepTx(ctx context.Context, tx *sql.Tx, project, step, kind, fingerprint string) (string, bool, error) {
	if step == "" {
		return "", false, nil
	}
	var id, storedKind, storedHash string
	err := tx.QueryRowContext(ctx, "SELECT result_id,kind,request_hash FROM capability_preset_steps WHERE project_id=? AND step_key=? FOR UPDATE", project, step).Scan(&id, &storedKind, &storedHash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedKind != kind || storedHash != fingerprint {
		return "", false, domain.ErrConflict
	}
	return id, true, nil
}
func savePresetStepTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, step, kind, fingerprint, id string) error {
	if step == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO capability_preset_steps(project_id,step_key,kind,request_hash,result_id,created_by) VALUES(?,?,?,?,?,?)", project, step, kind, fingerprint, id, a.ID)
	return err
}
