package integration

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func TestCharacterImagesPersistPrivatelyAndPublishWithExactIdentity(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "角色图片", "character-images-project")
	bid := branch(b, pid, "小说正史")
	base := "/api/projects/" + pid + "/branches/" + bid
	root := base + "/character-images"
	b.request("PUT", base+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 200)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	imageView := "front"
	upload := func(op, character, look string, data []byte, want int) map[string]any {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		metadata, _ := form.CreateFormField("metadata")
		json.NewEncoder(metadata).Encode(map[string]string{"character_id": character, "look": look, "view": imageView, "source": "本人绘制", "rights": "原创"})
		file, _ := form.CreateFormFile("file", "正面.png")
		file.Write(data)
		form.Close()
		req := httptest.NewRequest("POST", "http://127.0.0.1:18765"+root, &body)
		req.Header.Set("Origin", "http://127.0.0.1:18765")
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Idempotency-Key", op)
		req.AddCookie(b.cookie)
		rec := httptest.NewRecorder()
		b.handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("upload %d want %d: %s", rec.Code, want, rec.Body)
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	upload("bad-file", "lin-1", "青衣", []byte("<svg></svg>"), 400)
	upload("missing-character", "does-not-exist", "青衣", pngData.Bytes(), 409)
	first := upload("upload-image", "lin-1", "青衣", pngData.Bytes(), 201)
	id := first["id"].(string)
	if again := upload("upload-image", "lin-1", "青衣", pngData.Bytes(), 201); again["id"] != id {
		t.Fatal("duplicate upload")
	}
	upload("upload-image", "lin-1", "红衣", pngData.Bytes(), 409)
	listed := b.request("GET", root+"?character=lin-1", nil, 200)
	if len(listed["items"].([]any)) != 1 || listed["items"].([]any)[0].(map[string]any)["published"] != false {
		t.Fatal("upload unexpectedly adopted", listed)
	}
	read := func(client *browser, path string, want int) {
		req := httptest.NewRequest("GET", "http://127.0.0.1:18765"+path, nil)
		req.AddCookie(client.cookie)
		rec := httptest.NewRecorder()
		client.handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("read %d want %d: %s", rec.Code, want, rec.Body)
		}
		if want == 200 && (!bytes.Equal(rec.Body.Bytes(), pngData.Bytes()) || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "private, no-store") {
			t.Fatal("wrong image or cache policy")
		}
	}
	read(b, root+"/"+id+"/file", 200)
	read(b, root+"/portrait/lin-1", 204)
	otherProject := createProject(b, "其他小说", "other-image-project")
	otherBranch := branch(b, otherProject, "正史")
	read(b, "/api/projects/"+otherProject+"/branches/"+otherBranch+"/character-images/"+id+"/file", 403)
	member := b.request("POST", "/api/members", map[string]any{"username": "reader", "password": "reader-password-123", "role": "reader"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "reader", "password": "reader-password-123"}, 200)
	read(reader, root+"/"+id+"/file", 403)
	if len(reader.request("GET", root+"?character=lin-1", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("unpublished image exposed")
	}
	head := propose(b, pid, bid, "", "initial-character", originalSnapshot())
	publish(b, pid, bid, head)
	catalog := b.request("GET", "/api/projects/"+pid+"/dependencies/catalog", nil, 200)
	var characterSource any
	for _, v := range catalog["items"].([]any) {
		ref := v.(map[string]any)["ref"].(map[string]any)
		if ref["id"] == "lin-1" && ref["kind"] == "entity" {
			characterSource = ref
		}
	}
	input := map[string]any{"operation_id": "image-candidate", "expected_ip_head": head, "character_source": characterSource}
	candidate := b.request("POST", root+"/"+id+"/candidate", input, 201)
	releaseID := candidate["id"].(string)
	if again := b.request("POST", root+"/"+id+"/candidate", input, 201); again["id"] != releaseID {
		t.Fatal("duplicate candidate")
	}
	refs := candidate["manifest"].(map[string]any)["artifacts"].([]any)
	ref := refs[0].(map[string]any)
	if ref["original_hash"] != first["hash"] || ref["source_import_id"] != id {
		t.Fatal("image provenance omitted")
	}
	dependencies := ref["dependencies"].(map[string]any)["inputs"].([]any)
	if len(dependencies) != 2 || dependencies[1].(map[string]any)["source"].(map[string]any)["id"] != "identity" {
		t.Fatal("character assertions missing from visual impact", dependencies)
	}
	resourceID := ref["resource_id"].(string)
	if b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)["head"] != "" {
		t.Fatal("candidate auto-published")
	}
	releaseAdopt(b, pid, releaseID)
	releasePublish(b, pid, releaseID)
	read(reader, root+"/"+id+"/file", 200)
	if reader.request("GET", root+"?character=lin-1", nil, 200)["items"].([]any)[0].(map[string]any)["published"] != true {
		t.Fatal("published image unavailable")
	}
	if again := b.request("POST", root+"/"+id+"/candidate", input, 201); again["id"] != releaseID {
		t.Fatal("published replay changed result")
	}
	var imports, candidates int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE project_id=?", pid).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM source_import_candidates WHERE import_id=?", id).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if imports != 1 || candidates != 1 {
		t.Fatal("duplicate persistence", imports, candidates)
	}
	imageView = "portrait"
	portrait := upload("portrait-upload", "lin-1", "青衣", pngData.Bytes(), 201)
	read(reader, root+"/portrait/lin-1", 204)
	input["operation_id"] = "portrait-candidate"
	portraitCandidate := b.request("POST", root+"/"+portrait["id"].(string)+"/candidate", input, 201)
	portraitRelease := portraitCandidate["id"].(string)
	releaseAdopt(b, pid, portraitRelease)
	releasePublish(b, pid, portraitRelease)
	read(reader, root+"/portrait/lin-1", 200)
}
