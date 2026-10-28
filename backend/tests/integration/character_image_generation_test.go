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

func TestCharacterImageGenerationLifecycle(t *testing.T) {
	modelEnvironment(t)
	b, db := setup(t)
	ctx := context.Background()
	pid := createProject(b, "AI三视图", "image-generation-project")
	bid := branch(b, pid, "正史")
	head := propose(b, pid, bid, "", "image-character", originalSnapshot())
	publish(b, pid, bid, head)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	cid := b.request("POST", "/api/models", map[string]any{"provider": "openai", "name": "本地图片协议替身", "model": "test-image-protocol", "base_url": "https://api.openai.com/v1", "api_key": "test-only-never-real-key", "enabled": true, "image_capabilities": domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}}, 201)["id"].(string)
	b.request("PUT", "/api/model-bindings/image_generator", map[string]any{"connection_id": cid, "connection_revision": 1, "revision": 0}, 200)
	var pngData bytes.Buffer
	png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	reply, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(pngData.Bytes())}}, "usage": map[string]any{"input_tokens": 30, "input_tokens_details": map[string]int{"text_tokens": 10, "image_tokens": 20}, "output_tokens": 40, "total_tokens": 70}})
	transport := &generationTransport{response: string(reply)}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	gen := &service.Generations{IP: ip, Adapter: models.New(transport)}
	price, err := gen.SavePrice(ctx, actor.ID, service.PriceCard{ConnectionID: cid, ConnectionRevision: 1, Currency: "USD", Image: &models.ImageRates{TextInput: 1000000, ImageInput: 2000000, ImageOutput: 4000000}, Source: "https://developers.openai.com/api/docs/pricing", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/projects/" + pid + "/branches/" + bid + "/character-images"
	if len(b.request("GET", root+"/generation-options", nil, 200)["items"].([]any)) != 1 {
		t.Fatal("missing image option")
	}
	if len(b.request("GET", "/api/projects/"+pid+"/generation-options", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("image leaked into text choices")
	}
	input := service.CharacterImageGenerationInput{ExpectedIPHead: head, CharacterID: "lin-1", Look: "青衣造型", Instructions: "水墨风，青衣，左耳银环，保持脸部一致", Size: "1536x1024", Quality: "medium", Reservation: 1000, PriceID: price.ID, OperationID: "image-generate"}
	prepared, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal("prepare image", err)
	}
	task, err := gen.Read(ctx, actor, pid, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	again := b.request("POST", root+"/generate", input, 201)
	if _, err = gen.Prepare(ctx, actor, pid, service.GenerationInput{BranchID: bid, ExpectedIPHead: head, Role: "image_generator", PriceID: price.ID, OperationID: "bypass-image", Request: models.Request{Prompt: "bypass", MaxOutputTokens: 1, Image: task.Frozen.Image}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("generic image bypass", err)
	}
	if again["id"] != task.ID {
		t.Fatal("prepare duplicated")
	}
	if _, err = gen.Start(ctx, actor, pid, task.ID); err == nil || transport.calls.Load() != 0 {
		t.Fatal("paid gate missing", err)
	}
	if _, err = (&service.Budgets{Workspace: ip.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100000, PerCall: 10000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, task.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal("image execution", state, err)
	}
	done, err := gen.Read(ctx, actor, pid, task.ID)
	if err != nil || done.Output == nil || done.Output.Image == nil {
		t.Fatal("missing result", err)
	}
	if done.Call.Charged != 210 {
		t.Fatal("wrong modality fee", done.Call)
	}
	imageID := done.Output.Image.ImportID
	item, data, err := ip.ReadCharacterImage(ctx, actor, pid, bid, imageID)
	if err != nil || !bytes.Equal(data, pngData.Bytes()) || item.Published || item.Report.CharacterImage.Generated.Model != "test-image-protocol" {
		t.Fatal("private image readback", err)
	}
	if !strings.Contains(transport.payload, "右侧面") || !strings.Contains(transport.payload, "左耳银环") || transport.model != "test-image-protocol" {
		t.Fatal("visual prompt or explicit model lost")
	}
	input.OperationID = "image-edit"
	input.References = []string{imageID}
	input.Instructions = "只把腰带改成红色，其他保持一致"
	input.Region = &models.ImageRegion{SourceID: imageID, Width: 4, Height: 3, Preserve: "必须保留脸型和青衣", Areas: []models.ImageRectangle{{X: 2500, Y: 0, Width: 5000, Height: 3333}}}
	invalidRegion := input
	invalidRegion.Region = &models.ImageRegion{SourceID: imageID, Width: 9, Height: 3, Preserve: "脸型", Areas: input.Region.Areas}
	if _, err = gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, invalidRegion); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong source dimensions accepted", err)
	}
	edit, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal("prepare edit", err)
	}
	if _, err = gen.Start(ctx, actor, pid, edit.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, edit.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal("edit", state, err)
	}
	if !strings.Contains(transport.payload, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngData.Bytes())) || !strings.Contains(transport.payload, "腰带") {
		t.Fatal("pinned reference not delivered")
	}
	var editPayload struct {
		Mask struct {
			URL string `json:"image_url"`
		} `json:"mask"`
	}
	if json.Unmarshal([]byte(transport.payload), &editPayload) != nil {
		t.Fatal("invalid image request")
	}
	maskData, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(editPayload.Mask.URL, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	mask, err := png.Decode(bytes.NewReader(maskData))
	if err != nil || mask.Bounds() != image.Rect(0, 0, 4, 3) {
		t.Fatal("missing exact source mask", err)
	}
	_, _, _, inside := mask.At(1, 0).RGBA()
	_, _, _, outside := mask.At(0, 2).RGBA()
	if inside != 0 || outside != 65535 || !strings.Contains(transport.payload, "必须保留脸型和青衣") {
		t.Fatal("wrong region or preserve request")
	}
	input.OperationID = "withdraw-region-support"
	withdrawn, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	capabilityConfig := map[string]any{"provider": "openai", "name": "本地图片协议替身", "model": "test-image-protocol", "base_url": "https://api.openai.com/v1", "enabled": true, "revision": 1, "image_capabilities": domain.ImageCapabilities{Input: true, Generate: true, Edit: true}}
	b.request("PUT", "/api/models/"+cid, capabilityConfig, 200)
	if _, err = gen.Start(ctx, actor, pid, withdrawn.ID); !errors.Is(err, domain.ErrConflict) || transport.calls.Load() != 2 {
		t.Fatal("withdrawn region capability dispatched", err)
	}
	var options []service.GenerationOption
	options, err = gen.Options(ctx, actor, pid, true)
	if err != nil || len(options) != 1 || options[0].ImageCapabilities.RegionEdit {
		t.Fatal("withdrawn capability still advertised", err)
	}
	capabilityConfig["revision"] = 2
	capabilityConfig["image_capabilities"] = domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}
	b.request("PUT", "/api/models/"+cid, capabilityConfig, 200)
	catalog := b.request("GET", "/api/projects/"+pid+"/dependencies/catalog", nil, 200)
	var source any
	for _, v := range catalog["items"].([]any) {
		ref := v.(map[string]any)["ref"].(map[string]any)
		if ref["id"] == "lin-1" && ref["kind"] == "entity" {
			source = ref
		}
	}
	candidate := b.request("POST", root+"/"+imageID+"/candidate", map[string]any{"operation_id": "adopt-image", "expected_ip_head": head, "character_source": source}, 201)
	releaseID := candidate["id"].(string)
	releaseAdopt(b, pid, releaseID)
	releasePublish(b, pid, releaseID)
	item, _, err = ip.ReadCharacterImage(ctx, actor, pid, bid, imageID)
	if err != nil || !item.Published {
		t.Fatal("image not adopted", err)
	}
	visualTask := assertVisualAssistant(t, b, ip, actor, pid, bid, head, imageID)
	if os.Getenv("STUDIO_CHARACTER_IMAGE_BROWSER_NODE") != "" {
		assertCharacterImageBrowser(t, ip, visualTask, edit.ID)
	}
	// Restore retains binary candidates, input refs, financial receipts and source graph.
	archive := filepath.Join(t.TempDir(), "images.nsbak")
	if _, err = backup.Create(ctx, db, ip.Blobs, archive); err != nil {
		t.Fatal("image backup", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() { db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`") })
	blobRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("image restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredHash string
	if err = restored.QueryRow("SELECT source_hash FROM source_imports WHERE id=?", imageID).Scan(&restoredHash); err != nil || restoredHash != item.Hash {
		t.Fatal("restored image row", err)
	}
	restoredData, err := (storage.LocalBlobs{Root: blobRoot}).ReadObject(ctx, pid, restoredHash)
	if err != nil || !bytes.Equal(restoredData, data) {
		t.Fatal("restored image bytes", err)
	}
	var restoredInput, state string
	if err = restored.QueryRow("SELECT input_hash,state FROM generation_tasks WHERE id=?", edit.ID).Scan(&restoredInput, &state); err != nil || restoredInput != edit.Frozen.InputHash {
		t.Fatal("restored image request pin", err)
	}
	inputBytes, err := (storage.LocalBlobs{Root: blobRoot}).ReadObject(ctx, pid, restoredInput)
	if err != nil || !bytes.Contains(inputBytes, []byte(imageID)) || !bytes.Contains(inputBytes, []byte("腰带")) || !bytes.Contains(inputBytes, []byte("必须保留脸型和青衣")) || !bytes.Contains(inputBytes, []byte(`"region"`)) {
		t.Fatal("restored edit input/reference", err)
	}
	var enabled int
	if err = restored.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=TRUE").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("restored image authority enabled", err)
	}
	// Unknown receipt retains reservation; a second execution cannot send again.
	input.OperationID = "unknown-image"
	input.References = nil
	input.Region = nil
	unknown, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, unknown.ID); err != nil {
		t.Fatal(err)
	}
	transport.response = `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngData.Bytes()) + `"}]}`
	if state, err := gen.Execute(ctx, unknown.ID); err != nil || state != "UNKNOWN" {
		t.Fatal("missing usage", state, err)
	}
	calls := transport.calls.Load()
	gen.Execute(ctx, unknown.ID)
	if transport.calls.Load() != calls {
		t.Fatal("unknown replay sent")
	}
	u, err := gen.Read(ctx, actor, pid, unknown.ID)
	if err != nil || u.Output != nil || u.Call.State != "UNKNOWN" {
		t.Fatal("unknown candidate exposed", err)
	}
	// Cancellation before dispatch makes no call; late cancellation charges known usage.
	input.OperationID = "cancel-image"
	cancelled, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Control(ctx, actor, pid, cancelled.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, cancelled.ID); err != nil || state != "CANCELLED" || transport.calls.Load() != calls {
		t.Fatal("cancel dispatch", state, err)
	}
	input.OperationID = "late-cancel-image"
	late, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, late.ID); err != nil {
		t.Fatal(err)
	}
	transport.response = string(reply)
	transport.started = make(chan struct{})
	transport.release = make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		state, e := gen.Execute(ctx, late.ID)
		if e == nil && state != "CANCELLED" {
			e = errors.New("late image not cancelled: " + state)
		}
		finished <- e
	}()
	<-transport.started
	if _, err = gen.Control(ctx, actor, pid, late.ID, "cancel"); err != nil {
		close(transport.release)
		t.Fatal(err)
	}
	close(transport.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	transport.started = nil
	transport.release = nil
	lateRead, err := gen.Read(ctx, actor, pid, late.ID)
	if err != nil || lateRead.Output != nil || lateRead.Call.Charged != 210 {
		t.Fatal("late image fee/output", err)
	}
	var lateImports int
	if err = db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE operation_id=?", "generated-image-"+late.ID).Scan(&lateImports); err != nil || lateImports != 0 {
		t.Fatal("late image persisted", err)
	}
	input.OperationID = "stale-image"
	stale, err := gen.PrepareCharacterImageGeneration(ctx, actor, pid, bid, input)
	if err != nil {
		t.Fatal(err)
	}
	changed := originalSnapshot()
	changed.Entities[0].Description = "角色改为白发，银环改为右耳"
	b.request("PUT", "/api/projects/"+pid+"/branches/"+bid+"/draft", map[string]any{"revision": 0, "base_head": head, "snapshot": changed}, 200)
	if _, err = gen.Start(ctx, actor, pid, stale.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale character dispatched", err)
	}
	if _, err = gen.VisualSuggestion(ctx, actor, pid, bid, visualTask); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale visual text adopted", err)
	}
	t.Logf("generated/edit/adopt/restore/unknown/cancel/stale, %d local image calls, no real provider", transport.calls.Load())
}
