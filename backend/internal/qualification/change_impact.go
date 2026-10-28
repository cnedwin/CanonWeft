package qualification

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/presets"
)

// This suite has a separate contract/hash; existing consistency qualifications
// cannot authorize change-impact review through a compatible-looking schema.
func changeImpactSuite() Suite {
	suite := semanticReviewSuite()
	bundle, _ := presets.Bundle("change-impact-review")
	suite.entry.ID, suite.entry.Name = "shared-change-impact-v1", "固定变更影响独立审查"
	suite.entry.TaskKind, suite.entry.Scope = "shared.change-impact", "bounded.change-impact"
	suite.entry.Policy, suite.entry.Provenance = "repository-change-impact-v1", "repository:backend/internal/qualification/change_impact.go"
	suite.entry.InputSchema, suite.entry.OutputSchema = bundle.Find("input.schema.json"), bundle.Find("output.schema.json")
	suite.entry.Description = "固定变更前后与资料的有界影响审查；保留共享一致性反例，增加能力删除、历史时段、时间未知与改编差异。有限测试不证明全书漏检率或文学质量。"
	cases := []struct{ id, change, text, verdict, category string }{
		{"impact-removal", "同一正史分支，能力从第5章起永久失去，无恢复、替代或他人代行。第5章之前可以施火术。", "第6章，林青再次凭自己的该能力施出火术。", "FAIL", "world_rule"},
		{"impact-history", "同一正史分支，能力从第5章起失去；第5章之前可以施火术，不追溯修改过去。", "第2章，林青凭该能力施出火术。", "PASS", ""},
		{"impact-time-unknown", "能力从第5章起失去；当前文段未说明发生在第5章之前还是之后。", "林青凭该能力施出火术。", "UNKNOWN", "unsupported"},
		{"impact-adaptation", "小说正史计划将衣服由青色改为白色；目标漫剧分支已明确批准红色制服为独立造型，不要求随正史服装同步；人物身份相同。", "漫剧镜头中的林青穿着红色制服。", "PASS", ""},
	}
	for _, c := range cases {
		doc := domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: "change_plan", ID: c.id, Hash: Hash([]string{c.change, c.text}), BranchID: "eval", ExpectedIPHead: "base"}, Snapshot: domain.Snapshot{Entities: []domain.Entity{{ID: "lin", Kind: "character", Name: "林青"}}, Assertions: []domain.Assertion{}}, Artifacts: []domain.SemanticReviewArtifact{}, HardFindings: []string{}, RequiredCoverage: []string{"change", "resource:scene"}, Anchors: []domain.SemanticReviewAnchor{{Key: "change", TargetKey: "change", Text: c.change}, {Key: "source", TargetKey: "resource:scene", Text: c.text}}, Scope: domain.ChangeInspectionScope}
		raw, _ := json.Marshal(doc)
		suite.entry.Cases = append(suite.entry.Cases, Case{ID: c.id, Description: c.change, Input: raw, Negative: c.verdict != "PASS"})
		suite.gold[c.id] = heldout{Summary: c.verdict, Plan: c.category}
	}
	return suite
}
