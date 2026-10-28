package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestNovelPlanningOrderedConfirmationAndStaleInputs(t *testing.T) {
	runNovelPlanning(t, false)
}

func TestNovelChapterJointPublicationAndMemory(t *testing.T) {
	runNovelPlanning(t, true)
}

func TestNovelChapterResearchReferences(t *testing.T) { runNovelPlanning(t, true) }

func TestNovelTenChapterBatch(t *testing.T)          { runNovelPlanning(t, false, true) }
func TestNovelPolishMethodAndWorkspace(t *testing.T) { runNovelPlanning(t, false, false, true) }
func TestNovelBookExport(t *testing.T)               { runNovelPlanning(t, false, false, false, true) }
func TestNovelBookReview(t *testing.T)               { runNovelPlanning(t, false, false, false, false, true) }

func runNovelPlanning(t *testing.T, publish bool, batchMode ...bool) {
	writingCardHTTP := t.Name() == "TestNovelChapterWritingCardHTTP"
	batch := len(batchMode) == 1 && batchMode[0]
	polish := len(batchMode) == 2 && batchMode[1]
	exportBook := len(batchMode) == 3 && batchMode[2]
	reviewBook := len(batchMode) == 4 && batchMode[3]
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	setup := validNovelSetup()
	setup.TargetWords = 2000
	if batch || reviewBook {
		setup.TargetWords = 20000
	}
	setup.VolumeCount = 1
	saved, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: setup})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "planning-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	plans := &service.NovelPlanning{IP: ip}
	root := "/api/novels/" + book.ID + "/plans"
	gen := &service.Generations{IP: ip}
	if _, err = gen.Prepare(ctx, actor, book.ProjectID, service.GenerationInput{BranchID: book.BranchID, Role: "writer", OperationID: "skip-planning", Request: models.Request{Prompt: "直接写第一章", MaxOutputTokens: 20}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("writer bypassed planning", err)
	}

	open := func(kind, scope string) service.NovelPlanDraft {
		t.Helper()
		r, e := plans.Open(ctx, actor.ID, book.ID, kind, scope)
		if e != nil {
			t.Fatal(e)
		}
		again, e := plans.Open(ctx, actor.ID, book.ID, kind, scope)
		if e != nil || r.ID != again.ID {
			t.Fatal("duplicate plan resource", e)
		}
		d, e := plans.Read(ctx, actor.ID, book.ID, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	save := func(d service.NovelPlanDraft) service.NovelPlanDraft {
		t.Helper()
		v, e := plans.Save(ctx, actor.ID, book.ID, d.Resource.ID, d)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	adopt := func(d service.NovelPlanDraft, op string) service.NovelPlanDraft {
		t.Helper()
		d = save(d)
		next, e := plans.Basis(ctx, actor.ID, book.ID, d.Resource.ID, d.Revision, nil)
		if e != nil {
			t.Fatal(e)
		}
		d = save(next)
		release, e := plans.Prepare(ctx, actor.ID, book.ID, d.Resource.ID, d.Revision, d.Hash, op)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = plans.Confirm(ctx, actor.ID, book.ID, d.Resource.ID, release.ID, release.Hash, false); !errors.Is(e, domain.ErrValidation) {
			t.Fatal("confirmation optional", e)
		}
		confirmed, e := plans.Confirm(ctx, actor.ID, book.ID, d.Resource.ID, release.ID, release.Hash, true)
		if e != nil || confirmed.Status != "PUBLISHED" {
			t.Fatal("confirm plan", e)
		}
		replay, e := plans.Confirm(ctx, actor.ID, book.ID, d.Resource.ID, release.ID, release.Hash, true)
		if e != nil || replay.ID != release.ID {
			t.Fatal("lost reply duplicated plan", e)
		}
		d, e = plans.Read(ctx, actor.ID, book.ID, d.Resource.ID)
		if e != nil || d.BaseHead != release.ID {
			t.Fatal("published private draft base", e)
		}
		return d
	}
	volumeID, chapterID := service.NewID(), service.NewID()
	volume := open("volume", volumeID)
	volume.Document.Title = "第一卷"
	volume.Document.Summary = "发现旧案"
	volume.Document.Mainline = "追查师父"
	volume.Document.Chapters = []novel.PlannedChapter{{ID: chapterID, Title: "雨夜", Summary: "发现线索", Perspective: "林舟", Goal: "查明踪迹", Events: "进入药铺", Change: "开始调查", Transition: "下一步去旧城"}}
	nextChapterID := ""
	if batch || reviewBook {
		for i := 1; i < 10; i++ {
			chapter := volume.Document.Chapters[0]
			chapter.ID = service.NewID()
			chapter.Title = "后续调查"
			volume.Document.Chapters = append(volume.Document.Chapters, chapter)
		}
	}
	if publish || exportBook || writingCardHTTP {
		nextChapterID = service.NewID()
		volume.Document.Chapters = append(volume.Document.Chapters, novel.PlannedChapter{ID: nextChapterID, Title: "旧城", Summary: "追查来信", Perspective: "林舟", Goal: "查明署名", Events: "走访旧城", Change: "发现新证人", Transition: "追查证人"})
	}
	volume = save(volume)
	if _, err = plans.Prepare(ctx, actor.ID, book.ID, volume.Resource.ID, volume.Revision, volume.Hash, "too-early"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("volume bypassed world", err)
	}
	world := open("world", "")
	world.Document.Title = "雾城规划"
	world.Document.Summary = "医者寻找师父"
	world.Document.Mainline = "追查失踪"
	world.Document.Theme = "责任与真相"
	world.Document.WorldRules = "医术不能复活死人"
	world.Document.Costs = "治病消耗药材"
	world.Document.Boundaries = "不能穿越时间"
	world.Document.Setting = "雾城第一年"
	world.Document.Factions = "药师会与巡夜司"
	world.Document.Characters = "林舟与师父"
	world.Document.Arcs = "由逃避到负责"
	world.Document.TurningPoints = "发现师父主动失踪"
	world.Document.Ending = "救出师父并公开证据"
	world = adopt(world, "world-plan")
	volume = adopt(volume, "volume-plan")
	outline := open("book", "")
	outline.Document.Title = "全书规划"
	outline.Document.Summary = "调查与营救"
	outline.Document.Mainline = "追查师父"
	outline.Document.Theme = "责任与真相"
	outline.Document.Ending = "公开证据"
	outline.Document.Volumes = []novel.PlannedVolume{{ID: volumeID, Title: "第一卷", Summary: "追查旧案"}}
	outline = adopt(outline, "book-plan")
	overview, err := plans.Overview(ctx, actor.ID, book.ID)
	if err != nil || overview.Stage != "DETAIL_PLANNING" {
		t.Fatal(overview, err)
	}
	detail := open("detail", chapterID)
	detail.Document.Title = "雨夜细纲"
	detail.Document.Summary = "找到师父留下的纸条"
	detail.Document.Scenes = []novel.PlannedScene{{ID: service.NewID(), Time: "第一夜", Place: "药铺", Characters: "林舟", Knowledge: "只知道师父失踪", Goal: "寻找足迹", Conflict: "正门被封", Action: "进入后门", Outcome: "找到纸条", Emotion: "不安", Prerequisites: "听闻失踪", Hook: "纸条署名是自己", Words: 2000}}
	detail = adopt(detail, "detail-plan")
	if !detail.Ready {
		t.Fatal("confirmed detail not ready", detail.Reason)
	}
	if batch || reviewBook {
		for _, ch := range volume.Document.Chapters[1:] {
			d := open("detail", ch.ID)
			d.Document.Title, d.Document.Summary = "后续细纲", "逐步追查"
			d.Document.Scenes = append([]novel.PlannedScene{}, detail.Document.Scenes...)
			d.Document.Scenes[0].ID = service.NewID()
			adopt(d, "batch-detail-"+ch.ID)
		}
	}
	if publish || writingCardHTTP {
		next := open("detail", nextChapterID)
		next.Document.Title, next.Document.Summary = "旧城细纲", "追查雨夜线索"
		next.Document.Scenes = []novel.PlannedScene{{ID: service.NewID(), Time: "第二日", Place: "旧城", Characters: "林舟", Knowledge: "已经发现纸条", Goal: "追查署名", Conflict: "街巷封闭", Action: "寻找证人", Outcome: "得到新线索", Emotion: "疑惑", Prerequisites: "雨夜停在门外", Hook: "证人认出了他", Words: 2000}}
		if writingCardHTTP {
			next.Document.Summary = "仅下一章可用的蓝匣谜底"
		}
		adopt(next, "second-detail-plan")
	}
	connection := b.request("POST", "/api/models", map[string]any{"provider": "deepseek", "name": "隔离测试连接", "model": "test-only", "base_url": "https://api.deepseek.com", "api_key": "test-only-never-real-key", "enabled": true}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": connection, "connection_revision": 1, "revision": 0}, 200)
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: connection, ConnectionRevision: 1, Currency: "USD", Input: 1, Output: 1, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := gen.Prepare(ctx, actor, book.ProjectID, service.GenerationInput{BranchID: book.BranchID, NovelDetailID: detail.Resource.ID, Role: "writer", PriceID: price.ID, OperationID: "pinned-writer", Request: models.Request{Prompt: "写本章", MaxOutputTokens: 20}})
	if err != nil || task.Frozen.NovelDetail == nil || task.Frozen.NovelDetail.Revision != detail.Resource.Head {
		t.Fatal("prepared writer did not pin detail", err)
	}
	for _, role := range []string{"architect", "planner", "bookkeeper"} {
		b.request("PUT", "/api/model-bindings/"+role, map[string]any{"connection_id": connection, "connection_revision": 1, "revision": 0}, 200)
	}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	assistance := &service.NovelPlanningAssistance{Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}, Generations: gen}
	if writingCardHTTP {
		assertNovelWritingCardHTTP(t, b, gen, actor, book, chapterID, nextChapterID, []service.NovelPlanDraft{world, outline, volume, detail})
	}
	chapterTask, bookkeeperTask := assertNovelChapterWriting(t, gen, assistance.Knowledge, actor, book, price, chapterID, nextChapterID, publish)
	if publish {
		assertNovelEditionAfterPublication(t, b, ip, actor, book)
	}
	if t.Name() == "TestNovelChapterResearchReferences" {
		assertChapterResearchChange(t, gen, assistance.Knowledge, actor, book, price, chapterID)
	}
	if reviewBook {
		b.request("PUT", "/api/model-bindings/critic", map[string]any{"connection_id": connection, "connection_revision": 1, "revision": 0}, 200)
		assertNovelBookReview(t, b, gen, assistance.Knowledge, actor, book, price)
	}
	if batch {
		assertNovelBatch(t, gen, assistance.Knowledge, actor, book, price)
	}
	if polish {
		assertNovelPolish(t, gen, assistance.Knowledge, actor, book, price, chapterID)
	}
	if len(batchMode) == 3 && batchMode[2] {
		assertNovelExport(t, b, ip, actor, book, chapterID)
	}
	for _, current := range []service.NovelPlanDraft{world, outline, volume, detail} {
		current, err = plans.Read(ctx, actor.ID, book.ID, current.Resource.ID)
		if err != nil {
			t.Fatal(err)
		}
		current = save(current)
		prepared, e := assistance.Prepare(ctx, actor.ID, book.ID, current.Resource.ID, service.NovelPlanningAssistInput{Revision: current.Revision, Hash: current.Hash, Notes: "保留现有主线与角色，复核因果是否充分", PriceID: price.ID, MaxOutputTokens: 2048, OperationID: "assist-" + current.Document.Kind})
		if e != nil || prepared.Frozen.ContextID == "" {
			t.Fatal("planning assistant missing stage context", current.Document.Kind, e)
		}
	}
	// The generic release path must enforce the same structural and dependency gate.
	raw, _ := json.Marshal(detail.Document)
	_, err = ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: "", OperationID: "generic-bypass", Updates: []domain.ResourceUpdate{{ResourceID: detail.Resource.ID, ExpectedHead: detail.Resource.Head, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}}})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatal("generic release bypassed planning policy", err)
	}

	revision, err := entry.ReadSettingsDraft(ctx, actor.ID, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision.Setup.Theme = "自由与选择"
	revision, err = entry.SaveSettingsDraft(ctx, actor.ID, book.ID, revision)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := entry.PreviewSettings(ctx, actor.ID, book.ID, revision.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.ConfirmSettings(ctx, actor.ID, book.ID, service.NovelSettingsConfirmation{DraftRevision: revision.Revision, ExpectedRevision: book.SettingsRevision, PreviewHash: preview.Hash, OperationID: "change-planning-theme", AcceptSettings: true}); err != nil {
		t.Fatal(err)
	}
	stale, err := plans.Read(ctx, actor.ID, book.ID, detail.Resource.ID)
	if err != nil || stale.Ready || stale.Document.Summary != detail.Document.Summary {
		t.Fatal("stale detail erased or still usable", stale, err)
	}
	chapterEditor := &service.NovelChapters{IP: ip}
	accessible, err := chapterEditor.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil {
		t.Fatal("stale planning blocked access to existing chapter", err)
	}
	retainedChapter, err := chapterEditor.Read(ctx, actor.ID, book.ID, accessible.ID)
	if err != nil || retainedChapter.Ready || retainedChapter.Document.Text == "" {
		t.Fatal("stale chapter lost editable prose or became ready", err)
	}
	if writingCardHTTP {
		b.request("GET", "/api/novels/"+book.ID+"/chapters/"+accessible.ID+"/writing-card", nil, 409)
		var tasksBefore, callsBefore, tasksAfter, callsAfter int
		if err = db.QueryRow("SELECT (SELECT COUNT(*) FROM generation_tasks),(SELECT COUNT(*) FROM model_calls)").Scan(&tasksBefore, &callsBefore); err != nil {
			t.Fatal(err)
		}
		if _, err = chapterEditor.WritingCard(ctx, actor.ID, book.ID, accessible.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("stale planning produced a writing card", err)
		}
		if err = db.QueryRow("SELECT (SELECT COUNT(*) FROM generation_tasks),(SELECT COUNT(*) FROM model_calls)").Scan(&tasksAfter, &callsAfter); err != nil || tasksAfter != tasksBefore || callsAfter != callsBefore {
			t.Fatal("stale card caused generation side effects", err)
		}
		t.Log("Actual authenticated HTTP/isolated MySQL: chapter-scoped cards pin all four planning versions; reader/cross-book and stale basis rejected; GET creates no task/model call; captured Writer carries full plans/card and Bookkeeper excludes future plans/card. Backup/restore and provider networks were not invoked.")
		return // This focused test deliberately excludes the unrelated backup/restore below.
	}
	reviewQueue, err := (&service.NovelResearch{Generations: gen, Knowledge: assistance.Knowledge}).SourceReview(ctx, actor.ID, book.ID, "")
	if err != nil {
		t.Fatal("planning/chapter review queue", err)
	}
	planListed, chapterListed := false, false
	for _, item := range reviewQueue.Items {
		planListed = planListed || (item.ResourceID == detail.Resource.ID && item.Kind == "planning")
		chapterListed = chapterListed || (item.ResourceID == accessible.ID && item.Kind == "chapter" && item.ChapterID == chapterID)
	}
	if !planListed || !chapterListed {
		t.Fatal("review queue omitted stale planning or chapter", reviewQueue)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("prepared writer started after upstream settings changed", err)
	}
	if _, err = gen.Prepare(ctx, actor, book.ProjectID, service.GenerationInput{BranchID: book.BranchID, NovelDetailID: detail.Resource.ID, Role: "writer", OperationID: "stale-writer", Request: models.Request{Prompt: "写这一章", MaxOutputTokens: 20}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("writer ignored changed planning", err)
	}
	private := stale
	private.Document.Summary = "还未采用的细纲修改，保留旧版正文依据"
	private = save(private)
	stale.Document.Summary = "迟到的另一窗口修改"
	if _, err = plans.Save(ctx, actor.ID, book.ID, detail.Resource.ID, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("late private draft overwrote current work", err)
	}
	other := b.request("POST", "/api/members", map[string]any{"username": "plan-editor", "password": "planning-editor-password", "role": "editor"}, 201)
	editorID := other["id"].(string)
	if _, err = plans.Read(ctx, editorID, book.ID, detail.Resource.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("nonmember read book plan", err)
	}
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+editorID, map[string]any{"role": "editor"}, 200)
	otherDraft, err := plans.Read(ctx, editorID, book.ID, detail.Resource.ID)
	if err != nil || otherDraft.Revision != 0 || otherDraft.Document.Summary != detail.Document.Summary {
		t.Fatal("another editor saw private planning", err)
	}
	archive := filepath.Join(t.TempDir(), "plans.studio")
	if _, err = backup.Create(ctx, db, ip.Blobs, archive); err != nil {
		t.Fatal("planning broke backup", err)
	}
	assertNovelPlanningRestore(t, db, archive, private, task, chapterTask, bookkeeperTask)
	b.request("GET", root, nil, 200)
	var calls int
	if err = db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&calls); err != nil || (!publish && !batch && !polish && !reviewBook && calls != 2) || (reviewBook && calls != 5) || (polish && calls != 3) || (batch && calls != 12) || (publish && calls < 3) {
		t.Fatal("expected only test-only writer and bookkeeper transport calls", err)
	}
}

func assertNovelPlanningRestore(t *testing.T, db *sql.DB, archive string, private service.NovelPlanDraft, task, chapterTask, bookkeeperTask service.Generation) {
	t.Helper()
	ctx := context.Background()
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	blobs := storage.LocalBlobs{Root: filepath.Join(t.TempDir(), "restored")}
	_, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobs.Root, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}})
	if err != nil {
		t.Fatal("restore planning archive", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var body, head, hash string
	var revision int64
	if err = restored.QueryRow("SELECT body,revision FROM resource_drafts WHERE resource_id=? AND actor_id=?", private.Resource.ID, task.ActorID).Scan(&body, &revision); err != nil || revision != private.Revision {
		t.Fatal("planning private draft version lost", err)
	}
	var doc novel.PlanningDocument
	if err = json.Unmarshal([]byte(body), &doc); err != nil || doc.Summary != private.Document.Summary {
		t.Fatal("planning private draft text lost", err)
	}
	if err = restored.QueryRow("SELECT head_revision,content_hash FROM resources WHERE id=?", private.Resource.ID).Scan(&head, &hash); err != nil || head != private.Resource.Head || hash != private.Resource.Hash {
		t.Fatal("published plan reference lost", err)
	}
	raw, err := blobs.ReadObject(ctx, task.ProjectID, hash)
	if err != nil {
		t.Fatal("published planning object lost", err)
	}
	if err = json.Unmarshal(raw, &doc); err != nil || doc.Summary != private.Published.Summary {
		t.Fatal("private planning replaced formal plan during restore", err)
	}
	if err = restored.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var frozen service.GenerationFrozen
	if err = json.Unmarshal([]byte(body), &frozen); err != nil || frozen.NovelDetail == nil || *frozen.NovelDetail != *task.Frozen.NovelDetail {
		t.Fatal("writer detail pin lost in restore", err)
	}
	var enabled int
	if err = restored.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", bookkeeperTask.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var restoredBookkeeper service.GenerationFrozen
	if err = json.Unmarshal([]byte(body), &restoredBookkeeper); err != nil || restoredBookkeeper.NovelChapter == nil || *restoredBookkeeper.NovelChapter != *bookkeeperTask.Frozen.NovelChapter {
		t.Fatal("bookkeeper source pin lost in restore", err)
	}
	var ledgerBefore, ledgerAfter string
	query := "SELECT d.body FROM resource_drafts d JOIN resources r ON r.id=d.resource_id WHERE r.project_id=? AND r.kind='novel_chapter_ledger' AND d.actor_id=?"
	if err = db.QueryRow(query, task.ProjectID, task.ActorID).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow(query, task.ProjectID, task.ActorID).Scan(&ledgerAfter); err != nil || ledgerAfter != ledgerBefore {
		t.Fatal("private chapter ledger lost in restore", err)
	}
	rows, err := db.Query("SELECT id,document FROM memory_items WHERE project_id=? ORDER BY id", task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var id, original, copied string
		if err = rows.Scan(&id, &original); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = restored.QueryRow("SELECT document FROM memory_items WHERE id=?", id).Scan(&copied); err != nil || original != copied {
			rows.Close()
			t.Fatal("chapter memory changed on restore", err)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	var restoredCount int
	if err = restored.QueryRow("SELECT COUNT(*) FROM memory_items WHERE project_id=?", task.ProjectID).Scan(&restoredCount); err != nil || restoredCount != count {
		t.Fatal("memory restore count", err)
	}
	if err = restored.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", chapterTask.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var chapterFrozen service.GenerationFrozen
	if err = json.Unmarshal([]byte(body), &chapterFrozen); err != nil || chapterFrozen.NovelChapter == nil || *chapterFrozen.NovelChapter != *chapterTask.Frozen.NovelChapter {
		t.Fatal("chapter private source pin lost in restore", err)
	}
	var originalBody, restoredBody string
	if err = db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", chapterFrozen.NovelChapter.ResourceID, task.ActorID).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", chapterFrozen.NovelChapter.ResourceID, task.ActorID).Scan(&restoredBody); err != nil || restoredBody != originalBody {
		t.Fatal("chapter text lost in restore", err)
	}
	if err = restored.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=TRUE").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("restore enabled execution identities", err)
	}
	assertBookReviewRestore(t, db, restored, blobs, task.ProjectID)
	assertNovelEditionRestore(t, db, restored, blobs, task.ProjectID)
}
