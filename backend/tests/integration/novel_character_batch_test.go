package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestNovelCharacterBatchContinuation(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup, e := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if e != nil {
		t.Fatal(e)
	}
	book, e := entry.Confirm(ctx, actor.ID, setup.ID, service.NovelConfirmation{Revision: setup.Revision, OperationID: "batch-character-book", AcceptSettings: true})
	if e != nil {
		t.Fatal(e)
	}
	ip := propagationService(t, db)
	transport := &generationTransport{}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	embedder, e := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if e != nil {
		t.Fatal(e)
	}
	svc := &service.NovelCharacters{Generations: gen, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	d, e := svc.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "eleven-characters", Notes: "分段群像测试：十一位城中人", Genre: "悬疑", Count: 11})
	if e != nil {
		t.Fatal(e)
	}
	cid := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "角色分段隔离测试", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/character_designer", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	price, e := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if e != nil {
		t.Fatal(e)
	}
	in := service.CharacterBatchInput{Revision: d.Revision, Hash: d.Hash, Notes: d.Document.Notes, Size: 5, PriceID: price.ID, Currency: "USD", BudgetLimitMicros: 1000000, MaxOutputTokens: 8192, OperationID: "character-batch"}
	run, e := svc.PrepareBatch(ctx, actor.ID, book.ID, d.ResourceID, in)
	if e != nil || len(run.Steps) != 3 {
		t.Fatal("prepare batch", e)
	}
	if again, e := svc.PrepareBatch(ctx, actor.ID, book.ID, d.ResourceID, in); e != nil || again.ID != run.ID {
		t.Fatal("receipt", e)
	}
	changed := in
	changed.Notes = "不同要求"
	if _, e = svc.PrepareBatch(ctx, actor.ID, book.ID, d.ResourceID, changed); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("changed request replay", e)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("prepare called model")
	}
	recipes := &service.RecipeExecutions{Generations: gen}
	if _, e = recipes.Start(ctx, actor, book.ProjectID, run.ID, run.Revision); !errors.Is(e, service.ErrBudgetClosed) {
		t.Fatal("closed budget", e)
	}
	budgets := &service.Budgets{Workspace: entry.Workspace}
	if _, e = budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	run, e = recipes.Start(ctx, actor, book.ProjectID, run.ID, run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	read := func() service.RecipeExecution {
		r, e := recipes.Read(ctx, actor, book.ProjectID, run.ID)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	execute := func(ordinal int, forged bool) service.RecipeStepRun {
		if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "RUNNING" {
			t.Fatal("advance", ordinal, state, e, read().Reason)
		}
		run = read()
		var step service.RecipeStepRun
		for _, s := range run.Steps {
			if s.Active && s.Ordinal == ordinal {
				step = s
			}
		}
		var input struct {
			IDs []string `json:"ids"`
		}
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil || step.GenerationID == "" {
			t.Fatal("missing child")
		}
		p := novel.CharacterProposal{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}, Explanation: fmt.Sprintf("第%d段明确人物职责，继承前段角色关系", ordinal+1), Warnings: []string{}}
		for i, id := range input.IDs {
			if forged && i == 0 {
				id = d.Document.IDs[0]
			}
			p.Entities = append(p.Entities, domain.Entity{ID: id, Kind: "character", Name: fmt.Sprintf("分段人物%d", ordinal*5+i+1), Aliases: []string{}, Description: fmt.Sprintf("第%d段的城中人，掌握独有的线索。", ordinal+1)})
			p.Assertions = append(p.Assertions, domain.Assertion{ID: "occupation", Subject: id, Predicate: "职业", Value: fmt.Sprintf("城中职业%d", ordinal*5+i+1), Kind: "fact", Source: "创作候选", KnownBy: []string{}, Visibility: "author"})
		}
		content, _ := json.Marshal(p)
		response, _ := json.Marshal(map[string]any{"id": "batch-local", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 300, "completion_tokens": 200}})
		transport.response = string(response)
		if state, e := gen.Execute(ctx, step.GenerationID); e != nil || state != "SUCCEEDED" {
			t.Fatal("execute", state, e)
		}
		if ordinal > 0 && !strings.Contains(transport.payload, "分段人物1") {
			t.Fatal("first pass absent from actual prompt")
		}
		if ordinal > 1 && !strings.Contains(transport.payload, "分段人物6") {
			t.Fatal("second pass absent from actual prompt")
		}
		if _, e = recipes.Advance(ctx, run.ID); e != nil {
			t.Fatal("finish pass", e)
		}
		return step
	}
	first := execute(0, false)
	run = read()
	partial, e := svc.BatchReport(ctx, actor.ID, book.ID, d.ResourceID, run.ID)
	if e != nil || partial.Completed != 5 || partial.Suggestion != nil {
		t.Fatal("partial result", e)
	}
	run, e = recipes.Control(ctx, actor, book.ProjectID, run.ID, "pause", run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "PAUSED" {
		t.Fatal("pause", state, e)
	}
	run, e = recipes.Control(ctx, actor, book.ProjectID, run.ID, "resume", run.Revision)
	if e != nil {
		t.Fatal(e)
	}
	bad := execute(1, true)
	run = read()
	if run.State != "FAILED" {
		t.Fatal("wrong identity propagated", run.State)
	}
	run, e = recipes.Retry(ctx, actor, book.ProjectID, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "retry-bad-pass", Step: bad.Frozen.Input})
	if e != nil {
		t.Fatal("retry", e)
	}
	execute(1, false)
	execute(2, false)
	if state, e := recipes.Advance(ctx, run.ID); e != nil || state != "COMPLETED" {
		t.Fatal("finish whole run", state, e)
	}
	run = read()
	if run.Steps[0].ID != first.ID || run.Steps[0].GenerationID != first.GenerationID {
		t.Fatal("paid predecessor repeated")
	}
	report, e := svc.BatchReport(ctx, actor.ID, book.ID, d.ResourceID, run.ID)
	if e != nil || report.Completed != 11 || report.Suggestion == nil {
		t.Fatal("full suggestion", report.Completed, e)
	}
	if len(report.Suggestion.Draft.Document.Candidate.Entities) != len(d.Document.Base.Entities)+11 || len(report.Suggestion.Draft.Document.Candidate.Assertions) != len(d.Document.Base.Assertions)+11 {
		t.Fatal("merge lost identities or short assertion keys collided")
	}
	// A second prepared run must become stale after the author's candidate edit.
	in.OperationID = "pending-before-edit"
	pending, e := svc.PrepareBatch(ctx, actor.ID, book.ID, d.ResourceID, in)
	if e != nil {
		t.Fatal(e)
	}
	budget, e := budgets.Read(ctx, actor.ID)
	if e != nil {
		t.Fatal(e)
	}
	budget.Enabled = false
	if _, e = budgets.Configure(ctx, actor.ID, budget); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("STUDIO_CHARACTER_BATCH_BROWSER_NODE") != "" {
		assertCharacterBatchBrowser(t, ip, book.Setup.Title, d.ResourceID, run.ID)
	} else {
		if _, e = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, report.Suggestion.Draft); e != nil {
			t.Fatal("save merged proposal", e)
		}
	}
	if _, e = recipes.Start(ctx, actor, book.ProjectID, pending.ID, pending.Revision); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale batch started", e)
	}
	history, e := svc.BatchReport(ctx, actor.ID, book.ID, d.ResourceID, run.ID)
	if e != nil || !history.Stale || history.Completed != 11 || history.Suggestion != nil {
		t.Fatal("edited draft erased historical passes", e)
	}
	if transport.calls.Load() != 4 {
		t.Fatal("unexpected call count", transport.calls.Load())
	}
	other := b.request("POST", "/api/members", map[string]any{"username": "batch-character-editor", "password": "batch-character-editor-password", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+other["id"].(string), map[string]any{"role": "editor"}, 200)
	if _, e = svc.BatchReport(ctx, other["id"].(string), book.ID, d.ResourceID, run.ID); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("private batch exposed", e)
	}
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	if _, e = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: blobs}).Scan(ctx, actor.ID, book.ProjectID); e != nil {
		t.Fatal("inventory", e)
	}
	archive := filepath.Join(t.TempDir(), "character-batch.studio")
	if _, e = backup.Create(ctx, db, blobs, archive); e != nil {
		t.Fatal(e)
	}
	cfg, e := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, e := db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); e != nil {
			t.Error(e)
		}
	})
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	if _, e = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: restoredRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); e != nil {
		t.Fatal("restore", e)
	}
	restored, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	for _, step := range run.Steps {
		if !step.Active || step.State != "COMPLETED" {
			continue
		}
		var input, output string
		if e = restored.QueryRow("SELECT input_hash,output_hash FROM recipe_steps WHERE id=?", step.ID).Scan(&input, &output); e != nil || input != step.InputHash || output != step.OutputHash {
			t.Fatal("restored pass pins", e)
		}
		raw, e := (storage.LocalBlobs{Root: restoredRoot}).ReadObject(ctx, book.ProjectID, output)
		if e != nil {
			t.Fatal(e)
		}
		var p novel.CharacterProposal
		if json.Unmarshal(raw, &p) != nil || len(p.Entities) == 0 {
			t.Fatal("restored candidates unreadable")
		}
	}
	var disabled bool
	if e = restored.QueryRow("SELECT restored_disabled FROM recipe_runs WHERE id=?", run.ID).Scan(&disabled); e != nil || !disabled {
		t.Fatal("restored run enabled", e)
	}
}
