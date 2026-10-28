package skills

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type discoveryBundleTransport struct {
	corrupt bool
	calls   []string
}

func (tr *discoveryBundleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls = append(tr.calls, r.URL.String())
	files := map[string]string{"skills/method/SKILL.md": "---\nname: method\ndescription: A public method.\nlicense: MIT\n---\nUse evidence.", "skills/method/scripts/unsafe.py": "raise Exception('never execute')", "skills/method/examples/example.json": "{\"a\":1}", "LICENSE": "MIT fixture license"}
	var body []byte
	switch {
	case strings.Contains(r.URL.Path, "/commits/"):
		body = []byte(`{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","commit":{"tree":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`)
	case strings.Contains(r.URL.Path, "/git/trees/"):
		entries := []map[string]any{}
		for path, data := range files {
			entries = append(entries, map[string]any{"path": path, "type": "blob", "mode": "100644", "size": len(data), "sha": gitBlobHash([]byte(data))})
		}
		body, _ = json.Marshal(map[string]any{"tree": entries, "truncated": false})
	default:
		prefix := "/fixture/repository/" + strings.Repeat("a", 40) + "/"
		data, ok := files[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		body = []byte(data)
		if tr.corrupt {
			body = append(body, 'x')
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}, Request: r}, nil
}
func TestDiscoveryImportsCompleteDirectoryAndRejectsHashMismatch(t *testing.T) {
	tr := &discoveryBundleTransport{}
	bundle, source, err := NewFetcher(tr).Fetch(context.Background(), RemoteRequest{Repository: "fixture/repository", Ref: strings.Repeat("a", 40), Path: "skills/method"})
	if err != nil || len(bundle.Files) != 4 || source.Commit != strings.Repeat("a", 40) {
		t.Fatal(bundle, source, err)
	}
	report, err := Analyze(bundle)
	if err != nil || len(report.ScriptFiles) != 1 || len(report.LicenseFiles) != 1 || report.Qualification != "STATIC_ONLY" {
		t.Fatal(report, err)
	}
	if string(bundle.Find("scripts/unsafe.py")) != "raise Exception('never execute')" || bundle.Find("examples/example.json") == nil {
		t.Fatal("directory incomplete")
	}
	for _, call := range tr.calls {
		if strings.Contains(call, "raw.githubusercontent.com") && !strings.Contains(call, strings.Repeat("a", 40)) {
			t.Fatal("mutable raw source", call)
		}
	}
	tr.corrupt = true
	if _, _, err = NewFetcher(tr).Fetch(context.Background(), RemoteRequest{Repository: "fixture/repository", Ref: "main", Path: "skills/method"}); err == nil {
		t.Fatal("corrupt blob accepted")
	}
}
