package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
	"time"
)

func assertNovelVisualAssistant(t *testing.T, b *browser, ip *service.IP, a domain.Actor, book, pid, bid, head string, target *models.NovelImageTarget, imageID string) string {
	t.Helper()
	ctx := context.Background()
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "本地小说视觉方案替身", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/asset_designer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	proposal := map[string]any{"novel": target, "look": target.Name, "visual_brief": "雨夜钟楼与石桥，顶部留书名空间。", "render_prompt": "钟楼东侧石桥，午夜雨水反光，暖光窗口；顶部留白。", "preserve_features": []string{"钟楼与石桥"}, "proposed_details": []string{"暖光色温为视觉建议"}, "open_questions": []string{"是否保留远处人影？"}, "consistency_checks": []string{"主线氛围", "构图留白"}}
	content, _ := json.Marshal(proposal)
	reply, _ := json.Marshal(map[string]any{"id": "novel-visual-receipt", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 100}})
	transport := &generationTransport{response: string(reply)}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	price, err := gen.SavePrice(ctx, a.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.NovelImages{Generations: gen}
	b.request("POST", "/api/novels/"+book+"/images/assistant", map[string]any{}, 200)
	input := service.VisualAssistInput{Novel: target, ExpectedIPHead: head, References: []string{imageID}, Notes: "保留钟楼石桥与主题，完善封面", PreviousBrief: "上一轮留下了书名空间", PriceID: price.ID, MaxOutputTokens: 2000, OperationID: "novel-visual-assist"}
	if _, err = svc.Assist(ctx, a.ID, book, input); err != nil {
		t.Fatal("prepare novel visual", err)
	}
	prepared := b.request("POST", "/api/novels/"+book+"/images/assist", input, 201)
	task := prepared["id"].(string)
	if _, err = gen.Start(ctx, a, pid, task); err != nil {
		t.Fatal("novel visual start", err)
	}
	if state, e := gen.Execute(ctx, task); e != nil || state != "SUCCEEDED" {
		t.Fatal("novel visual result", state, e)
	}
	suggestion, err := svc.Suggestion(ctx, a.ID, book, task)
	if err != nil || suggestion.Proposal.Novel.BookID != book {
		t.Fatal("novel visual suggestion", err)
	}
	if !strings.Contains(transport.payload, "全文末尾必须保留的钟楼标记") || !strings.Contains(transport.payload, "封面") || !strings.Contains(transport.payload, "上一轮留下了书名空间") {
		t.Fatal("novel visual context")
	}
	b.request("GET", "/api/projects/"+pid+"/generations?branch="+bid+"&creative=novel_visual&creative_target="+book, nil, 200)
	return task
}
