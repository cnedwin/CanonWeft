package integration

import (
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestStructuredContextLongBookPersistsScopeAndRejectsHidden(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "长篇结构化上下文", "structured")
	bid := branch(b, pid, "正史")
	snapshot := originalSnapshot()
	snapshot.Assertions[0].Locked = true
	snapshot.Assertions = append(snapshot.Assertions, domain.Assertion{ID: "secret", Subject: "lin-1", Predicate: "秘密", Value: "密室入口在树下", Kind: "fact", Visibility: "author", Source: "作者", Locked: true})
	for i := 0; i < 180; i++ {
		id := fmt.Sprintf("extra-%03d", i)
		snapshot.Entities = append(snapshot.Entities, domain.Entity{ID: id, Kind: "character", Name: fmt.Sprintf("配角%d", i)})
		snapshot.Assertions = append(snapshot.Assertions, domain.Assertion{ID: id, Subject: id, Predicate: "前史", Value: strings.Repeat("异地城镇的旧日往事。", 50), Kind: "fact", Visibility: "public", Source: "作者"})
	}
	head := propose(b, pid, bid, "", "structured-ip", snapshot)
	publish(b, pid, bid, head)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	input := service.SearchInput{Query: "林舟", Purpose: "writer", Perspective: "lin-1", StoryTime: 3, Limit: 1, TokenBudget: 16000, Structured: &domain.StructuredScope{FocusEntityIDs: []string{"lin-1"}, RequiredAssertionIDs: []string{"extra-179"}}}
	result := b.request("POST", path+"/search", input, 200)
	raw, _ := json.Marshal(result)
	var pack service.ContextPackage
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Structured == nil || pack.Structured.OmittedAssertions < 170 || pack.EstimatedTokens > input.TokenBudget {
		t.Fatal("unbounded context", pack.Structured)
	}
	found := map[string]bool{}
	for _, a := range pack.Hidden {
		found[a.ID] = true
		if a.ID == "extra-179" && a.Value != snapshot.Assertions[len(snapshot.Assertions)-1].Value {
			t.Fatal("required assertion truncated")
		}
	}
	if !found["identity"] || !found["secret"] || !found["extra-179"] {
		t.Fatal("required constraints missing", found)
	}
	persisted := b.request("GET", path+"/contexts/"+pack.ID, nil, 200)
	if persisted["id"] != pack.ID {
		t.Fatal("context was not persisted")
	}
	input.Purpose = "npc"
	input.Structured.RequiredAssertionIDs = []string{"secret"}
	b.request("POST", path+"/search", input, 403)
	input.Structured.RequiredAssertionIDs = nil
	npc := b.request("POST", path+"/search", input, 200)
	npcRaw, _ := json.Marshal(npc)
	if strings.Contains(string(npcRaw), "密室入口在树下") || len(npc["hidden_constraints"].([]any)) != 0 || len(npc["entities"].([]any)) != 0 {
		t.Fatal("NPC secret leakage")
	}
	if npc["structured_selection"].(map[string]any)["omitted_entities"] != float64(0) {
		t.Fatal("NPC leaked hidden entity counts")
	}
	input.Purpose = "writer"
	input.Structured.RequiredAssertionIDs = []string{"extra-179"}
	input.TokenBudget = 4096
	b.request("POST", path+"/search", input, 409)
	input.Structured.FocusEntityIDs = []string{"lin-1", "lin-1"}
	b.request("POST", path+"/search", input, 400)
	next := propose(b, pid, bid, head, "structured-next", snapshot)
	publish(b, pid, bid, next)
	b.request("GET", path+"/contexts/"+pack.ID, nil, 503)
}

func TestStructuredContextOptionalBackgroundYieldsToRequiredSource(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "容量分配", "structured-budget")
	bid := branch(b, pid, "正史")
	snapshot := originalSnapshot()
	for i := 0; i < 15; i++ {
		snapshot.Assertions = append(snapshot.Assertions, domain.Assertion{ID: fmt.Sprintf("detail-%02d", i), Subject: "lin-1", Predicate: fmt.Sprintf("经历%d", i), Value: strings.Repeat("背景", 40), Kind: "fact", Visibility: "public", Source: "作者"})
	}
	head := propose(b, pid, bid, "", "budget-ip", snapshot)
	publish(b, pid, bid, head)
	rid := resource(b, pid, bid, "必须完整参考的正文")
	body := strings.Repeat("剧情依据不能省略。", 120)
	release := prepareRelease(b, pid, bid, head, "", "budget-source", []map[string]any{{"resource_id": rid, "text": body}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	input := service.SearchInput{Query: "林舟", Purpose: "author", TokenBudget: 12000, Limit: 1, RequiredResourceIDs: []string{rid}}
	result := b.request("POST", path+"/search", input, 200)
	raw, _ := json.Marshal(result)
	var pack service.ContextPackage
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	if len(pack.Required) != 1 || pack.Required[0].Text != body {
		t.Fatal("required text lost")
	}
	if !strings.Contains(strings.Join(pack.Omissions, " "), "容量优先保留") || pack.Structured.SelectedAssertions != pack.Structured.RequiredAssertions {
		t.Fatal("optional structure did not yield", pack.Structured, pack.Omissions)
	}
	b.request("GET", path+"/contexts/"+pack.ID, nil, 200)
}
