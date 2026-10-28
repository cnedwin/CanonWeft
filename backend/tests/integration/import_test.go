package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"novelstudio/internal/retrieval"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportedFileCandidateKeepsOriginalProvenance(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "文件导入", "one")
	bid := branch(b, pid, "正史")
	path := "/api/projects/" + pid + "/branches/" + bid + "/imports"
	upload := func() map[string]any {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("file", "原稿.txt")
		file.Write([]byte("原稿：剑客走进山洞。"))
		form.Close()
		req := httptest.NewRequest("POST", "http://127.0.0.1:18765"+path, &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Origin", "http://127.0.0.1:18765")
		req.Header.Set("Idempotency-Key", "upload-once")
		req.AddCookie(b.cookie)
		rec := httptest.NewRecorder()
		b.handler.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("upload %d %s", rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := upload()
	again := upload()
	if first["id"] != again["id"] {
		t.Fatal("upload retry duplicated import")
	}
	id := first["id"].(string)
	candidate := b.request("POST", path+"/"+id+"/candidate", map[string]any{"text": "编辑稿：剑客走进山洞寻找宝剑。", "expected_ip_head": "", "operation_id": "import-candidate"}, 201)
	ref := candidate["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
	if ref["original_hash"] != first["hash"] || ref["source_import_id"] != id || ref["hash"] == first["hash"] {
		t.Fatal("original and edited provenance were conflated")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_import_candidates WHERE import_id=? AND release_id=?", id, candidate["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatal("source link not atomically stored", err)
	}
	resource := b.request("GET", "/api/projects/"+pid+"/resources/"+ref["resource_id"].(string), nil, 200)
	if resource["head"] != "" {
		t.Fatal("import auto-published")
	}
	releaseAdopt(b, pid, candidate["id"].(string))
	releasePublish(b, pid, candidate["id"].(string))
}

func TestImportCoverageIncludesFailedUnits(t *testing.T) {
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	document, _ := archive.Create("word/document.xml")
	document.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>第一段：🌙林舟抵达雪原。</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>时间记录</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`))
	media, _ := archive.Create("word/media/image1.png")
	media.Write([]byte("unreadable-image"))
	archive.Close()
	report, err := retrieval.Extract(context.Background(), "故事.docx", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.Pending < 1 || report.Extracted != 2 {
		t.Fatalf("coverage %+v", report)
	}
	if report.Text != "第一段：🌙林舟抵达雪原。\n\n时间记录" {
		t.Fatalf("text %q", report.Text)
	}
	for _, unit := range report.Units {
		if unit.Locator == "" || unit.Status == "" {
			t.Fatal("missing coverage provenance")
		}
	}
}

func TestImportDoesNotSilentlyDecodeOrRunContent(t *testing.T) {
	if _, err := retrieval.Extract(context.Background(), "bad.txt", []byte{0xff, 0xfe, 0x80}); err == nil {
		t.Fatal("invalid encoding accepted")
	}
	_, err := retrieval.Extract(context.Background(), "scan.pdf", []byte("%PDF-1.4\nimage only"))
	if err == nil {
		t.Fatal("corrupt PDF accepted as a pending or complete import")
	}
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	f, _ := archive.Create("../escape.xml")
	f.Write([]byte("bad"))
	archive.Close()
	if _, err = retrieval.Extract(context.Background(), "escape.docx", data.Bytes()); err == nil {
		t.Fatal("archive path escape accepted")
	}
}

func TestPDFImportPartialCoverageAndSafeErrors(t *testing.T) {
	b, _ := setup(t)
	t.Setenv("STUDIO_INGEST_OCR", "disabled")
	pid := createProject(b, "扫描资料", "pdf-coverage")
	bid := branch(b, pid, "正史")
	path := "/api/projects/" + pid + "/branches/" + bid + "/imports"
	upload := func(name string, data []byte, status int) map[string]any {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("file", name)
		file.Write(data)
		form.Close()
		req := httptest.NewRequest("POST", "http://127.0.0.1:18765"+path, &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Origin", "http://127.0.0.1:18765")
		req.Header.Set("Idempotency-Key", "upload-"+name)
		req.AddCookie(b.cookie)
		rec := httptest.NewRecorder()
		b.handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("upload %d %s", rec.Code, rec.Body)
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	failure := upload("broken.pdf", []byte("%PDF-1.4\nimage only"), 400)
	if !strings.Contains(failure["error"].(string), "损坏") {
		t.Fatalf("missing safe actionable parser error: %v", failure)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	source := upload("mixed.pdf", data, 201)
	if source["hash"] != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("original PDF bytes/hash changed")
	}
	report := source["report"].(map[string]any)
	units := report["units"].([]any)
	if report["complete"] != false || report["extracted"] != float64(1) || report["pending"] != float64(1) || len(units) != 2 || units[1].(map[string]any)["locator"] != "page:2" {
		t.Fatalf("incomplete per-page report: %v", report)
	}
	id := source["id"].(string)
	input := map[string]any{"text": report["text"], "expected_ip_head": "", "operation_id": "pdf-partial", "accept_partial": false}
	b.request("POST", path+"/"+id+"/candidate", input, 400)
	input["accept_partial"] = true
	input["text"] = report["text"].(string) + "\n人工核对注释"
	candidate := b.request("POST", path+"/"+id+"/candidate", input, 201)
	ref := candidate["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
	if ref["original_hash"] != source["hash"] || ref["source_import_id"] != id || ref["hash"] == source["hash"] {
		t.Fatal("partial PDF edits lost original provenance")
	}
	releaseAdopt(b, pid, candidate["id"].(string))
	releasePublish(b, pid, candidate["id"].(string))
}
