package domain

import (
	"net/url"
	"regexp"
	"strings"
)

type ProviderPreset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	CheckedAt string `json:"checked_at"`
}

func ProviderCatalog() []ProviderPreset {
	return []ProviderPreset{
		{"openai", "ChatGPT / OpenAI", "https://api.openai.com/v1", "gpt-6.1-sol", "https://developers.openai.com/api/docs/models/all", "2026-10-24"},
		{"anthropic", "Claude Opus", "https://api.anthropic.com/v1", "claude-opus-5-5", "https://platform.claude.com/docs/en/about-claude/model-deprecations", "2026-10-24"},
		{"deepseek", "DeepSeek", "https://api.deepseek.com", "deepseek-flash", "https://api-docs.deepseek.com/quick_start/pricing/", "2026-10-24"},
		{"qwen", "Qwen", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "qwen3.8-max", "https://www.alibabacloud.com/help/en/model-studio/models", "2026-10-24"},
		{"gemini", "Gemini", "https://generativelanguage.googleapis.com/v1beta", "gemini-3.8-flash", "https://ai.google.dev/gemini-api/docs/generate-content/whats-new-gemini-3.7", "2026-10-24"},
	}
}

type ModelConfig struct {
	ImageCapabilities *ImageCapabilities `json:"image_capabilities,omitempty"`
	ID                string             `json:"id"`
	Provider          string             `json:"provider"`
	Name              string             `json:"name"`
	Model             string             `json:"model"`
	BaseURL           string             `json:"base_url"`
	ProviderWorkspace string             `json:"provider_workspace"`
	APIVersion        string             `json:"api_version"`
	Enabled           bool               `json:"enabled"`
	Revision          int64              `json:"revision"`
	HasKey            bool               `json:"has_key"`
}

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)
var workspacePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{0,100}$`)
var qwenHost = regexp.MustCompile(`^[a-z0-9-]+\.(cn-beijing|ap-southeast-1|us-east-1|cn-hongkong|ap-northeast-1|eu-central-1)\.maas\.aliyuncs\.com$`)

func ValidateModelConfig(c ModelConfig) error {
	if p := c.ImageCapabilities; p != nil && ((p.Edit && !p.Input) || (p.RegionEdit && (!p.Input || !p.Edit))) {
		return ErrValidation
	}
	if strings.TrimSpace(c.Name) == "" || len([]rune(c.Name)) > 120 || !modelIDPattern.MatchString(c.Model) || !workspacePattern.MatchString(c.ProviderWorkspace) {
		return ErrValidation
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || u.RawPath != "" {
		return ErrValidation
	}
	host, path := u.Hostname(), strings.TrimRight(u.Path, "/")
	valid := false
	switch c.Provider {
	case "openai":
		valid = host == "api.openai.com" && path == "/v1"
	case "anthropic":
		valid = host == "api.anthropic.com" && path == "/v1" && (c.APIVersion == "2023-06-01" || c.APIVersion == "2026-10-21")
	case "deepseek":
		valid = host == "api.deepseek.com" && (path == "" || path == "/v1")
	case "qwen":
		valid = (host == "dashscope-intl.aliyuncs.com" || host == "dashscope.aliyuncs.com" || host == "cn-hongkong.dashscope.aliyuncs.com" || qwenHost.MatchString(host)) && path == "/compatible-mode/v1"
	case "gemini":
		valid = host == "generativelanguage.googleapis.com" && path == "/v1beta"
	}
	if !valid {
		return ErrValidation
	}
	return nil
}

type ModelBinding struct {
	Role               string `json:"role"`
	ConnectionID       string `json:"connection_id"`
	ConnectionRevision int64  `json:"connection_revision"`
	Revision           int64  `json:"revision"`
}

func ModelRoles() []string {
	return []string{"architect", "planner", "writer", "bookkeeper", "critic", "researcher", "character_designer", "continuity_editor", "style_editor", "asset_designer", "image_generator", "import_reader"}
}
