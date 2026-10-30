package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

func referenceCounts(t *testing.T, f proseSearchFixture) []int {
	t.Helper()
	out := []int{}
	for _, table := range []string{"resources", "resource_drafts", "artifact_releases", "audit_events", "outbox", "generation_tasks", "model_calls", "memory_items", "operations"} {
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		out = append(out, count)
	}
	return out
}

func referenceObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func referencePlan(t *testing.T, f proseSearchFixture, doc novel.PlanningDocument) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := f.ip.Blobs.PutObject(context.Background(), f.book.ProjectID, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, release := service.NewID(), service.NewID()
	name := novel.PlanKindName(doc.Kind)
	if doc.Scope != "" {
		name += " · " + doc.Scope
	}
	_, err = f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind,head_revision,content_hash) VALUES(?,?,?,?,?,?,?)", id, f.book.ProjectID, f.book.BranchID, name, novel.PlanKind(doc.Kind), release, blob.Hash)
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.Manifest{Schema: 2, ReleaseID: release, BranchID: f.book.BranchID, Artifacts: []domain.ArtifactRef{{ResourceID: id, Hash: blob.Hash, Size: blob.Size}}}
	encoded, _ := json.Marshal(manifest)
	_, err = f.db.Exec("INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,status,created_by) VALUES(?,?,?,?,?,'PUBLISHED',?)", release, f.book.ProjectID, f.book.BranchID, encoded, editionChapterHash(string(encoded)), f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNovelChapterReferenceSourceIsolationAndNoWrites(t *testing.T) {
	f := newProseSearchFixture(t)
	path := "/api/novels/" + f.book.ID + "/chapter-references/" + f.doc.ChapterID
	before := referenceCounts(t, f)
	value := referenceObject(t, f.b.request("GET", path+"?source=working", nil, 200))
	if value["book_id"] != f.book.ID || value["chapter_id"] != f.doc.ChapterID || value["control_id"] != f.control || value["text"] != f.doc.Text || value["origin"] != "private" || value["text_hash"] != editionChapterHash(f.doc.Text) {
		t.Fatal("exact saved private source was not returned", value)
	}
	f.b.request("GET", path+"?source=published", nil, 409)
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("reference read wrote state")
	}
	f.publish(t)
	changed := f.doc
	changed.Text = "作者新私稿，没有覆盖正式原文。"
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=?,revision=2 WHERE resource_id=? AND actor_id=?", mustProseJSON(changed), f.control, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	before = referenceCounts(t, f)
	value = referenceObject(t, f.b.request("GET", path+"?source=published", nil, 200))
	if value["text"] != f.doc.Text || value["origin"] != "published" {
		t.Fatal("private content leaked into adopted source")
	}
	value = referenceObject(t, f.b.request("GET", path+"?source=working", nil, 200))
	if value["text"] != changed.Text || value["revision"] != float64(2) {
		t.Fatal("current actor private draft was not preferred")
	}
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("adopted reference wrote state")
	}
	member := referenceObject(t, f.b.request("POST", "/api/members", map[string]any{"username": "reference-editor", "password": "reference-editor-password", "role": "editor"}, 201))
	editor := &proseSearchClient{t: t, handler: f.b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "reference-editor", "password": "reference-editor-password"}, 200)
	editor.request("GET", path+"?source=working", nil, 403)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	value = referenceObject(t, editor.request("GET", path+"?source=working", nil, 200))
	if value["text"] != f.doc.Text || value["origin"] != "published" {
		t.Fatal("another author's private draft was exposed")
	}
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	editor.request("GET", path+"?source=working", nil, 403)
	(&proseSearchClient{t: t, handler: f.b.handler}).request("GET", path+"?source=working", nil, 401)
	f.b.request("GET", path+"?source=other", nil, 400)
}

func TestNovelChapterReferenceDirectoryWithStalePlanning(t *testing.T) {
	f := newProseSearchFixture(t)
	volume := service.NewID()
	second := service.NewID()
	base := novel.PlanningDocument{Schema: 1, SettingsRevision: 1, SettingsHash: f.doc.SettingsHash, IPHead: f.doc.IPHead, Title: "参考规划", Summary: "旧城主线", Mainline: "寻找线索", Theme: "守诺", Ending: "揭开真相"}
	bookPlan := base
	bookPlan.Kind = "book"
	bookPlan.Volumes = []novel.PlannedVolume{{ID: volume, Title: "旧城卷", Summary: "寻找线索"}}
	referencePlan(t, f, bookPlan)
	volumePlan := base
	volumePlan.Kind = "volume"
	volumePlan.Scope = volume
	volumePlan.Chapters = []novel.PlannedChapter{{ID: f.doc.ChapterID, Title: "先写的一章"}, {ID: second, Title: "尚未写的一章"}}
	referencePlan(t, f, volumePlan)
	before := referenceCounts(t, f)
	// There is deliberately no valid world/detail plan. Stale eligibility must
	// not stop reading the previously adopted directory or saved prose.
	f.b.request("GET", "/api/novels/"+f.book.ID+"/writing-batches/chapters", nil, 409)
	value := referenceObject(t, f.b.request("GET", "/api/novels/"+f.book.ID+"/chapter-references", nil, 200))
	items, ok := value["items"].([]any)
	if !ok || len(items) != 2 || value["book_id"] != f.book.ID || value["complete"] != true {
		t.Fatal("reference directory incomplete", value)
	}
	first := items[0].(map[string]any)
	last := items[1].(map[string]any)
	if first["chapter_id"] != f.doc.ChapterID || first["title"] != "先写的一章" || first["control_id"] != f.control || last["chapter_id"] != second || last["control_id"] != "" {
		t.Fatal("planning order, identity or unwritten state changed")
	}
	f.b.request("GET", "/api/novels/"+f.book.ID+"/chapter-references/"+f.doc.ChapterID+"?source=working", nil, 200)
	f.b.request("GET", "/api/novels/"+f.book.ID+"/chapter-references/"+second+"?source=working", nil, 404)
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("directory created chapter, draft or execution state")
	}
	// A duplicate stable control is never guessed, even when its visible title
	// happens to look identical.
	_, err := f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", service.NewID(), f.book.ProjectID, f.book.BranchID, "章节工作稿 · "+f.doc.ChapterID, "novel_chapter_control")
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", "/api/novels/"+f.book.ID+"/chapter-references/"+f.doc.ChapterID+"?source=working", nil, 409)
}

func TestNovelChapterReferenceRejectsBrokenAndForeignSources(t *testing.T) {
	f := newProseSearchFixture(t)
	path := "/api/novels/" + f.book.ID + "/chapter-references/" + f.doc.ChapterID
	release := f.publish(t)
	if _, err := f.db.Exec("UPDATE artifact_releases SET manifest=? WHERE id=?", `{"schema":2,"artifacts":[]}`, release); err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", path+"?source=published", nil, 409)
	// Both views retain the shared source-policy integrity gate. A private
	// draft is not an authorization bypass around a damaged resource head.
	f.b.request("GET", path+"?source=working", nil, 409)
	f.publish(t)
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", strings.Repeat("x", (512<<10)+1), f.control, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", path+"?source=working", nil, 409)
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", mustProseJSON(f.doc), f.control, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	// A second real book/project cannot use a chapter from this branch.
	saved, err := f.entry.SaveDraft(context.Background(), f.actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.entry.Confirm(context.Background(), f.actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "reference-other-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", "/api/novels/"+other.ID+"/chapter-references/"+f.doc.ChapterID+"?source=working", nil, 404)
}

func TestNovelChapterReferenceDirectoryBoundsAndMissingVolumes(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-volume", true: "bounded-directory"}[bounded], func(t *testing.T) {
			f := newProseSearchFixture(t)
			volume := service.NewID()
			base := novel.PlanningDocument{Schema: 1, SettingsRevision: 1, SettingsHash: f.doc.SettingsHash, IPHead: f.doc.IPHead, Title: "参考", Summary: "保留原顺序"}
			book := base
			book.Kind = "book"
			book.Volumes = []novel.PlannedVolume{{ID: volume, Title: "原分卷"}}
			plan := base
			plan.Kind = "volume"
			plan.Scope = volume
			plan.Chapters = []novel.PlannedChapter{{ID: f.doc.ChapterID, Title: "已存章"}}
			if bounded {
				book.Volumes[0].Title = strings.Repeat("卷", 7000)
				for i := 0; i < 99; i++ {
					plan.Chapters = append(plan.Chapters, novel.PlannedChapter{ID: service.NewID(), Title: "后续章"})
				}
			} else {
				book.Volumes = append(book.Volumes, novel.PlannedVolume{ID: service.NewID(), Title: "不可读卷"})
			}
			referencePlan(t, f, book)
			referencePlan(t, f, plan)
			before := referenceCounts(t, f)
			raw := f.b.request("GET", "/api/novels/"+f.book.ID+"/chapter-references", nil, 200)
			value := referenceObject(t, raw)
			items := value["items"].([]any)
			if value["complete"] != false || len(items) == 0 || len(raw) > 2<<20 {
				t.Fatal("partial directory is unbounded or falsely complete")
			}
			if bounded && len(items) >= 100 {
				t.Fatal("oversized metadata was not bounded")
			}
			if !reflect.DeepEqual(before, referenceCounts(t, f)) {
				t.Fatal("partial directory changed persisted state")
			}
		})
	}
}
