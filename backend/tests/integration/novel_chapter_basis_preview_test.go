package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

type chapterBasisFixture struct {
	b              *browser
	db             *sql.DB
	entry          *service.NovelEntry
	ip             *service.IP
	actor          domain.Actor
	book           service.NovelBook
	detail         service.NovelPlanDraft
	chapters       *service.NovelChapters
	draft          service.NovelChapterDraft
	adopt          func(service.NovelPlanDraft, string) service.NovelPlanDraft
	embeddingCalls *atomic.Int32
}

func newChapterBasisFixture(t *testing.T, extraChapters ...int) chapterBasisFixture {
	t.Helper()
	modelEnvironment(t)
	// Publication needs an index. This deterministic vector fixture exercises
	// the indexing contract without starting a model or claiming semantic quality.
	var embeddingCalls atomic.Int32
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddingCalls.Add(1)
		var input struct {
			Texts []string `json:"texts"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/embed" || json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid isolated embedding request", 400)
			return
		}
		vectors, hashes, tokens := make([][]float32, len(input.Texts)), make([]string, len(input.Texts)), make([]int, len(input.Texts))
		for i, text := range input.Texts {
			vectors[i] = make([]float32, retrieval.Dimension)
			vectors[i][0] = 1
			hash := sha256.Sum256([]byte(text))
			hashes[i], tokens[i] = hex.EncodeToString(hash[:]), 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "BAAI/bge-m3", "revision": retrieval.ModelRevision, "dimension": retrieval.Dimension, "vectors": vectors, "text_hashes": hashes, "token_counts": tokens})
	}))
	t.Cleanup(embedding.Close)
	t.Setenv("STUDIO_EMBEDDING_URL", embedding.URL)
	t.Setenv("STUDIO_EMBEDDING_TOKEN", "basis-preview-isolated-vector-fixture-token")
	ctx := context.Background()
	b, db, entry, actor := novelEntryFixture(t)
	setup := validNovelSetup()
	setup.TargetWords, setup.VolumeCount = 2000, 1
	saved, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: setup})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "basis-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	plans := &service.NovelPlanning{IP: ip}
	open := func(kind, scope string) service.NovelPlanDraft {
		t.Helper()
		r, e := plans.Open(ctx, actor.ID, book.ID, kind, scope)
		if e != nil {
			t.Fatal(e)
		}
		d, e := plans.Read(ctx, actor.ID, book.ID, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	adopt := func(d service.NovelPlanDraft, operation string) service.NovelPlanDraft {
		t.Helper()
		d, e := plans.Save(ctx, actor.ID, book.ID, d.Resource.ID, d)
		if e != nil {
			t.Fatal(e)
		}
		d, e = plans.Basis(ctx, actor.ID, book.ID, d.Resource.ID, d.Revision, nil)
		if e != nil {
			t.Fatal(e)
		}
		d, e = plans.Save(ctx, actor.ID, book.ID, d.Resource.ID, d)
		if e != nil {
			t.Fatal(e)
		}
		r, e := plans.Prepare(ctx, actor.ID, book.ID, d.Resource.ID, d.Revision, d.Hash, operation)
		if e != nil {
			t.Fatalf("prepare fixture %s: %v", operation, e)
		}
		if _, e = plans.Confirm(ctx, actor.ID, book.ID, d.Resource.ID, r.ID, r.Hash, true); e != nil {
			t.Fatalf("confirm fixture %s: %v", operation, e)
		}
		d, e = plans.Read(ctx, actor.ID, book.ID, d.Resource.ID)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	world := open("world", "")
	world.Document.Title, world.Document.Summary = "改稿测试世界", "医者寻找师父"
	world.Document.Mainline, world.Document.Theme = "追查失踪", "责任与真相"
	world.Document.WorldRules, world.Document.Costs, world.Document.Boundaries = "医术不能复活死人", "治病消耗药材", "不能穿越时间"
	world.Document.Setting, world.Document.Factions, world.Document.Characters = "雾城第一年", "药师会", "林舟与师父"
	world.Document.Arcs, world.Document.TurningPoints, world.Document.Ending = "由逃避到负责", "发现师父主动失踪", "救出师父并公开证据"
	adopt(world, "basis-world")
	volumeID, chapterID := service.NewID(), service.NewID()
	volume := open("volume", volumeID)
	volume.Document.Title, volume.Document.Summary, volume.Document.Mainline = "第一卷", "发现旧案", "追查师父"
	volume.Document.Chapters = []novel.PlannedChapter{{ID: chapterID, Title: "雨夜", Summary: "发现线索", Perspective: "林舟", Goal: "查明踪迹", Events: "进入药铺", Change: "开始调查", Transition: "去旧城"}}
	if len(extraChapters) > 0 {
		for i := 0; i < extraChapters[0]; i++ {
			next := volume.Document.Chapters[0]
			next.ID = service.NewID()
			volume.Document.Chapters = append(volume.Document.Chapters, next)
		}
	}
	adopt(volume, "basis-volume")
	outline := open("book", "")
	outline.Document.Title, outline.Document.Summary, outline.Document.Mainline, outline.Document.Theme, outline.Document.Ending = "全书规划", "调查营救", "追查师父", "责任", "公开证据"
	outline.Document.Volumes = []novel.PlannedVolume{{ID: volumeID, Title: "第一卷", Summary: "追查旧案"}}
	adopt(outline, "basis-outline")
	detail := open("detail", chapterID)
	detail.Document.Title, detail.Document.Summary = "雨夜细纲", "原细纲：寻找纸条"
	detail.Document.Scenes = []novel.PlannedScene{{ID: service.NewID(), Time: "第一夜", Place: "药铺", Characters: "林舟", Knowledge: "师父失踪", Goal: "寻找足迹", Conflict: "正门被封", Action: "进入后门", Outcome: "找到纸条", Emotion: "不安", Prerequisites: "听闻失踪", Hook: "署名是自己", Words: 2000}}
	detail = adopt(detail, "basis-detail")
	for _, next := range volume.Document.Chapters[1:] {
		d := open("detail", next.ID)
		d.Document.Title, d.Document.Summary = detail.Document.Title, detail.Document.Summary
		d.Document.Scenes = append([]novel.PlannedScene(nil), detail.Document.Scenes...)
		d.Document.Scenes[0].ID = service.NewID()
		adopt(d, "basis-extra-detail-"+next.ID)
	}
	chapters := &service.NovelChapters{IP: ip}
	resource, err := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := chapters.Read(ctx, actor.ID, book.ID, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Document.Text = "仅属于当前作者的正文，依据预览不得返回此句。"
	draft, err = chapters.Save(ctx, actor.ID, book.ID, resource.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	return chapterBasisFixture{b, db, entry, ip, actor, book, detail, chapters, draft, adopt, &embeddingCalls}
}

func decodeChapterBasis(t *testing.T, value map[string]any) service.NovelChapterBasisPreview {
	t.Helper()
	raw, err := json.Marshal(value)
	var result service.NovelChapterBasisPreview
	if err != nil || json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid chapter basis response", err)
	}
	if strings.Contains(string(raw), "仅属于当前作者的正文") {
		t.Fatal("basis preview returned chapter prose")
	}
	return result
}

func TestNovelChapterBasisPreviewIsReadOnlyVersionedAndScoped(t *testing.T) {
	f := newChapterBasisFixture(t)
	ctx := context.Background()
	path := "/api/novels/" + f.book.ID + "/chapters/" + f.draft.Resource.ID
	counts := func() []int {
		t.Helper()
		values := make([]int, 8)
		for i, table := range []string{"resources", "resource_drafts", "artifact_releases", "audit_events", "outbox", "generation_tasks", "model_calls", "memory_revisions"} {
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&values[i]); err != nil {
				t.Fatal(err)
			}
		}
		return values
	}
	before := counts()
	embeddingsBefore := f.embeddingCalls.Load()
	first := decodeChapterBasis(t, f.b.request("GET", path+"/basis-preview", nil, 200))
	again := decodeChapterBasis(t, f.b.request("GET", path+"/basis-preview", nil, 200))
	if !reflect.DeepEqual(first, again) || !first.CanRefresh || first.Revision != f.draft.Revision || first.Hash != f.draft.Hash || first.ResourceID != f.draft.Resource.ID || first.ChapterID != f.draft.Document.ChapterID || len(first.BasisHash) != 64 || first.Before.Detail == nil || first.After.Detail == nil || len(first.Limitations) != 0 {
		t.Fatal("preview lost exact saved chapter or immutable basis", first)
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("basis GET wrote author, publication, audit, memory, or execution state")
	}
	if f.embeddingCalls.Load() != embeddingsBefore {
		t.Fatal("basis GET performed an embedding request")
	}
	retained, err := f.chapters.Read(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID)
	if err != nil || !reflect.DeepEqual(retained, f.draft) {
		t.Fatal("basis GET modified the saved chapter", err)
	}
	f.detail.Document.Summary = "新细纲：先拜访证人"
	f.detail = f.adopt(f.detail, "basis-detail-second")
	preview := decodeChapterBasis(t, f.b.request("GET", path+"/basis-preview", nil, 200))
	if !preview.CanRefresh || preview.Before.Detail.Summary != "原细纲：寻找纸条" || preview.After.Detail == nil || preview.After.Detail.Summary != "新细纲：先拜访证人" || preview.Before.DetailRef == preview.After.DetailRef || preview.BasisHash == first.BasisHash {
		t.Fatal("did not compare old published detail to current published detail", preview)
	}
	f.detail.Document.Summary = "尚未采用的私人规划，不得作为当前依据"
	f.detail, err = (&service.NovelPlanning{IP: f.ip}).Save(ctx, f.actor.ID, f.book.ID, f.detail.Resource.ID, f.detail)
	if err != nil {
		t.Fatal(err)
	}
	privatePlan := decodeChapterBasis(t, f.b.request("GET", path+"/basis-preview", nil, 200))
	if privatePlan.After.Detail == nil || privatePlan.After.Detail.Summary != "新细纲：先拜访证人" || privatePlan.BasisHash != preview.BasisHash {
		t.Fatal("unadopted private planning changed proposed authority", privatePlan)
	}
	f.detail.Document.Summary = "更新的细纲：证人已离开"
	f.detail = f.adopt(f.detail, "basis-detail-third")
	f.b.request("POST", path+"/basis", map[string]any{"revision": f.draft.Revision, "basis_hash": preview.BasisHash}, 409)
	preview = decodeChapterBasis(t, f.b.request("GET", path+"/basis-preview", nil, 200))
	response := f.b.request("POST", path+"/basis", map[string]any{"revision": f.draft.Revision, "basis_hash": preview.BasisHash}, 200)
	raw, _ := json.Marshal(response)
	var refreshed service.NovelChapterDraft
	if json.Unmarshal(raw, &refreshed) != nil || refreshed.Document.Text != f.draft.Document.Text || refreshed.Document.Detail != preview.After.DetailRef {
		t.Fatal("basis update changed prose or adopted an unseen target")
	}
	refreshed, err = f.chapters.Save(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID, refreshed)
	if err != nil || !refreshed.Ready || refreshed.Document.Text != f.draft.Document.Text {
		t.Fatal("reviewed basis cannot be saved without changing prose", err)
	}
	f.b.request("POST", path+"/basis", map[string]any{"revision": refreshed.Revision, "basis_hash": preview.BasisHash}, 409)
	member := f.b.request("POST", "/api/members", map[string]any{"username": "basis-reader", "password": "basis-reader-password-123", "role": "reader"}, 201)
	reader := &browser{t: t, handler: f.b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "basis-reader", "password": "basis-reader-password-123"}, 200)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader.request("GET", path+"/basis-preview", nil, 403)
	(&browser{t: t, handler: f.b.handler}).request("GET", path+"/basis-preview", nil, 401)
	editorMember := f.b.request("POST", "/api/members", map[string]any{"username": "basis-editor", "password": "basis-editor-password-123", "role": "editor"}, 201)
	editor := &browser{t: t, handler: f.b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "basis-editor", "password": "basis-editor-password-123"}, 200)
	editor.request("GET", path+"/basis-preview", nil, 403)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+editorMember["id"].(string), map[string]any{"role": "editor"}, 200)
	editorPreview := decodeChapterBasis(t, editor.request("GET", path+"/basis-preview", nil, 200))
	if editorPreview.Revision != 0 || editorPreview.Hash == refreshed.Hash || editorPreview.Before.DetailRef != preview.After.DetailRef {
		t.Fatal("another editor inherited the owner's private chapter", editorPreview)
	}
	other, err := f.entry.SaveDraft(ctx, f.actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: f.book.Setup})
	if err != nil {
		t.Fatal(err)
	}
	otherBook, err := f.entry.Confirm(ctx, f.actor.ID, other.ID, service.NovelConfirmation{Revision: other.Revision, OperationID: "basis-other-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", "/api/novels/"+otherBook.ID+"/chapters/"+f.draft.Resource.ID+"/basis-preview", nil, 403)
}

func TestNovelChapterBasisPreviewReadsExactHistoricalAuthority(t *testing.T) {
	f := newChapterBasisFixture(t)
	ctx := context.Background()
	path := "/api/novels/" + f.book.ID + "/chapters/" + f.draft.Resource.ID + "/basis-preview"
	for revision := 2; revision <= 103; revision++ {
		setup := f.book.Setup
		setup.Title = fmt.Sprintf("改稿第%d版书名", revision)
		raw, err := json.Marshal(setup)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if _, err = f.db.Exec("INSERT INTO novel_settings_versions(novel_id,revision,payload,content_hash,created_by) VALUES(?,?,?,?,?)", f.book.ID, revision, raw, hex.EncodeToString(hash[:]), f.actor.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec("UPDATE novels SET settings_revision=103 WHERE id=?", f.book.ID); err != nil {
		t.Fatal(err)
	}
	versions := f.b.request("GET", "/api/novels/"+f.book.ID+"/settings/versions", nil, 200)["items"].([]any)
	if len(versions) != 100 || versions[len(versions)-1].(map[string]any)["revision"].(float64) == 1 {
		t.Fatal("test did not move the original setting outside the list window")
	}
	preview := decodeChapterBasis(t, f.b.request("GET", path, nil, 200))
	if len(preview.SettingsChanges) != 1 || preview.SettingsChanges[0].Field != "title" || preview.SettingsChanges[0].Before != f.book.Setup.Title || preview.Before.SettingsRevision != 1 || preview.After.SettingsRevision != 103 || len(preview.Limitations) != 0 {
		t.Fatal("preview relied on recent settings history instead of exact pin", preview)
	}
	candidate, err := f.ip.Propose(ctx, f.actor, f.book.ProjectID, f.book.BranchID, "", "basis-ip-change", domain.Snapshot{Entities: []domain.Entity{{ID: "basis-character", Kind: "character", Name: "新增人物", Aliases: []string{}, Description: "先前设定中没有的人物"}}, Assertions: []domain.Assertion{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ip.Adopt(ctx, f.actor, f.book.ProjectID, f.book.BranchID, candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ip.Publish(ctx, f.actor, f.book.ProjectID, f.book.BranchID, candidate.ID); err != nil {
		t.Fatal(err)
	}
	preview = decodeChapterBasis(t, f.b.request("GET", path, nil, 200))
	if preview.CanRefresh || preview.Reason == "" || preview.Before.IPHead != "" || preview.After.IPHead != candidate.ID || len(preview.IPChanges) != 1 || preview.IPChanges[0].Name != "新增人物" || preview.Before.Detail == nil || preview.After.Detail == nil {
		t.Fatal("IP change or invalid current planning was hidden", preview)
	}
	// Only the isolated fixture's old immutable detail is made unavailable.
	old, err := f.ip.ReadRelease(ctx, f.actor, f.book.ProjectID, f.detail.Resource.Head)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := old.Manifest.Artifacts[0].Hash
	if err = os.Remove(filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), f.book.ProjectID, oldHash)); err != nil {
		t.Fatal(err)
	}
	// An unavailable old pin is never replaced with the current author draft or
	// presented as an empty, unchanged detail when no valid target is published.
	if _, err = f.db.Exec("UPDATE resources SET head_revision='',content_hash='' WHERE id=?", f.detail.Resource.ID); err != nil {
		t.Fatal(err)
	}
	preview = decodeChapterBasis(t, f.b.request("GET", path, nil, 200))
	if preview.Before.Detail != nil || len(preview.Limitations) == 0 || preview.CanRefresh {
		t.Fatal("missing old detail was presented as unchanged/current", preview)
	}
}
