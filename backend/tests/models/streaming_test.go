package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"novelstudio/internal/models"
)

func event(value any) string {
	b, _ := json.Marshal(value)
	return "data: " + string(b) + "\n\n"
}

// Fixtures use independent literal receipts; only wire JSON encoding is shared.
func streamFixture(provider, text, finish string) (string, string) {
	switch provider {
	case "openai":
		first := event(map[string]any{"type": "response.created", "response": map[string]any{"id": "receipt"}})
		first += "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":" + fmt.Sprintf("%q", text) + "}\n\n"
		last := event(map[string]any{"type": "response.completed", "response": map[string]any{"id": "receipt", "status": finish, "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 3}}}})
		return first, last
	case "anthropic":
		first := event(map[string]any{"type": "message_start", "message": map[string]any{"id": "receipt", "usage": map[string]any{"input_tokens": 5, "output_tokens": 0, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}}})
		first += event(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		first += event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		last := event(map[string]any{"type": "content_block_stop", "index": 0})
		last += event(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": finish}, "usage": map[string]any{"output_tokens": 20}})
		last += event(map[string]any{"type": "message_stop"})
		return first, last
	case "gemini":
		first := event(map[string]any{"responseId": "receipt", "candidates": []any{map[string]any{"index": 0, "content": map[string]any{"parts": []any{map[string]any{"text": "private", "thought": true}, map[string]any{"text": text}}}}}})
		last := event(map[string]any{"responseId": "receipt", "candidates": []any{map[string]any{"index": 0, "finishReason": finish}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 17, "thoughtsTokenCount": 3, "cachedContentTokenCount": 3}})
		return first, last
	default:
		first := event(map[string]any{"id": "receipt", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text, "reasoning_content": "private"}}}})
		u := map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "prompt_tokens_details": map[string]any{"cached_tokens": 3}}
		var last string
		if provider == "deepseek" {
			u["prompt_cache_hit_tokens"] = 3
			u["prompt_cache_miss_tokens"] = 7
			u["total_tokens"] = 30
			last = event(map[string]any{"id": "receipt", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": u})
		} else {
			last = event(map[string]any{"id": "receipt", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
			last += event(map[string]any{"id": "receipt", "choices": []any{}, "usage": u})
		}
		return first, last + "data: [DONE]\n\n"
	}
}

func goodFinish(provider string) string {
	switch provider {
	case "openai":
		return "completed"
	case "anthropic":
		return "end_turn"
	case "gemini":
		return "STOP"
	default:
		return "stop"
	}
}

// Redirect only within the test transport, retaining production URL validation.
func localAdapter(t *testing.T, handler http.HandlerFunc) (*models.Adapter, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	return models.New(transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		endpoint := *r.URL
		endpoint.Scheme = u.Scheme
		endpoint.Host = u.Host
		copy.URL = &endpoint
		copy.Host = u.Host
		return transport.RoundTrip(copy)
	})), &calls
}

func TestStreamFiveProvidersDeliverBeforeFinalHTTPChunk(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			first, last := streamFixture(provider, `{"text":"人物候选🌏"}`, goodFinish(provider))
			arrived := make(chan struct{}, 1)
			var authorized atomic.Bool
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad JSON request")
				}
				if !authorized.Load() {
					t.Error("HTTP before durable authorization")
				}
				if provider == "anthropic" && (r.Header.Get("x-api-key") != "fixture" || r.Header.Get("Authorization") != "") {
					t.Error("wrong Claude API key authentication")
				}
				if provider == "gemini" {
					if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") || r.URL.Query().Get("alt") != "sse" || r.URL.Query().Get("key") != "" {
						t.Error("wrong Gemini stream endpoint")
					}
				} else if body["stream"] != true {
					t.Error("stream missing")
				}
				if provider == "deepseek" || provider == "qwen" {
					if body["stream_options"].(map[string]any)["include_usage"] != true {
						t.Error("usage request missing")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				// Byte-wise writes split UTF-8 and JSON tokens across actual HTTP chunks.
				for i := range []byte(first) {
					_, _ = w.Write([]byte(first[i : i+1]))
					w.(http.Flusher).Flush()
				}
				select {
				case <-arrived:
				case <-time.After(3 * time.Second):
					t.Error("callback buffered until final response")
				}
				_, _ = io.WriteString(w, ": keepalive\n\n"+last)
			})
			input := request()
			before := string(input.Schema)
			var got strings.Builder
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), input, func(context.Context) error { authorized.Store(true); return nil }, func(d models.Delta) error {
				if !authorized.Load() || !utf8.ValidString(d.Text) {
					t.Error("unauthorized or split UTF-8 callback")
				}
				got.WriteString(d.Text)
				select {
				case arrived <- struct{}{}:
				default:
				}
				return nil
			})
			if err != nil || result.ID != "receipt" || result.Text != `{"text":"人物候选🌏"}` || got.String() != result.Text || result.InputTokens != 10 || result.OutputTokens != 20 || result.CachedTokens != 3 || !result.UsageKnown || calls.Load() != 1 || string(input.Schema) != before {
				t.Fatalf("result=%+v err=%v calls=%d deltas=%s", result, err, calls.Load(), got.String())
			}
			if provider == "anthropic" && result.CacheWriteTokens != 2 {
				t.Fatal("cache writes lost")
			}
		})
	}
}

func TestStreamMissingCompletionAndInvalidOutput(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		for _, scenario := range []string{"disconnect", "schema", "truncated", "missing_usage", "missing_output_tokens", "negative_usage"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				text := `{"text":"ok"}`
				finish := goodFinish(provider)
				if scenario == "schema" {
					text = `{"wrong":1}`
				}
				if scenario == "truncated" {
					finish = "length"
					if provider == "openai" {
						finish = "incomplete"
					}
				}
				first, last := streamFixture(provider, text, finish)
				if provider == "openai" && scenario == "truncated" {
					last = strings.Replace(last, "response.completed", "response.incomplete", 1)
				}
				if scenario == "disconnect" {
					last = ""
				}
				if scenario == "missing_usage" {
					last = strings.ReplaceAll(last, `"usage":`, `"ignored_usage":`)
					last = strings.ReplaceAll(last, `"usageMetadata":`, `"ignored_usage":`)
				}
				if scenario == "missing_output_tokens" {
					for _, field := range []string{"output_tokens", "completion_tokens", "candidatesTokenCount"} {
						last = strings.ReplaceAll(last, `"`+field+`":`, `"ignored":`)
					}
				}
				if scenario == "negative_usage" {
					last = strings.ReplaceAll(last, `"output_tokens":20`, `"output_tokens":-1`)
					last = strings.ReplaceAll(last, `"completion_tokens":20`, `"completion_tokens":-1`)
					last = strings.ReplaceAll(last, `"candidatesTokenCount":17`, `"candidatesTokenCount":-1`)
				}
				adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, first)
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, last)
				})
				result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, nil)
				known := scenario == "schema" || scenario == "truncated"
				want := models.ErrUnknown
				if known {
					want = models.ErrInvalidOutput
				}
				if !errors.Is(err, want) || result.UsageKnown != known || calls.Load() != 1 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if known && result.OutputTokens != 20 {
					t.Fatal("accounted usage lost")
				}
			})
		}
	}
}

func TestStreamGateCancellationAndDownstreamFailure(t *testing.T) {
	for _, scenario := range []string{"closed", "authorization", "kill_switch", "cancel", "callback"} {
		t.Run(scenario, func(t *testing.T) {
			first, _ := streamFixture("openai", `{"text":"hello"}`, "completed")
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			authorize := permit
			n := 0
			if scenario == "closed" {
				authorize = nil
			}
			if scenario == "authorization" {
				authorize = func(context.Context) error { return models.ErrPaidClosed }
			}
			if scenario == "kill_switch" {
				t.Setenv("STUDIO_PROVIDER_NETWORK", "disabled")
				adapter = models.New(nil)
			}
			_, err := adapter.GenerateStream(ctx, config("openai"), []byte("fixture-secret"), request(), authorize, func(models.Delta) error {
				n++
				if scenario == "cancel" {
					cancel()
					return nil
				}
				return errors.New("fixture-secret")
			})
			if scenario == "callback" || scenario == "cancel" {
				if !errors.Is(err, models.ErrUnknown) || n != 1 || calls.Load() != 1 {
					t.Fatal(err, n, calls.Load())
				}
			} else if !errors.Is(err, models.ErrPaidClosed) || n != 0 || calls.Load() != 0 {
				t.Fatal("paid gate opened", err)
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("secret error exposed")
			}
		})
	}
}

func TestStreamMalformedOversizeAndUnknownEvents(t *testing.T) {
	first, last := streamFixture("openai", `{"text":"ok"}`, "completed")
	for _, tc := range []struct {
		name, wire string
		valid      bool
	}{
		{"unknown", "event: future\ndata: {\"type\":\"future\",\"secret\":\"hidden\"}\n\n" + first + last, true},
		{"malformed", first + "data: {\n\n" + last, false},
		{"oversize_event", "data: " + strings.Repeat(" ", 300*1024) + "{}\n\n" + first + last, false},
		{"oversize_body", strings.Repeat(":padding\n\n", 600000) + first + last, false},
		{"missing_event_boundary", first + strings.TrimRight(last, "\n"), false},
		{"error", first + "event: error\ndata: {\"type\":\"error\",\"message\":\"fixture-secret\"}\n\n" + last, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.wire)
			})
			var visible strings.Builder
			result, err := adapter.GenerateStream(context.Background(), config("openai"), []byte("fixture-secret"), request(), permit, func(d models.Delta) error { visible.WriteString(d.Text); return nil })
			if tc.valid {
				if err != nil || !result.UsageKnown || visible.String() != `{"text":"ok"}` {
					t.Fatal(result, err)
				}
			} else if !errors.Is(err, models.ErrUnknown) || result.UsageKnown {
				t.Fatal(result, err)
			}
			if calls.Load() != 1 {
				t.Fatal("retried")
			}
		})
	}
}

func TestStreamGeminiInterimUsageDoesNotPretendToBeFinal(t *testing.T) {
	first, last := streamFixture("gemini", `{"text":"ok"}`, "STOP")
	first = event(map[string]any{"responseId": "receipt", "usageMetadata": map[string]any{"promptTokenCount": 10}}) + first
	adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first+last)
	})
	result, err := adapter.GenerateStream(context.Background(), config("gemini"), []byte("fixture"), request(), permit, nil)
	if err != nil || !result.UsageKnown || result.OutputTokens != 20 {
		t.Fatal(result, err)
	}
}

func TestStreamGeminiBlockedPromptKeepsExplicitUsage(t *testing.T) {
	wire := event(map[string]any{"responseId": "receipt", "promptFeedback": map[string]any{"blockReason": "SAFETY"}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 0}})
	adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wire)
	})
	result, err := adapter.GenerateStream(context.Background(), config("gemini"), []byte("fixture"), request(), permit, nil)
	if !errors.Is(err, models.ErrInvalidOutput) || !result.UsageKnown || result.InputTokens != 10 || result.OutputTokens != 0 {
		t.Fatal(result, err)
	}
}

func TestStreamFinishAloneCannotSettleBeforeLateUsage(t *testing.T) {
	for _, provider := range []string{"deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			first, _ := streamFixture(provider, `{"text":"ok"}`, goodFinish(provider))
			var finish, usage string
			if provider == "gemini" {
				finish = event(map[string]any{"responseId": "receipt", "candidates": []any{map[string]any{"finishReason": "STOP"}}})
				usage = event(map[string]any{"responseId": "receipt", "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 20}})
			} else {
				finish = event(map[string]any{"id": "receipt", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "delta": map[string]any{}}}})
				counts := map[string]any{"prompt_tokens": 10, "completion_tokens": 20}
				if provider == "deepseek" {
					counts["prompt_cache_hit_tokens"] = 0
					counts["prompt_cache_miss_tokens"] = 10
					counts["total_tokens"] = 30
				}
				usage = event(map[string]any{"id": "receipt", "choices": []any{}, "usage": counts}) + "data: [DONE]\n\n"
			}
			adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first+finish)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, usage)
			})
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, nil)
			if err != nil || !result.UsageKnown || result.OutputTokens != 20 {
				t.Fatal(result, err)
			}
		})
	}
}

func TestStreamAnthropicInputCategoriesNeverDecrease(t *testing.T) {
	for _, field := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		for _, intermediate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/intermediate=%t", field, intermediate), func(t *testing.T) {
				first, last := streamFixture("anthropic", `{"text":"ok"}`, "end_turn")
				if intermediate {
					start := event(map[string]any{"type": "content_block_stop", "index": 0})
					middle := event(map[string]any{"type": "message_delta", "delta": map[string]any{}, "usage": map[string]any{"output_tokens": 10, "input_tokens": 7, "cache_read_input_tokens": 4, "cache_creation_input_tokens": 3}})
					last = strings.Replace(last, start, start+middle, 1)
				}
				value := 0
				if intermediate {
					value = map[string]int{"input_tokens": 6, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}[field]
				}
				last = strings.Replace(last, `"output_tokens":20`, fmt.Sprintf(`"output_tokens":20,%q:%d`, field, value), 1)
				adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, first)
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, last)
				})
				result, err := adapter.GenerateStream(context.Background(), config("anthropic"), []byte("fixture"), request(), permit, nil)
				if !errors.Is(err, models.ErrUnknown) || result.UsageKnown || calls.Load() != 1 {
					t.Fatal(result, err, calls.Load())
				}
			})
		}
	}
}

func TestStreamDeepSeekRequiresConsistentCacheBreakdown(t *testing.T) {
	for _, scenario := range []string{"missing_hit", "missing_miss", "missing_both", "missing_total", "negative_hit", "negative_miss", "hit_over_prompt", "bad_sum", "bad_total", "conflicting_details", "explicit_zero_hit", "valid", "qwen_optional_cache"} {
		t.Run(scenario, func(t *testing.T) {
			provider := "deepseek"
			counts := map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "prompt_cache_hit_tokens": 3, "prompt_cache_miss_tokens": 7, "total_tokens": 30}
			valid := false
			switch scenario {
			case "missing_hit":
				delete(counts, "prompt_cache_hit_tokens")
			case "missing_miss":
				delete(counts, "prompt_cache_miss_tokens")
			case "missing_both":
				delete(counts, "prompt_cache_hit_tokens")
				delete(counts, "prompt_cache_miss_tokens")
			case "missing_total":
				delete(counts, "total_tokens")
			case "negative_hit":
				counts["prompt_cache_hit_tokens"] = -1
			case "negative_miss":
				counts["prompt_cache_miss_tokens"] = -1
			case "hit_over_prompt":
				counts["prompt_cache_hit_tokens"] = 11
			case "bad_sum":
				counts["prompt_cache_miss_tokens"] = 6
			case "bad_total":
				counts["total_tokens"] = 29
			case "conflicting_details":
				counts["prompt_tokens_details"] = map[string]any{"cached_tokens": 4}
			case "explicit_zero_hit":
				counts["prompt_cache_hit_tokens"] = 0
				counts["prompt_cache_miss_tokens"] = 10
				valid = true
			case "valid":
				valid = true
			case "qwen_optional_cache":
				provider = "qwen"
				counts = map[string]any{"prompt_tokens": 10, "completion_tokens": 20}
				valid = true
			}
			first, _ := streamFixture(provider, `{"text":"ok"}`, "stop")
			last := event(map[string]any{"id": "receipt", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": counts}) + "data: [DONE]\n\n"
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, last)
			})
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, nil)
			if valid {
				if err != nil || !result.UsageKnown || result.InputTokens != 10 || result.OutputTokens != 20 {
					t.Fatal(result, err)
				}
			} else if !errors.Is(err, models.ErrUnknown) || result.UsageKnown {
				t.Fatal(result, err)
			}
			if calls.Load() != 1 {
				t.Fatal("request retried")
			}
		})
	}
}

func TestStreamAnthropicFinalUsageUpdatesAreCumulative(t *testing.T) {
	first, last := streamFixture("anthropic", `{"text":"ok"}`, "end_turn")
	last = strings.Replace(last, `"output_tokens":20`, `"output_tokens":20,"input_tokens":7,"cache_read_input_tokens":4,"cache_creation_input_tokens":3`, 1)
	adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first+last)
	})
	result, err := adapter.GenerateStream(context.Background(), config("anthropic"), []byte("fixture"), request(), permit, nil)
	if err != nil || !result.UsageKnown || result.InputTokens != 14 || result.CachedTokens != 4 || result.CacheWriteTokens != 3 {
		t.Fatal(result, err)
	}
}

func TestStreamAnthropicOutputUsageCannotDecreaseFromStart(t *testing.T) {
	first, last := streamFixture("anthropic", `{"text":"ok"}`, "end_turn")
	first = strings.Replace(first, `"output_tokens":0`, `"output_tokens":25`, 1)
	adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first+last)
	})
	result, err := adapter.GenerateStream(context.Background(), config("anthropic"), []byte("fixture"), request(), permit, nil)
	if !errors.Is(err, models.ErrUnknown) || result.UsageKnown {
		t.Fatal(result, err)
	}
}

func TestStreamRefusalsRetainAccountedUsage(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			first, last := streamFixture(provider, `{"text":"ok"}`, goodFinish(provider))
			switch provider {
			case "openai":
				first += event(map[string]any{"type": "response.refusal.delta", "delta": "private refusal"})
			case "anthropic":
				last = strings.Replace(last, "end_turn", "refusal", 1)
			case "gemini":
				last = strings.Replace(last, "STOP", "SAFETY", 1)
			default:
				first = strings.Replace(first, `"reasoning_content":"private"`, `"refusal":"private refusal"`, 1)
			}
			adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first+last)
			})
			var visible strings.Builder
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, func(d models.Delta) error { visible.WriteString(d.Text); return nil })
			if !errors.Is(err, models.ErrInvalidOutput) || !result.UsageKnown || result.OutputTokens != 20 || strings.Contains(visible.String(), "private") {
				t.Fatal(result, err, visible.String())
			}
		})
	}
}

func TestStreamBoundsVisibleCallbacksAndLateUsage(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			text := `{"text":"` + strings.Repeat("文🌏", 6000) + `"}`
			first, last := streamFixture(provider, text, goodFinish(provider))
			adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, last)
			})
			n := 0
			var visible strings.Builder
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, func(d models.Delta) error {
				n++
				if len(d.Text) > 16*1024 || !utf8.ValidString(d.Text) {
					t.Error("unbounded or broken callback")
				}
				visible.WriteString(d.Text)
				return nil
			})
			if err != nil || !result.UsageKnown || visible.String() != text || n < 3 {
				t.Fatal(result.UsageKnown, err, n)
			}
		})
	}
}

func TestStreamTransportFailuresNeverRetryOrLeak(t *testing.T) {
	for _, status := range []int{302, 307, 400, 401, 408, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://api.openai.com/v1/responses?secret=fixture-secret")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "fixture-secret")
			})
			_, err := adapter.GenerateStream(context.Background(), config("openai"), []byte("fixture-secret"), request(), permit, nil)
			want := models.ErrRejected
			if status >= 500 || status == 408 {
				want = models.ErrUnknown
			}
			if !errors.Is(err, want) || calls.Load() != 1 || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestStreamRejectsUntrustedInputBeforeAuthorization(t *testing.T) {
	for _, scenario := range []string{"endpoint", "key", "schema", "prompt", "limit", "disabled", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached HTTP") })
			c := config("openai")
			input := request()
			key := []byte("fixture")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "endpoint":
				c.BaseURL = "https://evil.invalid/v1"
			case "key":
				key = []byte("bad\r\nkey")
			case "schema":
				input.Schema = json.RawMessage(`{"$ref":"https://evil.invalid/schema"}`)
			case "prompt":
				input.Prompt = ""
			case "limit":
				input.MaxOutputTokens = 0
			case "disabled":
				c.Enabled = false
			case "cancelled":
				cancel()
			}
			authorized := false
			_, err := adapter.GenerateStream(ctx, c, key, input, func(context.Context) error { authorized = true; return nil }, nil)
			if err == nil || authorized || calls.Load() != 0 {
				t.Fatal("invalid input dispatched", err)
			}
		})
	}
}

func TestStreamRejectsConflictingAndPostTerminalReceipts(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		for _, scenario := range []string{"post_terminal", "missing_id", "conflict", "wrong_event", "bad_json_type"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				first, last := streamFixture(provider, `{"text":"ok"}`, goodFinish(provider))
				switch scenario {
				case "post_terminal":
					last += event(map[string]any{"type": "future", "usage": map[string]any{"input_tokens": 1, "output_tokens": 2}})
				case "missing_id":
					first = strings.ReplaceAll(first, `"receipt"`, `""`)
					last = strings.ReplaceAll(last, `"receipt"`, `""`)
				case "conflict":
					if provider == "anthropic" {
						first += event(map[string]any{"type": "message_start", "message": map[string]any{"id": "other"}})
					} else {
						last = strings.ReplaceAll(last, `"receipt"`, `"other"`)
					}
				case "wrong_event":
					first = "event: malicious\ndata: {\"type\":\"error\"}\n\n" + first
				case "bad_json_type":
					first = "data: []\n\n" + first
				}
				adapter, calls := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, first+last)
				})
				result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, nil)
				if !errors.Is(err, models.ErrUnknown) || result.UsageKnown || calls.Load() != 1 {
					t.Fatal(result, err)
				}
			})
		}
	}
}

func TestStreamUnknownMixedEventsCannotEmitSecretShapedText(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			first, last := streamFixture(provider, `{"text":"ok"}`, goodFinish(provider))
			unknown := event(map[string]any{"type": "future_private", "delta": "private", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "private"}}}, "candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "private"}}}}}})
			adapter, _ := localAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, unknown+first+last)
			})
			var visible strings.Builder
			result, err := adapter.GenerateStream(context.Background(), config(provider), []byte("fixture"), request(), permit, func(d models.Delta) error { visible.WriteString(d.Text); return nil })
			if err != nil || !result.UsageKnown || visible.String() != `{"text":"ok"}` {
				t.Fatal(result, err, visible.String())
			}
		})
	}
}
