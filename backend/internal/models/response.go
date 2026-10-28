package models

import (
	"encoding/json"
	"strings"
)

type usage struct {
	Input          int64 `json:"input_tokens"`
	Output         int64 `json:"output_tokens"`
	Prompt         int64 `json:"prompt_tokens"`
	Completion     int64 `json:"completion_tokens"`
	CacheRead      int64 `json:"cache_read_input_tokens"`
	CacheCreation  int64 `json:"cache_creation_input_tokens"`
	PromptCacheHit int64 `json:"prompt_cache_hit_tokens"`
	InputDetails   struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptDetails struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parse(provider string, data []byte) (Result, bool, error) {
	var wire struct {
		ID         string `json:"id"`
		ResponseID string `json:"responseId"`
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
		Usage      usage  `json:"usage"`
		Output     []struct {
			Type    string    `json:"type"`
			Content []content `json:"content"`
		} `json:"output"`
		Content []content `json:"content"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Candidates []struct {
			Finish  string `json:"finishReason"`
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			Input   int64 `json:"promptTokenCount"`
			Output  int64 `json:"candidatesTokenCount"`
			Thought int64 `json:"thoughtsTokenCount"`
			Cached  int64 `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Result{}, false, err
	}
	result := Result{ID: wire.ID, InputTokens: wire.Usage.Input, OutputTokens: wire.Usage.Output}
	var text strings.Builder
	complete := false
	switch provider {
	case "openai":
		complete = wire.Status == "completed"
		result.CachedTokens = wire.Usage.InputDetails.Cached
		for _, item := range wire.Output {
			if item.Type == "message" {
				for _, block := range item.Content {
					if block.Type == "output_text" {
						text.WriteString(block.Text)
					} else if block.Type == "refusal" {
						complete = false
					}
				}
			}
		}
	case "anthropic":
		complete = wire.StopReason == "end_turn"
		result.CachedTokens = wire.Usage.CacheRead
		result.CacheWriteTokens = wire.Usage.CacheCreation
		result.InputTokens += wire.Usage.CacheRead + wire.Usage.CacheCreation
		for _, block := range wire.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
	case "gemini":
		result.ID = wire.ResponseID
		result.InputTokens = wire.UsageMetadata.Input
		result.OutputTokens = wire.UsageMetadata.Output + wire.UsageMetadata.Thought
		result.CachedTokens = wire.UsageMetadata.Cached
		if len(wire.Candidates) == 1 {
			c := wire.Candidates[0]
			complete = c.Finish == "STOP"
			for _, part := range c.Content.Parts {
				if !part.Thought {
					text.WriteString(part.Text)
				}
			}
		}
	default:
		result.InputTokens = wire.Usage.Prompt
		result.OutputTokens = wire.Usage.Completion
		result.CachedTokens = wire.Usage.PromptDetails.Cached
		if provider == "deepseek" && wire.Usage.PromptCacheHit > result.CachedTokens {
			result.CachedTokens = wire.Usage.PromptCacheHit
		}
		if len(wire.Choices) == 1 {
			c := wire.Choices[0]
			complete = c.Finish == "stop" && c.Message.Refusal == ""
			text.WriteString(c.Message.Content)
		}
	}
	result.Text = text.String()
	return result, complete, nil
}
