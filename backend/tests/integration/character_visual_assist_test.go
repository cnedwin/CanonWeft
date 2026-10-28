package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
	"time"
)

func assertVisualAssistant(t *testing.T, b *browser, ip *service.IP, a domain.Actor, pid, bid, head, imageID string) string {
	t.Helper()
	ctx := context.Background()
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "本地视觉方案替身", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/asset_designer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	proposal := service.VisualProposal{CharacterID: "lin-1", Look: "青衣造型", VisualBrief: "同一医师、青衣、正右侧背同尺度，左耳银环为待确认造型建议。", RenderPrompt: "水墨武侠风医师，青衣药囊，同尺度正面、右侧面、背面；保持脸型和肤色一致。", PreserveFeatures: []string{"谨慎的青年医师"}, ProposedDetails: []string{"银环为本轮造型建议，未写入人物设定"}, OpenQuestions: []string{"银环是否采用？"}, ConsistencyChecks: []string{"脸型", "发型", "肤色", "身高", "服饰", "道具", "左右位置"}}
	content, _ := json.Marshal(proposal)
	reply, _ := json.Marshal(map[string]any{"id": "visual-receipt", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 100}})
	transport := &generationTransport{response: string(reply)}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	price, err := gen.SavePrice(ctx, a.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := gen.VisualAssistantWorkspace(ctx, a, pid, bid)
	if err != nil || workspace.Project.ID != pid || workspace.Branch.ID != bid {
		t.Fatal("visual workspace", err)
	}
	caps := &service.Capabilities{IP: ip}
	installed, err := caps.InstallNarrative(ctx, a, pid, "character-visual-director")
	if err != nil {
		t.Fatal(err)
	}
	var agent service.CapabilityVersion
	for _, ref := range installed.Versions {
		if ref.Kind == "agent" {
			agent, err = caps.Read(ctx, a, pid, ref.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	agent.Definition.Instructions += "\n作者自定视觉要求：三面鞋底厚度一致。"
	edited, err := caps.Save(ctx, a, pid, service.CapabilitySave{OperationID: "edited-visual-agent", ExpectedHead: agent.ID, Definition: agent.Definition})
	if err != nil {
		t.Fatal(err)
	}
	input := service.VisualAssistInput{ExpectedIPHead: head, CharacterID: "lin-1", Look: "青衣造型", References: []string{imageID}, Notes: "在保留医师身份的前提下，完善青衣和药囊三视图，未知项单列。", PreviousBrief: "上一轮只明确了青衣", AgentVersionID: edited.ID, PriceID: price.ID, MaxOutputTokens: 2000, OperationID: "visual-assist"}
	g, err := gen.PrepareVisualAssistant(ctx, a, pid, bid, input)
	if err != nil {
		t.Fatal("visual prepare", err)
	}
	if _, err = gen.Start(ctx, a, pid, g.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, g.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal("visual execute", state, err)
	}
	result, err := gen.VisualSuggestion(ctx, a, pid, bid, g.ID)
	if err != nil || result.Proposal.RenderPrompt != proposal.RenderPrompt {
		t.Fatal("visual suggestion", err)
	}
	if !strings.Contains(transport.payload, "character-visual-director") || !strings.Contains(transport.payload, "三面鞋底厚度一致") || !strings.Contains(transport.payload, imageID) || !strings.Contains(transport.payload, "并未收到图像像素") {
		t.Fatal("method, editable agent or references missing from actual model input")
	}
	// A structurally valid but wrong character must never become a suggestion.
	input.OperationID = "visual-wrong-character"
	wrong, err := gen.PrepareVisualAssistant(ctx, a, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	transport.response = strings.ReplaceAll(string(reply), "lin-1", "other-person")
	if _, err = gen.Start(ctx, a, pid, wrong.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Execute(ctx, wrong.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.VisualSuggestion(ctx, a, pid, bid, wrong.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("wrong visual identity adopted", err)
	}
	t.Log("Visual assistant: real editable Skill/Agent in model payload, pinned references, private result, wrong identity rejected; 2 local text calls")
	return g.ID
}
