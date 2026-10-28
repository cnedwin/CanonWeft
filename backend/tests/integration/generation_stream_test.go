package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"sync/atomic"
	"testing"
	"time"
)

// Production provider host validation still runs; only this test's transport
// redirects the authorized HTTP request to an actual loopback streaming server.
type localStreamTransport struct{ target *url.URL }

func (r localStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	endpoint := *req.URL
	endpoint.Scheme, endpoint.Host = r.target.Scheme, r.target.Host
	clone.URL = &endpoint
	return http.DefaultTransport.RoundTrip(clone)
}

func TestGenerationStreamPersistsBeforeEOFAndNeverResends(t *testing.T) {
	for _, missingUsage := range []bool{false, true} {
		t.Run(fmt.Sprint("missing_usage=", missingUsage), func(t *testing.T) {
			b, svc, actor, project, original := generationFixture(t, &generationTransport{})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			release := make(chan struct{})
			var released atomic.Bool
			finish := func() {
				if released.CompareAndSwap(false, true) {
					close(release)
				}
			}
			defer finish()
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"stream-proof\",\"choices\":[{\"delta\":{\"content\":\"第一段：林舟推开门。\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				fmt.Fprint(w, "data: {\"id\":\"stream-proof\",\"choices\":[{\"delta\":{\"content\":\"门外下着雨。\"},\"finish_reason\":\"stop\"}]}\n\n")
				if !missingUsage {
					fmt.Fprint(w, "data: {\"id\":\"stream-proof\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10,\"prompt_cache_hit_tokens\":10,\"prompt_cache_miss_tokens\":10,\"total_tokens\":30}}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer func() { finish(); server.Close() }()
			target, _ := url.Parse(server.URL)
			svc.Adapter = models.New(localStreamTransport{target})
			root := "/api/projects/" + project + "/generations"
			prepared := b.request("POST", root, map[string]any{"branch_id": original.BranchID, "role": "writer", "price_id": original.Frozen.Price.ID, "operation_id": "stream-proof", "stream": true, "request": map[string]any{"prompt": "真实分段传输测试，只连接本地测试服务器", "max_output_tokens": 100}}, 201)
			id := prepared["id"].(string)
			if _, err := svc.Start(ctx, actor, project, id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := svc.Execute(ctx, id); done <- err }()
			var partial map[string]any
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				read := b.request("GET", root+"/"+id, nil, 200)
				partial, _ = read["preview"].(map[string]any)
				if partial != nil {
					break
				}
				time.Sleep(40 * time.Millisecond)
			}
			if partial == nil || partial["text"] != "第一段：林舟推开门。" {
				finish()
				t.Fatal("partial output not durable/visible before terminal receipt", partial)
			}
			if partial["provisional"] != true || partial["sequence"].(float64) < 1 {
				t.Fatal("preview is not explicitly provisional", partial)
			}
			// A new service instance reads the durable preview, not worker memory.
			restarted := &service.Generations{IP: svc.IP, Adapter: svc.Adapter}
			if recovered, err := restarted.Read(ctx, actor, project, id); err != nil || recovered.Preview == nil || recovered.Preview.Text != partial["text"] {
				t.Fatal("new service could not recover exact persisted preview", err)
			}
			finish()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("stream did not finish")
			}
			result := b.request("GET", root+"/"+id, nil, 200)
			want := "SUCCEEDED"
			if missingUsage {
				want = "UNKNOWN"
			}
			if result["state"] != want {
				t.Fatal(result)
			}
			if missingUsage {
				if result["output"] != nil || result["call"].(map[string]any)["state"] != "UNKNOWN" {
					t.Fatal("incomplete receipt promoted", result)
				}
			} else if result["output"].(map[string]any)["text"] != "第一段：林舟推开门。门外下着雨。" {
				t.Fatal(result)
			}
			if _, err := svc.Execute(ctx, id); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 1 {
				t.Fatal("provider request resent", attempts.Load())
			}
		})
	}
}

func TestGenerationStreamStopsOnCancellationOrStaleAuthority(t *testing.T) {
	for _, action := range []string{"cancel", "model_disabled", "stale_head", "membership"} {
		t.Run(action, func(t *testing.T) {
			b, svc, actor, project, old := generationFixture(t, &generationTransport{})
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			stopped := make(chan struct{})
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"interrupt-proof\",\"choices\":[{\"delta\":{\"content\":\"尚未完成的私人文字\"}}]}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
			}))
			defer func() { cancel(); server.Close() }()
			target, _ := url.Parse(server.URL)
			svc.Adapter = models.New(localStreamTransport{target})
			root := "/api/projects/" + project + "/generations"
			prepared := b.request("POST", root, map[string]any{"branch_id": old.BranchID, "role": "writer", "price_id": old.Frozen.Price.ID, "operation_id": "interrupt-proof", "stream": true, "request": map[string]any{"prompt": "local test", "max_output_tokens": 100}}, 201)
			id := prepared["id"].(string)
			if _, err := svc.Start(ctx, actor, project, id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := svc.Execute(ctx, id); done <- err }()
			var preview map[string]any
			for until := time.Now().Add(4 * time.Second); time.Now().Before(until); {
				read := b.request("GET", root+"/"+id, nil, 200)
				preview, _ = read["preview"].(map[string]any)
				if preview != nil {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			if preview == nil {
				t.Fatal("preview never persisted")
			}
			// Project access does not imply access to another author's private run.
			member := b.request("POST", "/api/members", map[string]any{"username": "stream-reader", "password": "stream-reader-password-123", "role": "editor"}, 201)
			b.request("PUT", "/api/projects/"+project+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
			other := &browser{t: t, handler: b.handler}
			other.request("POST", "/api/login", map[string]any{"username": "stream-reader", "password": "stream-reader-password-123"}, 200)
			other.request("GET", root+"/"+id, nil, 403)
			switch action {
			case "cancel":
				if _, err := svc.Control(ctx, actor, project, id, "cancel"); err != nil {
					t.Fatal(err)
				}
			case "model_disabled":
				b.request("PUT", "/api/models/"+old.Frozen.Config.ID, map[string]any{"provider": "deepseek", "name": "停用", "model": "deepseek-flash", "base_url": "https://api.deepseek.com", "enabled": false, "revision": 1}, 200)
			case "stale_head":
				candidate := propose(b, project, old.BranchID, "", "new-canon", originalSnapshot())
				publish(b, project, old.BranchID, candidate)
			case "membership":
				if _, err := svc.IP.Workspace.DB.Exec("UPDATE users SET enabled=FALSE WHERE id=?", actor.ID); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("stalled HTTP request survived cancellation/revocation")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("receipt persistence did not finish")
			}
			var state, callState, output string
			if err := svc.IP.Workspace.DB.QueryRow("SELECT g.state,c.state,c.output_hash FROM generation_tasks g JOIN model_calls c ON c.id=g.call_id WHERE g.id=?", id).Scan(&state, &callState, &output); err != nil {
				t.Fatal(err)
			}
			if callState != "UNKNOWN" || output != "" {
				t.Fatal("unfinished stream promoted/refunded", state, callState, output)
			}
			if action == "cancel" && state != "CANCELLED" {
				t.Fatal(state)
			}
			if action == "stale_head" {
				b.request("GET", root+"/"+id, nil, 409)
			}
			if action == "membership" {
				b.request("GET", root+"/"+id, nil, 401)
			}
			_, _ = svc.Execute(ctx, id)
			if attempts.Load() != 1 {
				t.Fatal("cancelled stream resent")
			}
		})
	}
}
