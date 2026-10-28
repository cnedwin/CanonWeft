package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"image"
	"image/png"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNovelImagesLifecycle(t *testing.T) {
	modelEnvironment(t)
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	draft, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "novel-images-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/novels/" + book.ID + "/images"
	w := b.request("GET", root+"/workspace", nil, 200)
	head := w["branch"].(map[string]any)["head"].(string)
	head = propose(b, book.ProjectID, book.BranchID, head, "novel-image-ip", originalSnapshot())
	publish(b, book.ProjectID, book.BranchID, head)
	var publishedPayload []byte
	if err = db.QueryRow("SELECT payload FROM ip_revisions WHERE id=?", head).Scan(&publishedPayload); err != nil {
		t.Fatal(err)
	}
	var publishedRoles domain.Snapshot
	if err = json.Unmarshal(publishedPayload, &publishedRoles); err != nil {
		t.Fatal(err)
	}
	draftPath := "/api/projects/" + book.ProjectID + "/branches/" + book.BranchID + "/draft"
	saveRoleDraft := func(snapshot domain.Snapshot) {
		current := b.request("GET", draftPath, nil, 200)
		b.request("PUT", draftPath, map[string]any{"revision": current["revision"], "base_head": head, "snapshot": snapshot}, 200)
	}
	saveRoleDraft(publishedRoles)
	sourceID := resource(b, book.ProjectID, book.BranchID, "钟楼场景资料")
	sourceText := "钟楼东侧石桥，午夜雨势增强。" + strings.Repeat("石桥桥面有积水，远处窗口透出暖光。", 150) + "全文末尾必须保留的钟楼标记。"
	sourceRelease := prepareRelease(b, book.ProjectID, book.BranchID, head, "", "novel-image-source", []map[string]any{{"resource_id": sourceID, "expected_head": "", "text": sourceText}})
	releaseAdopt(b, book.ProjectID, sourceRelease)
	releasePublish(b, book.ProjectID, sourceRelease)
	cid := b.request("POST", "/api/models", map[string]any{"provider": "openai", "name": "本地小说图片替身", "model": "test-image-protocol", "base_url": "https://api.openai.com/v1", "api_key": "test-only-never-real-key", "enabled": true, "image_capabilities": domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/image_generator", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	var pixels bytes.Buffer
	png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	reply, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(pixels.Bytes())}}, "usage": map[string]any{"input_tokens": 30, "input_tokens_details": map[string]int{"text_tokens": 10, "image_tokens": 20}, "output_tokens": 40, "total_tokens": 70}})
	transport := &generationTransport{response: string(reply)}
	ip := &service.IP{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Image: &models.ImageRates{TextInput: 1000000, ImageInput: 2000000, ImageOutput: 4000000}, Source: "https://developers.openai.com/api/docs/pricing", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	target := &models.NovelImageTarget{BookID: book.ID, Kind: "cover", Name: "雨夜钟楼", SourceIDs: []string{sourceID}, CharacterIDs: []string{"lin-1"}}
	in := service.CharacterImageGenerationInput{Novel: target, ExpectedIPHead: head, Instructions: "雨夜钟楼，给书名留白", Size: "1024x1536", Quality: "medium", Reservation: 1000, PriceID: price.ID, OperationID: "novel-cover-generate"}
	prepared := b.request("POST", root+"/generate", in, 201)
	task := prepared["id"].(string)
	if _, err = (&service.Budgets{Workspace: entry.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100000, PerCall: 10000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, task); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, task); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	done, err := gen.Read(ctx, actor, book.ProjectID, task)
	if err != nil || done.Output == nil || done.Output.Image == nil {
		t.Fatal("image output", err)
	}
	images := &service.NovelImages{Generations: gen}
	item, data, err := images.Read(ctx, actor.ID, book.ID, done.Output.Image.ImportID)
	if err != nil || !bytes.Equal(data, pixels.Bytes()) || item.Report.NovelImage == nil || item.Report.CharacterImage != nil || !strings.Contains(transport.payload, "雨夜钟楼") {
		t.Fatal("novel image identity", err)
	}
	if !strings.Contains(transport.payload, "全文末尾必须保留的钟楼标记") {
		t.Fatal("selected source was truncated")
	}
	visualTask := assertNovelVisualAssistant(t, b, ip, actor, book.ID, book.ProjectID, book.BranchID, head, target, item.ID)
	coverRelease := b.request("POST", root+"/"+item.ID+"/candidate", map[string]any{"expected_ip_head": head, "operation_id": "novel-cover-candidate"}, 201)["id"].(string)
	releaseAdopt(b, book.ProjectID, coverRelease)
	releasePublish(b, book.ProjectID, coverRelease)
	// Removing a character from the current draft invalidates its visual basis,
	// but must never delete the adopted original or break gallery access.
	saveRoleDraft(domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}})
	history, e := images.List(ctx, actor.ID, book.ID, "")
	if e != nil {
		t.Fatal("deleted character broke gallery", e)
	}
	foundHistory := false
	for _, v := range history.Items {
		if v.ID == item.ID {
			foundHistory = v.Published && v.NeedsReview != ""
		}
	}
	if !foundHistory {
		t.Fatal("deleted character hid historical artwork or lost review warning")
	}
	if _, original, e := images.Read(ctx, actor.ID, book.ID, item.ID); e != nil || !bytes.Equal(original, data) {
		t.Fatal("deleted character removed historical pixels", e)
	}
	saveRoleDraft(publishedRoles)
	in.OperationID = "novel-cover-visual-edit"
	in.References = []string{item.ID}
	in.VisualGenerationID = visualTask
	in.Region = &models.ImageRegion{SourceID: item.ID, Width: 4, Height: 3, Preserve: "保留钟楼与书名留白", Areas: []models.ImageRectangle{{X: 0, Y: 0, Width: 5000, Height: 5000}}}
	edit, err := images.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil {
		t.Fatal("visual proposal edit", err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, edit.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, edit.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("edit", state, e)
	}
	if !strings.Contains(transport.payload, "mask") || !strings.Contains(transport.payload, "保留钟楼") {
		t.Fatal("novel region lost")
	}
	if os.Getenv("STUDIO_NOVEL_IMAGES_BROWSER_NODE") != "" {
		assertNovelImagesBrowser(t, ip, book.Setup.Title, book.ID, task, visualTask)
	}
	// A named scene uses the same book, with independent purpose and image identity.
	sceneTarget := *target
	sceneTarget.Kind = "scene"
	sceneTarget.Name = "钟楼石桥"
	in.Novel = &sceneTarget
	in.OperationID = "novel-scene"
	in.VisualGenerationID = ""
	in.Region = nil
	scene, err := images.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, book.ProjectID, scene.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := gen.Execute(ctx, scene.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	sceneDone, err := gen.Read(ctx, actor, book.ProjectID, scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.OperationID = "novel-stale-source"
	stale, err := images.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	nextSource := prepareRelease(b, book.ProjectID, book.BranchID, head, "", "novel-source-change", []map[string]any{{"resource_id": sourceID, "expected_head": sourceRelease, "text": "钟楼已经倒塌，石桥封闭。"}})
	releaseAdopt(b, book.ProjectID, nextSource)
	releasePublish(b, book.ProjectID, nextSource)
	if _, err = gen.Start(ctx, actor, book.ProjectID, stale.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale source sent", err)
	}
	if _, err = images.Candidate(ctx, actor.ID, book.ID, sceneDone.Output.Image.ImportID, head, "stale-scene-adopt"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale image adopted", err)
	}
	if _, err = images.Suggestion(ctx, actor.ID, book.ID, visualTask); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale visual suggestion", err)
	}
	in.OperationID = "novel-stale-settings"
	settingsTask, err := images.Prepare(ctx, actor.ID, book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	changed := book.Setup
	changed.Title = "钟楼的余晖"
	settingsRoot := "/api/novels/" + book.ID + "/settings"
	b.request("PUT", settingsRoot+"/draft", map[string]any{"revision": 0, "base_revision": 1, "setup": changed}, 200)
	preview := b.request("POST", settingsRoot+"/preview", map[string]any{"draft_revision": 1}, 200)
	b.request("POST", settingsRoot+"/confirm", map[string]any{"draft_revision": 1, "expected_revision": 1, "preview_hash": preview["hash"], "operation_id": "novel-image-settings", "accept_settings": true}, 200)
	if _, err = gen.Start(ctx, actor, book.ProjectID, settingsTask.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale settings sent", err)
	}
	listed, err := images.List(ctx, actor.ID, book.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range listed.Items {
		if v.Report.NovelImage != nil && v.Report.NovelImage.Generated != nil && v.NeedsReview == "" {
			t.Fatal("old basis not marked", v.ID)
		}
	}
	otherDraft, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := entry.Confirm(ctx, actor.ID, otherDraft.ID, service.NovelConfirmation{Revision: otherDraft.Revision, OperationID: "other-novel-image", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = images.Read(ctx, actor.ID, other.ID, item.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("cross-book image", err)
	}
	b.request("GET", root, nil, 200)
	b.request("GET", "/api/projects/"+book.ProjectID+"/generations?branch="+book.BranchID+"&image_novel="+book.ID, nil, 200)
	scanner := service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if _, err = scanner.Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("novel image sources", err)
	}
	archive := filepath.Join(t.TempDir(), "novel-images.nsbak")
	if _, err = backup.Create(ctx, db, ip.Blobs, archive); err != nil {
		t.Fatal("novel image backup", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() { db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`") })
	blobRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("novel image restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var imageHash, inputHash string
	if err = restored.QueryRow("SELECT source_hash FROM source_imports WHERE id=?", item.ID).Scan(&imageHash); err != nil || imageHash != item.Hash {
		t.Fatal("restored image", err)
	}
	store := storage.LocalBlobs{Root: blobRoot}
	if restoredBytes, e := store.ReadObject(ctx, book.ProjectID, imageHash); e != nil || !bytes.Equal(restoredBytes, data) {
		t.Fatal("restored image bytes", e)
	}
	if err = restored.QueryRow("SELECT input_hash FROM generation_tasks WHERE id=?", edit.ID).Scan(&inputHash); err != nil {
		t.Fatal(err)
	}
	if request, e := store.ReadObject(ctx, book.ProjectID, inputHash); e != nil || !bytes.Contains(request, []byte("全文末尾必须保留的钟楼标记")) || !bytes.Contains(request, []byte(visualTask)) {
		t.Fatal("restored novel image basis and visual origin", e)
	}
}
