package skills

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type discoveryTransport struct {
	truncated bool
	unsafe    bool
	calls     []string
}

func (t *discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls = append(t.calls, r.URL.String())
	body := `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","commit":{"tree":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`
	if strings.Contains(r.URL.Path, "/git/trees/") {
		entries := []map[string]any{{"path": "skills/b/SKILL.md", "type": "blob", "mode": "100644"}, {"path": "skills/a/SKILL.md", "type": "blob", "mode": "100644"}, {"path": "outside/SKILL.md", "type": "blob", "mode": "100644"}}
		if t.unsafe {
			entries[0]["mode"] = "120000"
		}
		raw, _ := json.Marshal(map[string]any{"truncated": t.truncated, "tree": entries})
		body = string(raw)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}
func TestDiscoveryPinsCommitAndBoundsDirectories(t *testing.T) {
	tr := &discoveryTransport{}
	got, err := NewFetcher(tr).Discover(context.Background(), RemoteRequest{Repository: "anthropics/skills", Ref: "main", Path: "skills"}, "", 1)
	if err != nil || len(got.Paths) != 1 || got.Paths[0] != "skills/a" || got.Commit != strings.Repeat("a", 40) || got.Total != 2 {
		t.Fatalf("bad discovery %#v %v", got, err)
	}
	filtered, err := NewFetcher(tr).Discover(context.Background(), RemoteRequest{Repository: "anthropics/skills", Path: "skills"}, "b", 3)
	if err != nil || len(filtered.Paths) != 1 || filtered.Paths[0] != "skills/b" {
		t.Fatal(filtered, err)
	}
	for _, location := range tr.calls {
		if strings.Contains(location, "keyword") {
			t.Fatal("private keyword leaked")
		}
	}
}
func TestDiscoveryRejectsTruncationAndSymlink(t *testing.T) {
	for _, tr := range []*discoveryTransport{{truncated: true}, {unsafe: true}} {
		if _, err := NewFetcher(tr).Discover(context.Background(), RemoteRequest{Repository: "a/b", Path: "skills"}, "", 3); err == nil {
			t.Fatal("unsafe inventory accepted")
		}
	}
}
