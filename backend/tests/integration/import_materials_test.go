package integration

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestNovelMaterialsOriginalAndKnowledgeUse(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "小说资料用途", "novel-materials")
	branchID := branch(b, project, "本书")
	ctx := context.Background()
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	original := "# 作者规划\n第三卷揭示师父仍然活着。🌙"
	job, err := jobs.Create(ctx, actor, project, branchID, "故事方向.md", "materials-file", []byte(original))
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/projects/" + project + "/branches/" + branchID + "/imports"
	download := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "http://127.0.0.1:18765"+path, nil)
		req.AddCookie(b.cookie)
		rec := httptest.NewRecorder()
		b.handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("download status %d want %d: %s", rec.Code, status, rec.Body)
		}
		return rec
	}
	pending := download(root+"/jobs/"+job.ID+"/file", 200)
	if pending.Body.String() != original || !strings.HasPrefix(pending.Header().Get("Content-Disposition"), "attachment;") || pending.Header().Get("Cache-Control") != "private, no-store" || pending.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe or changed original download")
	}
	if state, err := jobs.Advance(ctx, job.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	if download(root+"/"+job.ID+"/file", 200).Body.String() != original {
		t.Fatal("extraction replaced original")
	}
	otherBranch := branch(b, project, "另一故事")
	download("/api/projects/"+project+"/branches/"+otherBranch+"/imports/"+job.ID+"/file", 403)
	download("/api/projects/"+project+"/branches/"+otherBranch+"/imports/jobs/"+job.ID+"/file", 403)
	member := b.request("POST", "/api/members", map[string]any{"username": "material-editor", "password": "material-editor-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+project+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "material-editor", "password": "material-editor-password-123"}, 200)
	ownerCookie := b.cookie
	b.cookie = editor.cookie
	download(root+"/"+job.ID+"/file", 403)
	download(root+"/jobs/"+job.ID+"/file", 403)
	b.cookie = ownerCookie

	path := root + "/" + job.ID
	draft := b.request("GET", path+"/draft", nil, 200)
	content := draft["content"].(map[string]any)
	policy := map[string]any{"space": "working", "visibility": "author", "reveal_at": 0}
	content["text"] = "作者修订：第四卷揭示师父仍然活着。"
	content["policy"] = policy
	content["operation_id"] = "materials-candidate"
	b.request("PUT", path+"/draft", draft, 200)
	restored := b.request("GET", path+"/draft", nil, 200)["content"].(map[string]any)
	if restored["policy"].(map[string]any)["space"] != "working" {
		t.Fatal("private knowledge use lost")
	}
	input := map[string]any{"text": content["text"], "policy": policy, "expected_ip_head": "", "operation_id": "materials-candidate"}
	result := b.request("POST", path+"/candidate", input, 201)
	artifact := result["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
	if artifact["policy"].(map[string]any)["space"] != "working" || artifact["original_hash"] != job.Hash {
		t.Fatal("candidate lost purpose or original")
	}
	again := b.request("POST", path+"/candidate", input, 201)
	if again["id"] != result["id"] {
		t.Fatal("lost receipt duplicated candidate")
	}
	input["policy"] = map[string]any{"space": "methods", "visibility": "author", "reveal_at": 0}
	b.request("POST", path+"/candidate", input, 409)
	releaseAdopt(b, project, result["id"].(string))
	releasePublish(b, project, result["id"].(string))
	resource := b.request("GET", "/api/projects/"+project+"/resources/"+artifact["resource_id"].(string), nil, 200)
	if resource["policy"].(map[string]any)["space"] != "working" || resource["text"] != content["text"] {
		t.Fatal("adoption lost reviewed content or purpose")
	}
	if download(path+"/file", 200).Body.String() != original {
		t.Fatal("adoption overwrote original")
	}
	// A valid database reference must not turn corrupt bytes into a download.
	blobPath := filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), project, job.Hash)
	if err := os.WriteFile(blobPath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	download(path+"/file", 503)
	download(root+"/jobs/"+job.ID+"/file", 503)
}
