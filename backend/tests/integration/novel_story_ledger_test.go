package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

func storyLedgerDraft(t *testing.T, f chapterBasisFixture) service.NovelLedgerDraft {
	t.Helper()
	ctx := context.Background()
	l, err := f.chapters.OpenLedger(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	l.Document.Summary = "只记已写变化"
	l.Document.Entries = []novel.LedgerEntry{{ID: service.NewID(), Kind: "event", Context: "reality", SubjectName: "同名人物", Predicate: "行动", Statement: "已写的行动", StoryTime: 3, KnownFrom: 5, Visibility: "author", KnownBy: []string{}, Quotes: []string{f.draft.Document.Text}}}
	l, err = f.chapters.SaveLedger(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID, l)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The publication pipeline is covered separately. Supply immutable reviewed
// artifacts directly: these read tests never execute a model or chapter critic.
func storyLedgerPublish(t *testing.T, f chapterBasisFixture, l service.NovelLedgerDraft) string {
	t.Helper()
	ctx := context.Background()
	id := service.NewID()
	ledgerRaw, _ := json.Marshal(l.Document)
	envelope, _ := json.Marshal(struct {
		Schema   int                 `json:"schema"`
		Actor    string              `json:"actor_id"`
		Revision int64               `json:"ledger_revision"`
		Hash     string              `json:"ledger_hash"`
		Base     string              `json:"ledger_base_head"`
		Ledger   novel.ChapterLedger `json:"ledger"`
	}{1, f.actor.ID, l.Revision, editionChapterHash(string(ledgerRaw)), l.BaseHead, l.Document})
	control, _ := json.Marshal(f.draft.Document)
	manifest := domain.Manifest{Schema: 2, ReleaseID: id, BranchID: f.book.BranchID, Artifacts: []domain.ArtifactRef{}}
	for _, item := range []struct {
		id, kind string
		data     []byte
	}{{f.draft.Resource.ID, "novel_chapter_control", control}, {f.draft.Document.BodyID, "novel_chapter_body", []byte(f.draft.Document.Text)}, {l.Resource.ID, "novel_chapter_ledger", envelope}} {
		blob, err := f.ip.Blobs.PutObject(ctx, f.book.ProjectID, bytes.NewReader(item.data))
		if err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, domain.ArtifactRef{ResourceID: item.id, Hash: blob.Hash, Size: blob.Size})
		if _, err = f.db.Exec("UPDATE resources SET head_revision=?,content_hash=?,revision=revision+1 WHERE id=?", id, blob.Hash, item.id); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(manifest)
	if _, err := f.db.Exec("INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,status,created_by) VALUES(?,?,?,?,?,'PUBLISHED',?)", id, f.book.ProjectID, f.book.BranchID, raw, editionChapterHash(string(raw)), f.actor.ID); err != nil {
		t.Fatal(err)
	}
	// Match the real post-publication rebase: content stays exact, private
	// revision/base_head change. These alone must not make the prose source stale.
	for _, resource := range []string{f.draft.Resource.ID, l.Resource.ID} {
		if _, err := f.db.Exec("UPDATE resource_drafts SET base_head=?,revision=revision+1 WHERE resource_id=?", id, resource); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("UPDATE resource_policy_drafts SET revision=revision+1 WHERE resource_id=?", resource); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func storyLedgerPage(t *testing.T, b *browser, path string) service.NovelStoryLedgerPage {
	t.Helper()
	raw, _ := json.Marshal(b.request("GET", path, nil, 200))
	var page service.NovelStoryLedgerPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNovelStoryLedgerSeparatesSourcesAndIsReadOnly(t *testing.T) {
	f := newChapterBasisFixture(t)
	l := storyLedgerDraft(t, f)
	path := "/api/novels/" + f.book.ID + "/story-ledger"
	counts := func() []int {
		values := make([]int, 8)
		for i, table := range []string{"resources", "resource_drafts", "artifact_releases", "audit_events", "outbox", "generation_tasks", "model_calls", "memory_items"} {
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&values[i]); err != nil {
				t.Fatal(err)
			}
		}
		return values
	}
	before, embeddings := counts(), f.embeddingCalls.Load()
	p := storyLedgerPage(t, f.b, path+"?source=working")
	if len(p.Items) != 1 || p.Scanned != 1 || p.NextCursor != "" || p.Items[0].Origin != "private" || !p.Items[0].SourceMatchesCurrent || !p.Items[0].CurrentReady || p.Items[0].ChapterID != f.draft.Document.ChapterID {
		t.Fatal("private exact source", p)
	}
	if p.Items[0].LedgerHash != l.Hash || l.Hash == "" {
		t.Fatal("read/save/aggregate ledger hashes differ")
	}
	if !reflect.DeepEqual(before, counts()) || embeddings != f.embeddingCalls.Load() {
		t.Fatal("GET wrote or called an embedding")
	}
	pub := storyLedgerPage(t, f.b, path+"?source=published")
	if len(pub.Items) != 1 || pub.Items[0].Origin != "unavailable" || len(pub.Items[0].Entries) != 0 {
		t.Fatal("private ledger leaked as published", pub)
	}
	storyLedgerPublish(t, f, l)
	before, embeddings = counts(), f.embeddingCalls.Load()
	pub = storyLedgerPage(t, f.b, path+"?source=published")
	if len(pub.Items) != 1 || pub.Items[0].Origin != "published" || !pub.Items[0].SourceMatchesCurrent || !pub.Items[0].CurrentReady {
		t.Fatal("post-publication normalization falsely marked stale", pub)
	}
	if !reflect.DeepEqual(before, counts()) || embeddings != f.embeddingCalls.Load() {
		t.Fatal("published GET mutated state or dispatched embedding")
	}
	ctx := context.Background()
	var err error
	f.draft, err = f.chapters.Read(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.draft.Document.Text = "后来修改的私人正文。"
	f.draft, err = f.chapters.Save(ctx, f.actor.ID, f.book.ID, f.draft.Resource.ID, f.draft)
	if err != nil {
		t.Fatal(err)
	}
	pub = storyLedgerPage(t, f.b, path+"?source=published")
	if pub.Items[0].SourceMatchesCurrent || pub.Items[0].Entries[0].Quotes[0] == f.draft.Document.Text || pub.Items[0].Origin != "published" {
		t.Fatal("published source mixed with new private text", pub)
	}
	working := storyLedgerPage(t, f.b, path+"?source=working")
	if working.Items[0].Origin != "private" || working.Items[0].SourceMatchesCurrent || working.Items[0].Reason == "" || len(working.Items[0].Entries) != 1 {
		t.Fatal("old private ledger lost or claimed current", working)
	}
	member := f.b.request("POST", "/api/members", map[string]any{"username": "story-ledger-editor", "password": "story-ledger-editor-password", "role": "editor"}, 201)
	other := &browser{t: t, handler: f.b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "story-ledger-editor", "password": "story-ledger-editor-password"}, 200)
	other.request("GET", path+"?source=working", nil, 403)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	if page := storyLedgerPage(t, other, path+"?source=working"); page.Items[0].Origin != "published" {
		t.Fatal("another actor's private ledger leaked", page)
	}
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	other.request("GET", path+"?source=working", nil, 403)
	(&browser{t: t, handler: f.b.handler}).request("GET", path+"?source=published", nil, 401)
	f.b.request("GET", path+"?source=bad", nil, 400)
	f.b.request("GET", path+"?source=working&after=bad", nil, 400)
	// Planning invalidity affects readiness, not access to saved evidence.
	if _, err = f.db.Exec("UPDATE resources SET content_hash=? WHERE id=?", strings.Repeat("0", 64), f.detail.Resource.ID); err != nil {
		t.Fatal(err)
	}
	p = storyLedgerPage(t, f.b, path+"?source=published")
	if len(p.Items) != 1 || p.Items[0].CurrentReady || len(p.Items[0].Entries) != 1 {
		t.Fatal("invalid planning hid saved ledger", p)
	}
	// Names are labels; the two equally named identities remain distinct IDs.
	one, two, head := service.NewID(), service.NewID(), service.NewID()
	snap, _ := json.Marshal(domain.Snapshot{Entities: []domain.Entity{{ID: one, Kind: "character", Name: "同名人物"}, {ID: two, Kind: "character", Name: "同名人物"}}, Assertions: []domain.Assertion{}})
	if _, err = f.db.Exec("INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,status,created_by) VALUES(?,?,'',?,?,'PUBLISHED',?)", head, f.book.BranchID, snap, editionChapterHash(string(snap)), f.actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE branches SET head_revision=? WHERE id=?", head, f.book.BranchID); err != nil {
		t.Fatal(err)
	}
	l.Document.Entries[0].SubjectID = one
	l.Document.Entries[0].ObjectID = two
	raw, _ := json.Marshal(l.Document)
	if _, err = f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", raw, l.Resource.ID, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	p = storyLedgerPage(t, f.b, path+"?source=working")
	if len(p.Items[0].People) != 2 || p.Items[0].People[0].ID == p.Items[0].People[1].ID || p.Items[0].People[0].Name != p.Items[0].People[1].Name {
		t.Fatal("same names collapsed stable entity identity", p)
	}
}

func TestNovelStoryLedgerBoundsCoverageAndRejectsBrokenSources(t *testing.T) {
	f := newChapterBasisFixture(t)
	l := storyLedgerDraft(t, f)
	storyLedgerPublish(t, f, l)
	path := "/api/novels/" + f.book.ID + "/story-ledger?source=published"
	// A changed source hash is visible as unavailable; no unverified entry leaks.
	if _, err := f.db.Exec("UPDATE resources SET content_hash=? WHERE id=?", strings.Repeat("0", 64), f.draft.Document.BodyID); err != nil {
		t.Fatal(err)
	}
	p := storyLedgerPage(t, f.b, path)
	if len(p.Items) != 1 || p.Items[0].Origin != "unavailable" || len(p.Items[0].Entries) != 0 || p.Items[0].Reason == "" {
		t.Fatal("corrupt body accepted", p)
	}
	// Restore the body pointer, then exercise pagination, absent control and size
	// limits without expanding the model/publication fixture.
	if _, err := f.db.Exec("UPDATE resources SET content_hash=? WHERE id=?", editionChapterHash(f.draft.Document.Text), f.draft.Document.BodyID); err != nil {
		t.Fatal(err)
	}
	ids := []string{l.Resource.ID}
	for i := 0; i < 6; i++ {
		id := service.NewID()
		ids = append(ids, id)
		if _, err := f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?, 'novel_chapter_ledger')", id, f.book.ProjectID, f.book.BranchID, "剧情账本 · "+service.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	p = storyLedgerPage(t, f.b, path)
	if p.Scanned != 5 || len(p.Items) != 5 || p.NextCursor == "" {
		t.Fatal("missing bounded first-page coverage", p)
	}
	next := storyLedgerPage(t, f.b, path+"&after="+p.NextCursor)
	if next.Scanned != 2 || len(next.Items) != 2 || next.NextCursor != "" {
		t.Fatal("cursor skipped/duplicated ledger", next)
	}
	seen := map[string]bool{}
	for _, item := range append(p.Items, next.Items...) {
		if seen[item.LedgerID] {
			t.Fatal("duplicate cursor row")
		}
		seen[item.LedgerID] = true
	}
	if len(seen) != len(ids) {
		t.Fatal("silent coverage loss")
	}
	// Explicitly oversized private data must be skipped visibly and advance.
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", strings.Repeat("x", 1100000), l.Resource.ID, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	workingPath := strings.Replace(path, "published", "working", 1)
	cursor := ""
	found := false
	for {
		working := storyLedgerPage(t, f.b, workingPath+"&after="+cursor)
		for _, item := range working.Items {
			if item.LedgerID == l.Resource.ID {
				found = true
				if item.Origin != "unavailable" || len(item.Entries) != 0 || item.Reason == "" {
					t.Fatal("oversize body parsed or returned", item)
				}
			}
		}
		if lenMustJSON(t, working) > 1048576 {
			t.Fatal("page exceeds response byte budget")
		}
		if working.NextCursor == "" {
			break
		}
		cursor = working.NextCursor
	}
	if !found {
		t.Fatal("oversize ledger silently disappeared")
	}
	// An unrelated book's ledger ID cannot be used as a cursor capability.
	other, err := f.entry.SaveDraft(context.Background(), f.actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := f.entry.Confirm(context.Background(), f.actor.ID, other.ID, service.NovelConfirmation{Revision: other.Revision, OperationID: "other-story-ledger", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", "/api/novels/"+book.ID+"/story-ledger?source=working&after="+l.Resource.ID, nil, 403)
	// Individually legal saved ledgers can collectively exceed a response page.
	// Sort these first so this exercises the byte cap, not only the row limit.
	for i := 0; i < 5; i++ {
		ledgerID, controlID, chapterID := fmt.Sprintf("%032x", i+1), service.NewID(), service.NewID()
		chapter := f.draft.Document
		chapter.ChapterID = chapterID
		chapter.BodyID = service.NewID()
		chapterRaw, _ := json.Marshal(chapter)
		doc := l.Document
		doc.ChapterID = chapterID
		doc.BodyID = chapter.BodyID
		doc.Source.ResourceID = controlID
		doc.Source.Hash = editionChapterHash(string(chapterRaw))
		doc.Entries = []novel.LedgerEntry{}
		for j := 0; j < 40; j++ {
			e := l.Document.Entries[0]
			e.ID = service.NewID()
			e.Statement = strings.Repeat("注", 1800)
			doc.Entries = append(doc.Entries, e)
		}
		raw, _ := json.Marshal(doc)
		if len(raw) > 256*1024 {
			t.Fatal("capacity fixture exceeds the real private-save limit")
		}
		for _, r := range []struct{ id, kind, name, body string }{{controlID, "novel_chapter_control", "章节工作稿 · " + chapterID, string(chapterRaw)}, {ledgerID, "novel_chapter_ledger", "剧情账本 · " + chapterID, string(raw)}} {
			if _, err := f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", r.id, f.book.ProjectID, f.book.BranchID, r.name, r.kind); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec("INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body) VALUES(?,?,1,'',?)", r.id, f.actor.ID, r.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	large := storyLedgerPage(t, f.b, workingPath)
	if large.Scanned >= 5 || large.Scanned < 1 || large.NextCursor == "" || lenMustJSON(t, large) > 1048576 {
		t.Fatal("byte-budget continuation missing", large.Scanned, lenMustJSON(t, large))
	}
	if large.Items[0].Origin != "private" || len(large.Items[0].Entries) != 40 {
		t.Fatal("legal large ledger was silently truncated", large.Items[0].Reason)
	}
}

func lenMustJSON(t *testing.T, value any) int {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}
