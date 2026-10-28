package integration

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestPromotionCopiesOnlySelectedSemanticItems(t *testing.T) {
	b, _ := setup(t)
	project := createProject(b, "提升提案", "promotion-project")
	parent := branch(b, project, "正史")
	first := propose(b, project, parent, "", "base", originalSnapshot())
	publish(b, project, parent, first)
	child := b.request("POST", "/api/projects/"+project+"/branches", map[string]any{"name": "改编", "operation_id": "fork", "parent_id": parent, "base_revision": first}, 201)["id"].(string)
	changed := originalSnapshot()
	changed.Entities = append(changed.Entities, domain.Entity{ID: "herb", Name: "返照草", Kind: "item"})
	changed.Assertions = append(changed.Assertions, domain.Assertion{ID: "herb-use", Subject: "herb", Predicate: "用途", Value: "缓解寒毒", Kind: "fact", Visibility: "public", Source: "改编设定"}, domain.Assertion{ID: "balance", Subject: "lin-1", Predicate: "游戏伤害", Value: "50", Kind: "hypothesis", Source: "玩法平衡表"})
	source := propose(b, project, child, first, "child", changed)
	publish(b, project, child, source)
	input := map[string]any{"source_branch": child, "source_head": source, "base_head": first, "operation_id": "promote", "reason": "将药草的语义设定复用于正史，不包含游戏数值", "selection": map[string]any{"entities": []string{"herb"}, "assertions": []string{"herb-use"}}}
	path := "/api/projects/" + project + "/branches/" + parent
	input["change_reasons"] = deltaReasons(b.request("POST", path+"/promotion-delta-preview", input, 200))
	candidate := b.request("POST", path+"/promotions", input, 201)
	id := candidate["id"].(string)
	state := b.request("GET", path, nil, 200)
	if state["head"] != first {
		t.Fatal("promotion changed authority before approval")
	}
	// Qualified positive publication lives in the real Generation fixture suite.
	b.request("POST", path+"/candidates/"+id+"/adopt", map[string]any{}, 503)
	snapshot := candidate["snapshot"].(map[string]any)
	if len(snapshot["assertions"].([]any)) != 2 {
		t.Fatal("unselected gameplay data copied")
	}
	if b.request("POST", path+"/promotions", input, 201)["id"] != id {
		t.Fatal("promotion retry duplicated")
	}
	childState := b.request("GET", "/api/projects/"+project+"/branches/"+child, nil, 200)
	if childState["head"] != source {
		t.Fatal("promotion changed child")
	}
}
