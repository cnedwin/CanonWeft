//go:build qualification

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
	"os"
	"strings"
	"testing"
	"time"
)

// This is a bounded infrastructure qualification corpus, not a novel-quality
// evaluation. Invoke explicitly with -tags qualification -run TestLongBookIndex.
func TestLongBookIndexCoverageAndRetrieval(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "三十万字索引资格验证", "longbook")
	bid := branch(b, pid, "正史")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	themes := []struct{ Name, Text, Query string }{
		{"山洞寻剑", "剑客沿地下河探索幽暗山洞，在石壁旁寻找遗失的宝剑。水声回荡，他记下每一道岔路和岩层上的剑痕。", "侠客在洞穴寻觅遗失的兵器"},
		{"药师行医", "药师在村落救治发热的病人，辨认草药，记录药量与体温。她逐户检查饮水，研制缓解疫病的新药。", "治疗疾病的草药和诊疗记录"},
		{"港口远航", "船长在海港检修帆船，核对航线和潮汐。他组织水手装载货物，研究风向，准备穿越远海风暴。", "船员在港口准备出海航行"},
		{"古城解谜", "学者在古城图书馆辨认石碑上的失传文字，翻查旧卷宗。他比对王朝年表，寻找密室和机关的线索。", "从古老文献和碑文破解谜题"},
		{"天台观星", "天文学家在高山观测台记录群星的位置，校准望远镜。他计算彗星轨道，推算月食时间并观察星云。", "使用望远镜计算天体运行"},
	}
	texts := map[string][]rune{}
	ids := []string{}
	updates := []map[string]any{}
	for _, theme := range themes {
		id := resource(b, pid, bid, theme.Name)
		ids = append(ids, id)
		var content strings.Builder
		for scene := 1; content.Len() < 210000; scene++ {
			fmt.Fprintf(&content, "第%d段记录。%s这次经历仍有未解之处，需要在后续笔记中核对。\n", scene, theme.Text)
		}
		runes := []rune(content.String())[:60000]
		texts[id] = runes
		updates = append(updates, map[string]any{"resource_id": id, "text": string(runes)})
	}
	release := prepareRelease(b, pid, bid, "", "", "longbook-release", updates)
	releaseAdopt(b, pid, release)
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &service.IndexJobs{Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	job, err := jobs.Create(ctx, actor, pid, bid, service.IndexJobInput{OperationID: "longbook-index", ReleaseID: release})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "studio-longbook-qualification")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	lastDone := -1
	for {
		current, err := jobs.Read(ctx, actor, pid, bid, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.DoneSources != lastDone {
			t.Logf("索引进度：%d/%d 份资料，%d 片段，耗时 %s", current.DoneSources, current.TotalSources, current.Chunks, time.Since(started).Round(time.Millisecond))
			lastDone = current.DoneSources
		}
		if current.State == "READY" {
			job = current
			break
		}
		if current.State != "QUEUED" && current.State != "RUNNING" {
			t.Fatal(current)
		}
		select {
		case <-ctx.Done():
			t.Fatal("qualification timed out")
		case <-time.After(time.Second):
		}
	}
	buildElapsed := time.Since(started)
	rows, err := db.QueryContext(ctx, "SELECT resource_id,start_rune,end_rune,content FROM knowledge_chunks WHERE index_id=? ORDER BY resource_id,start_rune", job.IndexID)
	if err != nil {
		t.Fatal(err)
	}
	coverage := map[string]int{}
	chunks := 0
	for rows.Next() {
		var id, text string
		var begin, end int
		if err = rows.Scan(&id, &begin, &end, &text); err != nil {
			t.Fatal(err)
		}
		original, ok := texts[id]
		if !ok || begin < 0 || begin > coverage[id] || end <= coverage[id] || end > len(original) || text != string(original[begin:end]) {
			t.Fatal("missing, overlapping-only or altered source range", id, begin, end, coverage[id])
		}
		coverage[id] = end
		chunks++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for id, original := range texts {
		if coverage[id] != len(original) {
			t.Fatal("source coverage gap", id, coverage[id], len(original))
		}
	}
	if chunks != job.Chunks || chunks < 600 {
		t.Fatal("unexpected chunk coverage", chunks, job.Chunks)
	}
	publishStarted := time.Now()
	releasePublish(b, pid, release)
	publishElapsed := time.Since(publishStarted)
	type queryEvidence struct {
		Query            string `json:"query"`
		ExpectedResource string `json:"expected_resource"`
		TopResource      string `json:"top_resource"`
		ElapsedMS        int64  `json:"elapsed_ms"`
	}
	queries := []queryEvidence{}
	for i, theme := range themes {
		started := time.Now()
		result, err := jobs.Knowledge.Search(ctx, actor, pid, bid, service.SearchInput{Query: theme.Query, Purpose: "author", Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) == 0 || result.Evidence[0].ResourceID != ids[i] {
			t.Fatal("retrieval did not rank the relevant theme first", theme.Name, result.Evidence)
		}
		queries = append(queries, queryEvidence{theme.Query, theme.Name, result.Evidence[0].Name, time.Since(started).Milliseconds()})
	}
	var calls int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_calls").Scan(&calls); err != nil || calls != 0 {
		t.Fatal("qualification must not call a paid provider", calls, err)
	}
	evidence := struct {
		Schema                      int    `json:"schema"`
		Purpose                     string `json:"purpose"`
		At                          string `json:"at"`
		Model                       string `json:"model_revision"`
		Characters, Sources, Chunks int
		BuildMS                     int64           `json:"build_ms"`
		PublishMS                   int64           `json:"publish_ms"`
		Coverage                    string          `json:"coverage"`
		Queries                     []queryEvidence `json:"queries"`
		PaidCalls                   int             `json:"paid_calls"`
	}{1, "合成中文资料的规模、原文覆盖和主题检索验证；不代表小说质量验收", time.Now().UTC().Format(time.RFC3339), retrieval.ModelRevision, 300000, 5, chunks, buildElapsed.Milliseconds(), publishElapsed.Milliseconds(), "全部原文字符已逐段核对，无缺口或截断", queries, calls}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("STUDIO_QUALIFICATION_REPORT"); path != "" {
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(raw))
}
