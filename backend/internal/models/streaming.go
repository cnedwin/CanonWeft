package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"novelstudio/internal/domain"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxStreamBytes = 4 * 1024 * 1024
	maxEventBytes  = 256 * 1024
	maxDeltaBytes  = 16 * 1024
)

// Delta contains only visible candidate text. It is provisional until GenerateStream succeeds.
type Delta struct {
	Text string `json:"text"`
}

// GenerateStream invokes onDelta synchronously, after durable authorization and before
// the terminal receipt. Nil onDelta collects only. Callbacks must return promptly and
// honor their caller's context. A callback error stops reading without exposing its text.
// UsageKnown is true only after a complete, internally consistent provider receipt.
// ErrUnknown always retains the reservation: never automatically resend this request.
func (a *Adapter) GenerateStream(ctx context.Context, c domain.ModelConfig, key []byte, input Request, authorize func(context.Context) error, onDelta func(Delta) error) (Result, error) {
	if input.Image != nil {
		return Result{}, domain.ErrValidation
	}
	var result Result
	if authorize == nil || a.networkDenied {
		return result, ErrPaidClosed
	}
	if err := domain.ValidateModelConfig(c); err != nil {
		return result, err
	}
	if !c.Enabled || len(key) == 0 || len(key) > 8192 || bytes.ContainsAny(key, "\r\n\x00") || strings.TrimSpace(input.Prompt) == "" || len(input.Prompt)+len(input.System) > 1024*1024 || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 131072 {
		return result, domain.ErrValidation
	}
	schema, err := compile(input.Schema)
	if err != nil {
		return result, err
	}
	endpoint, payload := build(c, input)
	if c.Provider == "gemini" {
		endpoint = strings.TrimSuffix(endpoint, ":generateContent") + ":streamGenerateContent?alt=sse"
	} else {
		payload["stream"] = true
		if c.Provider == "deepseek" || c.Provider == "qwen" {
			payload["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return result, domain.ErrValidation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, domain.ErrValidation
	}
	// Remove replay support, including any transport-level retry based on GetBody.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	switch c.Provider {
	case "gemini":
		request.Header.Set("x-goog-api-key", string(key))
	case "anthropic":
		request.Header.Set("x-api-key", string(key))
		request.Header.Set("anthropic-version", c.APIVersion)
		if c.ProviderWorkspace != "" {
			request.Header.Set("anthropic-workspace-id", c.ProviderWorkspace)
		}
	default:
		request.Header.Set("Authorization", "Bearer "+string(key))
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = authorize(ctx); err != nil {
		return result, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return result, ErrUnknown
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 || response.StatusCode == 408 {
			return result, ErrUnknown
		}
		return result, ErrRejected
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return result, ErrUnknown
	}
	state := streamState{provider: c.Provider, blocks: make(map[int]string)}
	emit := func(text string) error {
		if !utf8.ValidString(text) || len(state.result.Text)+len(text) > maxStreamBytes {
			return ErrUnknown
		}
		for len(text) > 0 {
			if ctx.Err() != nil {
				return ErrUnknown
			}
			n := len(text)
			if n > maxDeltaBytes {
				n = maxDeltaBytes
				for !utf8.RuneStart(text[n]) {
					n--
				}
			}
			delta := text[:n]
			state.result.Text += delta
			if onDelta != nil && onDelta(Delta{Text: delta}) != nil {
				return ErrUnknown
			}
			text = text[n:]
		}
		return nil
	}
	err = readStream(response.Body, func(name, data string) error {
		if ctx.Err() != nil {
			return ErrUnknown
		}
		return state.consume(name, data, emit)
	})
	result = state.result
	if err != nil || ctx.Err() != nil || !state.terminal || !state.accounted || result.ID == "" {
		return result, ErrUnknown
	}
	result.UsageKnown = true
	if state.invalid || strings.TrimSpace(result.Text) == "" {
		return result, ErrInvalidOutput
	}
	if schema != nil {
		value, decodeErr := jsonschema.UnmarshalJSON(strings.NewReader(result.Text))
		if decodeErr != nil || schema.Validate(value) != nil {
			return result, ErrInvalidOutput
		}
	}
	return result, nil
}

// readStream frames SSE incrementally. Even unknown events and comments count toward
// the total budget. An unterminated event at EOF is never a completion signal.
func readStream(source io.Reader, consume func(string, string) error) error {
	reader := bufio.NewReaderSize(io.LimitReader(source, maxStreamBytes+1), 32*1024)
	var name string
	var line, data []byte
	total, eventBytes := 0, 0
	for {
		fragment, err := reader.ReadSlice('\n')
		total += len(fragment)
		if total > maxStreamBytes || len(line)+len(fragment) > maxEventBytes {
			return ErrUnknown
		}
		line = append(line, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(line) == 0 && len(data) == 0 && name == "" {
				return nil
			}
			return ErrUnknown
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
		line = line[:0]
		if value == "" {
			if len(data) > 0 {
				if err = consume(name, string(data[:len(data)-1])); err != nil {
					return err
				}
			}
			name = ""
			data = data[:0]
			eventBytes = 0
			continue
		}
		eventBytes += len(value)
		if eventBytes > maxEventBytes {
			return ErrUnknown
		}
		if strings.HasPrefix(value, ":") {
			continue
		}
		field, content, _ := strings.Cut(value, ":")
		content = strings.TrimPrefix(content, " ")
		switch field {
		case "event":
			name = content
		case "data":
			data = append(data, content...)
			data = append(data, '\n')
		}
	}
}
