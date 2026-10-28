package models

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Pointers distinguish a reported zero from absent billing fields.
type streamUsage struct {
	Input        *int64 `json:"input_tokens"`
	Output       *int64 `json:"output_tokens"`
	Prompt       *int64 `json:"prompt_tokens"`
	Completion   *int64 `json:"completion_tokens"`
	Read         *int64 `json:"cache_read_input_tokens"`
	Write        *int64 `json:"cache_creation_input_tokens"`
	Hit          *int64 `json:"prompt_cache_hit_tokens"`
	Miss         *int64 `json:"prompt_cache_miss_tokens"`
	Total        *int64 `json:"total_tokens"`
	InputDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type streamResponse struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Usage  *streamUsage `json:"usage"`
	Output []struct {
		Type    string    `json:"type"`
		Content []content `json:"content"`
	} `json:"output"`
}

type streamEnvelope struct {
	Type           string          `json:"type"`
	ID             string          `json:"id"`
	ResponseID     string          `json:"responseId"`
	Index          int             `json:"index"`
	Delta          json.RawMessage `json:"delta"`
	Response       *streamResponse `json:"response"`
	Message        *streamResponse `json:"message"`
	Usage          *streamUsage    `json:"usage"`
	Error          json.RawMessage `json:"error"`
	ContentBlock   *content        `json:"content_block"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Choices []struct {
		Index  int     `json:"index"`
		Finish *string `json:"finish_reason"`
		Delta  struct {
			Content string          `json:"content"`
			Refusal string          `json:"refusal"`
			Tools   json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Candidates []struct {
		Index   int    `json:"index"`
		Finish  string `json:"finishReason"`
		Content struct {
			Parts []struct {
				Text     string          `json:"text"`
				Thought  bool            `json:"thought"`
				Function json.RawMessage `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		Input   *int64 `json:"promptTokenCount"`
		Output  *int64 `json:"candidatesTokenCount"`
		Thought *int64 `json:"thoughtsTokenCount"`
		Cached  *int64 `json:"cachedContentTokenCount"`
		Tool    *int64 `json:"toolUsePromptTokenCount"`
	} `json:"usageMetadata"`
}

type streamState struct {
	provider   string
	result     Result
	terminal   bool
	finished   bool
	accounted  bool
	invalid    bool
	started    bool
	inputKnown bool
	inputUsage streamUsage
	blocks     map[int]string
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func (s *streamState) receipt(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 512 || !utf8.ValidString(id) || strings.ContainsAny(id, "\r\n\x00") || s.result.ID != "" && s.result.ID != id {
		return ErrUnknown
	}
	s.result.ID = id
	return nil
}

func (s *streamState) consume(name, data string, emit func(string) error) error {
	if s.terminal {
		return ErrUnknown
	}
	if data == "[DONE]" {
		if (s.provider != "deepseek" && s.provider != "qwen") || !s.finished {
			return ErrUnknown
		}
		s.terminal = true
		return nil
	}
	if !utf8.ValidString(data) {
		return ErrUnknown
	}
	var wire streamEnvelope
	if json.Unmarshal([]byte(data), &wire) != nil || strings.TrimSpace(data) == "null" {
		return ErrUnknown
	}
	if name == "error" || wire.Type == "error" || present(wire.Error) {
		return ErrUnknown
	}
	if name != "" && wire.Type != "" && name != wire.Type {
		return ErrUnknown
	}
	// Chat Completions and Gemini use data-only SSE. Unknown typed extensions
	// must not be interpreted just because their payload resembles visible text.
	if s.provider != "openai" && s.provider != "anthropic" && (wire.Type != "" || name != "" && name != "message") {
		return nil
	}
	switch s.provider {
	case "openai":
		return s.openAI(wire, emit)
	case "anthropic":
		return s.anthropic(wire, emit)
	case "gemini":
		return s.gemini(wire, emit)
	default:
		return s.chat(wire, emit)
	}
}

func (s *streamState) openAI(w streamEnvelope, emit func(string) error) error {
	switch w.Type {
	case "response.created":
		if s.started || w.Response == nil {
			return ErrUnknown
		}
		s.started = true
		return s.receipt(w.Response.ID)
	case "response.output_text.delta":
		var text string
		if !s.started || json.Unmarshal(w.Delta, &text) != nil {
			return ErrUnknown
		}
		return emit(text)
	case "response.refusal.delta", "response.refusal.done":
		s.invalid = true
	case "response.completed", "response.incomplete", "response.failed":
		if !s.started || w.Response == nil {
			return ErrUnknown
		}
		r := w.Response
		if err := s.receipt(r.ID); err != nil {
			return err
		}
		if err := s.usage(r.Usage, false); err != nil {
			return err
		}
		s.terminal = true
		s.invalid = s.invalid || w.Type != "response.completed" || r.Status != "completed"
		var final strings.Builder
		for _, item := range r.Output {
			if item.Type == "message" {
				for _, part := range item.Content {
					if part.Type == "output_text" {
						final.WriteString(part.Text)
					} else if part.Type == "refusal" {
						s.invalid = true
					}
				}
			} else if item.Type != "reasoning" {
				s.invalid = true
			}
		}
		if final.String() != s.result.Text {
			return ErrUnknown
		}
	}
	return nil
}

func (s *streamState) anthropic(w streamEnvelope, emit func(string) error) error {
	switch w.Type {
	case "message_start":
		if s.started || w.Message == nil {
			return ErrUnknown
		}
		s.started = true
		if err := s.receipt(w.Message.ID); err != nil {
			return err
		}
		u := w.Message.Usage
		if u == nil || !validCount(u.Input, true) || !validCount(u.Output, true) || !validCount(u.Read, false) || !validCount(u.Write, false) {
			return ErrUnknown
		}
		s.result.InputTokens = *u.Input + count(u.Read) + count(u.Write)
		s.result.OutputTokens = *u.Output
		s.result.CachedTokens = count(u.Read)
		s.result.CacheWriteTokens = count(u.Write)
		s.inputKnown = true
		s.inputUsage = *u
	case "content_block_start":
		if !s.started || s.finished || w.ContentBlock == nil || w.Index < 0 {
			return ErrUnknown
		}
		if _, exists := s.blocks[w.Index]; exists {
			return ErrUnknown
		}
		s.blocks[w.Index] = w.ContentBlock.Type
		if w.ContentBlock.Type == "text" {
			return emit(w.ContentBlock.Text)
		}
		if w.ContentBlock.Type != "thinking" && w.ContentBlock.Type != "redacted_thinking" {
			s.invalid = true
		}
	case "content_block_delta":
		if !s.started || s.finished {
			return ErrUnknown
		}
		block, exists := s.blocks[w.Index]
		if !exists {
			return ErrUnknown
		}
		var delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(w.Delta, &delta) != nil {
			return ErrUnknown
		}
		if delta.Type == "text_delta" {
			if block != "text" {
				return ErrUnknown
			}
			return emit(delta.Text)
		}
	case "content_block_stop":
		if _, exists := s.blocks[w.Index]; !exists {
			return ErrUnknown
		}
		delete(s.blocks, w.Index)
	case "message_delta":
		if !s.started || len(s.blocks) != 0 {
			return ErrUnknown
		}
		var delta struct {
			Stop *string `json:"stop_reason"`
		}
		if json.Unmarshal(w.Delta, &delta) != nil || w.Usage == nil || !validCount(w.Usage.Output, true) || !validCount(w.Usage.Input, false) || !validCount(w.Usage.Read, false) || !validCount(w.Usage.Write, false) {
			return ErrUnknown
		}
		if *w.Usage.Output < s.result.OutputTokens ||
			w.Usage.Input != nil && *w.Usage.Input < count(s.inputUsage.Input) ||
			w.Usage.Read != nil && *w.Usage.Read < count(s.inputUsage.Read) ||
			w.Usage.Write != nil && *w.Usage.Write < count(s.inputUsage.Write) {
			return ErrUnknown
		}
		s.result.OutputTokens = *w.Usage.Output
		// These fields, when supplied in later deltas, are cumulative replacements.
		if w.Usage.Input != nil {
			s.inputUsage.Input = w.Usage.Input
		}
		if w.Usage.Read != nil {
			s.inputUsage.Read = w.Usage.Read
		}
		if w.Usage.Write != nil {
			s.inputUsage.Write = w.Usage.Write
		}
		s.result.InputTokens = count(s.inputUsage.Input) + count(s.inputUsage.Read) + count(s.inputUsage.Write)
		s.result.CachedTokens = count(s.inputUsage.Read)
		s.result.CacheWriteTokens = count(s.inputUsage.Write)
		if delta.Stop != nil && *delta.Stop != "" {
			if s.finished {
				return ErrUnknown
			}
			s.finished = true
			s.invalid = s.invalid || *delta.Stop != "end_turn"
			s.accounted = s.inputKnown
		}
	case "message_stop":
		if !s.started || !s.finished || len(s.blocks) != 0 {
			return ErrUnknown
		}
		s.terminal = true
	}
	return nil
}

func (s *streamState) chat(w streamEnvelope, emit func(string) error) error {
	if err := s.receipt(w.ID); err != nil {
		return err
	}
	if len(w.Choices) > 1 {
		return ErrUnknown
	}
	if len(w.Choices) == 1 {
		choice := w.Choices[0]
		if choice.Index != 0 || s.finished {
			return ErrUnknown
		}
		if choice.Delta.Refusal != "" || present(choice.Delta.Tools) {
			s.invalid = true
		}
		if err := emit(choice.Delta.Content); err != nil {
			return err
		}
		if choice.Finish != nil && *choice.Finish != "" {
			s.finished = true
			s.invalid = s.invalid || *choice.Finish != "stop"
		}
	}
	if w.Usage != nil {
		if !s.finished || s.accounted {
			return ErrUnknown
		}
		if err := s.usage(w.Usage, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *streamState) gemini(w streamEnvelope, emit func(string) error) error {
	if err := s.receipt(w.ResponseID); err != nil {
		return err
	}
	if len(w.Candidates) > 1 {
		return ErrUnknown
	}
	if w.PromptFeedback != nil && w.PromptFeedback.BlockReason != "" && w.PromptFeedback.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		if len(w.Candidates) != 0 || s.finished {
			return ErrUnknown
		}
		s.finished = true
		s.invalid = true
	}
	if len(w.Candidates) == 1 {
		candidate := w.Candidates[0]
		if candidate.Index != 0 || s.finished {
			return ErrUnknown
		}
		for _, part := range candidate.Content.Parts {
			if present(part.Function) {
				s.invalid = true
			}
			if !part.Thought {
				if err := emit(part.Text); err != nil {
					return err
				}
			}
		}
		if candidate.Finish != "" {
			s.finished = true
			s.invalid = s.invalid || candidate.Finish != "STOP"
		}
	}
	if w.UsageMetadata != nil {
		u := w.UsageMetadata
		if !validCount(u.Input, s.finished) || !validCount(u.Output, s.finished) || !validCount(u.Thought, false) || !validCount(u.Cached, false) || !validCount(u.Tool, false) {
			return ErrUnknown
		}
		// Intermediate chunks may carry running usage; only terminal usage qualifies.
		if s.finished {
			s.result.InputTokens = *u.Input + count(u.Tool)
			s.result.OutputTokens = *u.Output + count(u.Thought)
			s.result.CachedTokens = count(u.Cached)
			if s.result.CachedTokens > *u.Input {
				return ErrUnknown
			}
			s.accounted = true
		}
	}
	// Gemini has no DONE marker. GenerateStream also requires clean HTTP EOF.
	if s.finished && s.accounted {
		s.terminal = true
	}
	return nil
}

func count(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
func validCount(value *int64, required bool) bool {
	if value == nil {
		return !required
	}
	return *value >= 0 && *value <= 1_000_000_000_000
}

func (s *streamState) usage(u *streamUsage, chat bool) error {
	if u == nil {
		return ErrUnknown
	}
	input, output := u.Input, u.Output
	var cached *int64
	if chat {
		input, output = u.Prompt, u.Completion
		if u.PromptDetails != nil {
			cached = u.PromptDetails.Cached
			if cached == nil {
				return ErrUnknown
			}
		}
		if s.provider == "deepseek" {
			// DeepSeek reports mandatory cache hit/miss categories and their totals.
			// Missing or contradictory categories cannot be priced as zero cache.
			if !validCount(input, true) || !validCount(output, true) || !validCount(u.Hit, true) || !validCount(u.Miss, true) || !validCount(u.Total, true) {
				return ErrUnknown
			}
			if *u.Hit+*u.Miss != *input || *input+*output != *u.Total {
				return ErrUnknown
			}
			if cached != nil && *cached != *u.Hit {
				return ErrUnknown
			}
			cached = u.Hit
		}
	} else if u.InputDetails != nil {
		cached = u.InputDetails.Cached
		if cached == nil {
			return ErrUnknown
		}
	}
	if !validCount(input, true) || !validCount(output, true) || !validCount(cached, false) || count(cached) > count(input) {
		return ErrUnknown
	}
	s.result.InputTokens = *input
	s.result.OutputTokens = *output
	s.result.CachedTokens = count(cached)
	s.accounted = true
	return nil
}
