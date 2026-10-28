// Package models implements provider protocols. It never writes authoritative content.
package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"os"
	"strings"
	"time"
)

var (
	ErrPaidClosed    = errors.New("尚未授权本次模型调用的预算")
	ErrUnknown       = errors.New("请求结果未知，已保留预算占用；禁止自动重发")
	ErrRejected      = errors.New("服务商未接受请求，请检查模型、区域、额度和权限")
	ErrInvalidOutput = errors.New("模型结果不完整或不符合输出结构，已保留使用量")
)

type Request struct {
	Creative        *CreativeSource     `json:"creative,omitempty"`
	Image           *CharacterImageTask `json:"image,omitempty"`
	System          string              `json:"system"`
	Prompt          string              `json:"prompt"`
	Schema          json.RawMessage     `json:"schema,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens"`
}
type Result struct {
	Image            *CharacterImageOutput `json:"image,omitempty"`
	ImageUsage       *ImageUsage           `json:"image_usage,omitempty"`
	UsageKnown       bool                  `json:"usage_known,omitempty"`
	ID               string                `json:"id"`
	Text             string                `json:"text"`
	InputTokens      int64                 `json:"input_tokens"`
	OutputTokens     int64                 `json:"output_tokens"`
	CachedTokens     int64                 `json:"cached_tokens"`
	CacheWriteTokens int64                 `json:"cache_write_tokens"`
}
type Adapter struct {
	client        *http.Client
	networkDenied bool
}

// The default HTTP transport makes no application-level retries; redirects are forbidden.
func New(transport http.RoundTripper) *Adapter {
	denied := transport == nil && os.Getenv("STUDIO_PROVIDER_NETWORK") == "disabled"
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Adapter{networkDenied: denied, client: &http.Client{Transport: transport, Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type noExternalLoader struct{}

func (noExternalLoader) Load(string) (any, error) { return nil, domain.ErrValidation }

func compile(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 128*1024 {
		return nil, domain.ErrValidation
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, domain.ErrValidation
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noExternalLoader{})
	if err = compiler.AddResource("https://studio.invalid/output.json", value); err != nil {
		return nil, domain.ErrValidation
	}
	schema, err := compiler.Compile("https://studio.invalid/output.json")
	if err != nil {
		return nil, domain.ErrValidation
	}
	return schema, nil
}

// authorize must durably reserve and mark the call dispatched. Nil always denies.
// A caller must retain that reservation on ErrUnknown and never automatically resend it.
func (a *Adapter) Generate(ctx context.Context, c domain.ModelConfig, key []byte, input Request, authorize func(context.Context) error) (Result, error) {
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
	raw, err := json.Marshal(payload)
	if err != nil {
		return result, domain.ErrValidation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, domain.ErrValidation
	}
	request.Header.Set("Content-Type", "application/json")
	if c.Provider == "gemini" {
		request.Header.Set("x-goog-api-key", string(key))
	} else {
		request.Header.Set("Authorization", "Bearer "+string(key))
	}
	if c.Provider == "anthropic" {
		request.Header.Set("anthropic-version", c.APIVersion)
		if c.ProviderWorkspace != "" {
			request.Header.Set("anthropic-workspace-id", c.ProviderWorkspace)
		}
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
		// Do not copy response bodies or URLs into logs: providers may reflect prompts or keys.
		if response.StatusCode >= 500 || response.StatusCode == 408 {
			return result, ErrUnknown
		}
		return result, ErrRejected
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return result, ErrUnknown
	}
	result, complete, err := parse(c.Provider, data)
	if err != nil {
		return result, ErrUnknown
	}
	if !complete || strings.TrimSpace(result.Text) == "" || result.InputTokens < 0 || result.OutputTokens < 0 {
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

func build(c domain.ModelConfig, input Request) (string, map[string]any) {
	base := strings.TrimRight(c.BaseURL, "/")
	structured := len(input.Schema) > 0
	switch c.Provider {
	case "openai":
		body := map[string]any{"model": c.Model, "instructions": input.System, "input": input.Prompt, "max_output_tokens": input.MaxOutputTokens, "store": false}
		if structured {
			body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "candidate", "schema": input.Schema, "strict": true}}
		}
		return base + "/responses", body
	case "anthropic":
		body := map[string]any{"model": c.Model, "system": input.System, "messages": []any{map[string]any{"role": "user", "content": input.Prompt}}, "max_tokens": input.MaxOutputTokens}
		if structured {
			body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": input.Schema}}
		}
		return base + "/messages", body
	case "gemini":
		generation := map[string]any{"maxOutputTokens": input.MaxOutputTokens}
		if structured {
			generation["responseMimeType"] = "application/json"
			generation["responseJsonSchema"] = input.Schema
		}
		part := func(text string) map[string]any { return map[string]any{"parts": []any{map[string]any{"text": text}}} }
		// New paid Gemini projects store requests by default since 2026-10-26.
		// Keep manuscript retention explicit for both ordinary and streaming calls.
		body := map[string]any{"systemInstruction": part(input.System), "contents": []any{part(input.Prompt)}, "generationConfig": generation, "store": false}
		return base + "/models/" + c.Model + ":generateContent", body
	default:
		system := input.System
		if structured {
			system += "\nReturn only JSON matching this schema:\n" + string(input.Schema)
		}
		body := map[string]any{"model": c.Model, "messages": []any{map[string]any{"role": "system", "content": system}, map[string]any{"role": "user", "content": input.Prompt}}, "max_tokens": input.MaxOutputTokens, "stream": false}
		if structured {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
		return base + "/chat/completions", body
	}
}
