package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"

	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func historyPath(f proseSearchFixture) string {
	return "/api/novels/" + f.book.ID + "/chapters/" + f.control + "/history"
}

func TestNovelChapterHistoryPaginationAndSaveRollback(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	svc := &service.NovelChapters{IP: f.ip}
	saved := referenceObject(t, f.b.request("POST", historyPath(f), historyInput(t, f, "page-"+service.NewID(), "分页种子"), 201))
	var body string
	if err := f.db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", saved["id"], f.actor.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 31; i++ {
		var doc service.ChapterHistoryDocument
		if json.Unmarshal([]byte(body), &doc) != nil {
			t.Fatal("seed")
		}
		id := service.NewID()
		doc.ID = id
		doc.OperationID = ""
		raw, _ := json.Marshal(doc)
		if _, err := f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind,created_at) VALUES(?,?,?,?,?,UTC_TIMESTAMP(6))", id, f.book.ProjectID, f.book.BranchID, "章节修订 · "+f.doc.ChapterID+" · "+id, "novel_chapter_history"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body) VALUES(?,?,1,'',?)", id, f.actor.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	first := referenceObject(t, f.b.request("GET", historyPath(f), nil, 200))
	next := first["next"].(string)
	if len(first["items"].([]any)) != 30 || next == "" {
		t.Fatal("unbounded first history page")
	}
	second := referenceObject(t, f.b.request("GET", historyPath(f)+"?after="+next, nil, 200))
	if len(second["items"].([]any)) != 2 || second["next"] != "" {
		t.Fatal("history cursor skipped entries")
	}
	seen := map[string]bool{}
	for _, page := range []map[string]any{first, second} {
		for _, raw := range page["items"].([]any) {
			m := raw.(map[string]any)
			id := m["id"].(string)
			if seen[id] {
				t.Fatal("duplicate page item")
			}
			seen[id] = true
			if _, ok := m["text"]; ok {
				t.Fatal("pagination loaded bodies")
			}
		}
	}
	d, err := svc.Read(ctx, f.actor.ID, f.book.ID, f.control)
	if err != nil {
		t.Fatal(err)
	}
	d.Document.Text = "需要被保护的最新正文。"
	d, err = svc.Save(ctx, f.actor.ID, f.book.ID, f.control, d)
	if err != nil {
		t.Fatal(err)
	}
	before := referenceCounts(t, f)
	if _, err = f.db.Exec("CREATE TRIGGER history_save_fault BEFORE UPDATE ON resource_drafts FOR EACH ROW BEGIN IF NEW.resource_id='" + f.control + "' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated chapter save fault'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	d.Document.Text = "不得保存的正文。"
	d.HistoryReason = "before_restore"
	if _, err = svc.Save(ctx, f.actor.ID, f.book.ID, f.control, d); err == nil {
		t.Fatal("save fault was not reached")
	}
	if _, err = f.db.Exec("DROP TRIGGER history_save_fault"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("failed prose save left partial history")
	}
	got, err := svc.Read(ctx, f.actor.ID, f.book.ID, f.control)
	if err != nil || got.Document.Text != "需要被保护的最新正文。" {
		t.Fatal("failed save overwrote prior manuscript", err)
	}
	tooLong := historyInput(t, f, "large-note-"+service.NewID(), strings.Repeat("字", 121))
	f.b.request("POST", historyPath(f), tooLong, 400)
	nextDocument := got.Document
	nextDocument.Text = "来自另一候选的正文。"
	nextDocument.WriterGenerationID = service.NewID()
	encoded, _ := json.Marshal(nextDocument)
	if _, err = f.ip.SaveResourceDraft(ctx, f.actor, f.book.ProjectID, f.control, service.ResourceDraft{Revision: got.Revision, BaseHead: got.BaseHead, Text: string(encoded)}); err != nil {
		t.Fatal(err)
	}
	newest := historyItems(t, f)[0].(map[string]any)
	if newest["kind"] != "before_replace" {
		t.Fatal("new writer candidate bypassed critical pre-change checkpoint")
	}
}
func newHistoryFixture(t *testing.T) proseSearchFixture {
	t.Helper()
	// Reuse the snapshot suite's UTC session contract; never change server globals.
	if admin := os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"); admin != "" {
		cfg, err := mysql.ParseDSN(admin)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params["time_zone"] = "'+00:00'"
		t.Setenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN", cfg.FormatDSN())
	}
	return newProseSearchFixture(t)
}
func historyInput(t *testing.T, f proseSearchFixture, op, note string) map[string]any {
	t.Helper()
	current, err := (&service.NovelChapters{IP: f.ip}).Read(context.Background(), f.actor.ID, f.book.ID, f.control)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"revision": current.Revision, "hash": current.Hash, "operation_id": op, "note": note}
}
func historyItems(t *testing.T, f proseSearchFixture) []any {
	t.Helper()
	return referenceObject(t, f.b.request("GET", historyPath(f), nil, 200))["items"].([]any)
}
func TestNovelChapterHistoryAutoManualAndReadOnly(t *testing.T) {
	f := newHistoryFixture(t)
	before := referenceCounts(t, f)
	if len(historyItems(t, f)) != 0 || !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("history list must not create snapshots")
	}
	svc := &service.NovelChapters{IP: f.ip}
	d, err := svc.Read(context.Background(), f.actor.ID, f.book.ID, f.control)
	if err != nil {
		t.Fatal(err)
	}
	d.Document.Text += "第一轮精修。"
	d, err = svc.Save(context.Background(), f.actor.ID, f.book.ID, f.control, d)
	if err != nil {
		t.Fatal(err)
	}
	items := historyItems(t, f)
	if len(items) != 1 {
		t.Fatal("old prose not automatically checkpointed", len(items))
	}
	old := items[0].(map[string]any)
	if _, ok := old["text"]; ok {
		t.Fatal("list loads full prose")
	}
	got := referenceObject(t, f.b.request("GET", historyPath(f)+"/"+old["id"].(string), nil, 200))
	if got["text"] != f.doc.Text || got["text_hash"] != editionChapterHash(f.doc.Text) {
		t.Fatal("raw prose changed", got)
	}
	d.Document.Text += "第二轮精修。"
	if _, err = svc.Save(context.Background(), f.actor.ID, f.book.ID, f.control, d); err != nil {
		t.Fatal(err)
	}
	if len(historyItems(t, f)) != 1 {
		t.Fatal("rapid autosaves were not coalesced")
	}
	current, err := svc.Read(context.Background(), f.actor.ID, f.book.ID, f.control)
	if err != nil {
		t.Fatal(err)
	}
	priorText := current.Document.Text
	current.Document.Text = "取回旧正文后的一版。"
	current.HistoryReason = "before_restore"
	if _, err = svc.Save(context.Background(), f.actor.ID, f.book.ID, f.control, current); err != nil {
		t.Fatal(err)
	}
	forced := historyItems(t, f)
	if len(forced) != 2 || forced[0].(map[string]any)["kind"] != "before_restore" {
		t.Fatal("destructive take-back failed to preserve previous draft in same save")
	}
	forceText := referenceObject(t, f.b.request("GET", historyPath(f)+"/"+forced[0].(map[string]any)["id"].(string), nil, 200))
	if forceText["text"] != priorText {
		t.Fatal("pre-take-back checkpoint captured new, not previous prose")
	}
	in := historyInput(t, f, "history-"+service.NewID(), "改动前留版")
	saved := referenceObject(t, f.b.request("POST", historyPath(f), in, 201))
	retry := referenceObject(t, f.b.request("POST", historyPath(f), in, 201))
	if saved["id"] != retry["id"] || saved["kind"] != "manual" || saved["note"] != "改动前留版" {
		t.Fatal("checkpoint receipt not stable", saved, retry)
	}
	before = referenceCounts(t, f)
	f.b.request("GET", historyPath(f)+"?operation_id="+in["operation_id"].(string), nil, 200)
	f.b.request("GET", historyPath(f)+"/"+saved["id"].(string), nil, 200)
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("history reads wrote state")
	}
	in["note"] = "different request"
	f.b.request("POST", historyPath(f), in, 409)
	stale := historyInput(t, f, "stale-"+service.NewID(), "")
	stale["revision"] = float64(0)
	f.b.request("POST", historyPath(f), stale, 409)
}

func TestNovelChapterHistoryRetentionBackupRestoreAndProjectCopy(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	saved := referenceObject(t, f.b.request("POST", historyPath(f), historyInput(t, f, "archive-"+service.NewID(), "备份前修订🌙"), 201))
	id := saved["id"].(string)
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Texts []string `json:"texts"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("embedding fixture request")
			w.WriteHeader(400)
			return
		}
		vectors, hashes, tokens := make([][]float32, len(in.Texts)), make([]string, len(in.Texts)), make([]int, len(in.Texts))
		for i := range vectors {
			vectors[i] = make([]float32, 1024)
			vectors[i][0] = 1
			hash := sha256.Sum256([]byte(in.Texts[i]))
			hashes[i] = hex.EncodeToString(hash[:])
			tokens[i] = 1
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"model": "BAAI/bge-m3", "revision": retrieval.ModelRevision, "dimension": retrieval.Dimension, "vectors": vectors, "text_hashes": hashes, "token_counts": tokens})
	}))
	defer embedding.Close()
	t.Setenv("STUDIO_EMBEDDING_URL", embedding.URL)
	t.Setenv("STUDIO_EMBEDDING_TOKEN", "chapter-history-isolated-vector-fixture-token")
	embedder, err := retrieval.NewEmbedder(embedding.URL, "chapter-history-isolated-vector-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	k := &service.Knowledge{IP: f.ip, Embedder: embedder}
	if _, err := k.Build(ctx, f.actor, f.book.ProjectID, f.book.BranchID); err != nil {
		t.Fatal("isolated source index", err)
	}
	choices, err := k.WorkingChoices(ctx, f.actor, f.book.ProjectID, f.book.BranchID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range choices.Items {
		if choice.Ref.ID == id {
			t.Fatal("history offered for automatic context")
		}
	}
	if _, err := k.Search(ctx, f.actor, f.book.ProjectID, f.book.BranchID, service.SearchInput{Purpose: "critic", Query: "核对", IncludeWorking: true, Limit: 8, TokenBudget: 65536, WorkingRefs: []domain.WorkingRef{{Kind: "resource_draft", ID: id, Version: "1"}}}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("history may enter explicit model context", err)
	}
	if _, err := f.ip.PrepareRelease(ctx, f.actor, f.book.ProjectID, domain.ReleaseInput{BranchID: f.book.BranchID, OperationID: "history-publish-rejected", Updates: []domain.ResourceUpdate{{ResourceID: id, Text: "不得发布历史"}}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("history may publish", err)
	}
	scanner := &service.RetentionScanner{Workspace: f.ip.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if _, err := scanner.Scan(ctx, f.actor.ID, f.book.ProjectID); err != nil {
		tx, _ := f.db.BeginTx(ctx, nil)
		diagnostic, e := retention.RegisterSourceObservations(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, f.book.ProjectID, f.ip.Blobs.ReadObject)
		tx.Rollback()
		t.Fatal("history retention/source inventory", err, e, diagnostic.UnresolvedReferences, diagnostic.UnboundSources)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "chapter-history.studio")
	if _, err := backup.Create(ctx, f.db, f.ip.Blobs, archive); err != nil {
		t.Fatal("history backup", err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := file.Stat()
	_, err = backup.ValidateArchive(file, stat.Size())
	file.Close()
	if err != nil {
		t.Fatal("archive identity", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := f.db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	_, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(root, "restored"), Authority: backup.ProtectedAuthority{DB: f.db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}})
	if err != nil {
		t.Fatal("isolated history restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredSvc := &service.NovelChapters{IP: &service.IP{Workspace: &service.Workspace{DB: restored}, Blobs: storage.LocalBlobs{Root: filepath.Join(root, "restored")}}}
	got, err := restoredSvc.HistoryText(ctx, f.actor.ID, f.book.ID, f.control, id)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("isolated restore silently reactivated the archived author", err)
	}
	var originalBody, restoredBody string
	if err = f.db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, f.actor.ID).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, f.actor.ID).Scan(&restoredBody); err != nil {
		t.Fatal(err)
	}
	if originalBody != restoredBody || json.Unmarshal([]byte(restoredBody), &got) != nil || got.Text != f.doc.Text || got.TextHash != editionChapterHash(f.doc.Text) || got.Note != "备份前修订🌙" {
		t.Fatal("restored history changed exact saved content")
	}
	var name string
	if err = f.db.QueryRow("SELECT name FROM projects WHERE id=?", f.book.ProjectID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	f.b.request("DELETE", "/api/projects/"+f.book.ProjectID, map[string]any{"confirm_name": name, "revision": 1, "operation_id": "history-delete-source"}, 200)
	copied, err := f.ip.RecoverProject(ctx, f.actor.ID, f.book.ProjectID, service.ProjectRecoveryInput{Name: "历史恢复副本", Confirmation: name, Revision: 2, OperationID: "history-project-copy"})
	if err != nil {
		t.Fatal("project copy with history", err)
	}
	var bookID, controlID, historyID string
	if err = f.db.QueryRow("SELECT id FROM novels WHERE project_id=?", copied.ID).Scan(&bookID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT id FROM resources WHERE project_id=? AND kind='novel_chapter_control'", copied.ID).Scan(&controlID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT id FROM resources WHERE project_id=? AND kind='novel_chapter_history'", copied.ID).Scan(&historyID); err != nil {
		t.Fatal(err)
	}
	got, err = (&service.NovelChapters{IP: f.ip}).HistoryText(ctx, f.actor.ID, bookID, controlID, historyID)
	if err != nil || got.Text != f.doc.Text || got.Note != "备份前修订🌙" || historyID == id {
		t.Fatal("project copy did not preserve history with rebound physical identity", err)
	}
}

func TestNovelChapterHistoryPrivateImmutableAndIntegrity(t *testing.T) {
	f := newHistoryFixture(t)
	in := historyInput(t, f, "checkpoint-"+service.NewID(), "私人备注")
	saved := referenceObject(t, f.b.request("POST", historyPath(f), in, 201))
	id := saved["id"].(string)
	member := referenceObject(t, f.b.request("POST", "/api/members", map[string]any{"username": "history-editor", "password": "history-editor-password", "role": "editor"}, 201))
	other := &proseSearchClient{t: t, handler: f.b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "history-editor", "password": "history-editor-password"}, 200)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	v := referenceObject(t, other.request("GET", historyPath(f), nil, 200))
	if len(v["items"].([]any)) != 0 {
		t.Fatal("another author sees private history")
	}
	other.request("GET", historyPath(f)+"/"+id, nil, 403)
	if _, err := f.ip.SaveResourceDraft(context.Background(), f.actor, f.book.ProjectID, id, service.ResourceDraft{Revision: 1, Text: "tampered"}); err == nil {
		t.Fatal("generic draft writer can mutate immutable history")
	}
	var body string
	if err := f.db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, f.actor.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal([]byte(body), &doc)
	doc["text"] = "tampered"
	broken, _ := json.Marshal(doc)
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", broken, id, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", historyPath(f)+"/"+id, nil, 503)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	other.request("GET", historyPath(f), nil, 403)
}
