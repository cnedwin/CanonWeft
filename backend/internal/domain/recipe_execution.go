package domain

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RecipeValueBinding transfers a specific value, never an implicit agent history.
// Pointers use the JSON string form of RFC 6901, not URI fragments or expressions.
type RecipeValueBinding struct {
	TargetPointer string `json:"target_pointer"`
	SourceStep    string `json:"source_step"`
	SourcePointer string `json:"source_pointer"`
}

const recipeInputLimit = 256 * 1024

func recipeJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || len(raw) > recipeInputLimit || !utf8.Valid(raw) || validateReviewJSON(raw) != nil {
		return nil, ErrValidation
	}
	// encoding/json replaces isolated UTF-16 surrogate escapes with U+FFFD.
	// An agent hand-off must not silently change a key or a quoted source value.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		code, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if code >= 0xDC00 && code <= 0xDFFF {
			return nil, ErrValidation
		}
		if code >= 0xD800 && code <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return nil, ErrValidation
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return nil, ErrValidation
			}
			i += 6
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, ErrValidation
	}
	return value, nil
}

func recipePointer(pointer string) ([]string, error) {
	if !utf8.ValidString(pointer) || len(pointer) > recipeInputLimit {
		return nil, ErrValidation
	}
	if pointer == "" {
		return []string{}, nil
	}
	if pointer[0] != '/' {
		return nil, ErrValidation
	}
	tokens := strings.Split(pointer[1:], "/")
	if len(tokens) > 64 {
		return nil, ErrValidation
	}
	for i, token := range tokens {
		var decoded strings.Builder
		for j := 0; j < len(token); j++ {
			if token[j] != '~' {
				decoded.WriteByte(token[j])
				continue
			}
			j++
			if j >= len(token) || (token[j] != '0' && token[j] != '1') {
				return nil, ErrValidation
			}
			if token[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		tokens[i] = decoded.String()
	}
	return tokens, nil
}

func recipeIndex(token string, length int) (int, error) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, ErrValidation
	}
	for _, ch := range token {
		if ch < '0' || ch > '9' {
			return 0, ErrValidation
		}
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 || index >= length {
		return 0, ErrValidation
	}
	return index, nil
}

func recipeRead(value any, tokens []string) (any, error) {
	for _, token := range tokens {
		switch object := value.(type) {
		case map[string]any:
			var exists bool
			value, exists = object[token]
			if !exists {
				return nil, ErrValidation
			}
		case []any:
			index, err := recipeIndex(token, len(object))
			if err != nil {
				return nil, err
			}
			value = object[index]
		default:
			return nil, ErrValidation
		}
	}
	return value, nil
}

func recipeOverlaps(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ResolveRecipeInput only reads direct, explicitly allowed predecessors. Missing
// or ambiguous paths fail closed. Targets must exist in the input template;
// duplicate/overlapping writes are rejected regardless of their list order.
func ResolveRecipeInput(base json.RawMessage, bindings []RecipeValueBinding, allowed []string, outputs map[string]json.RawMessage) (json.RawMessage, error) {
	if err := ValidateRecipeRouting(base, bindings, allowed); err != nil {
		return nil, err
	}
	if len(bindings) > 64 || len(allowed) > 32 {
		return nil, ErrValidation
	}
	permissions := map[string]bool{}
	for _, step := range allowed {
		if !capabilityKey.MatchString(step) || permissions[step] {
			return nil, ErrValidation
		}
		permissions[step] = true
	}
	root, err := recipeJSON(base)
	if err != nil {
		return nil, err
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, ErrValidation
	}
	targets := make([][]string, len(bindings))
	values := make([]any, len(bindings))
	sources := map[string]any{}
	for i, binding := range bindings {
		if !permissions[binding.SourceStep] {
			return nil, ErrValidation
		}
		target, err := recipePointer(binding.TargetPointer)
		if err != nil {
			return nil, err
		}
		for _, previous := range targets[:i] {
			if recipeOverlaps(previous, target) {
				return nil, ErrValidation
			}
		}
		if _, err = recipeRead(root, target); err != nil {
			return nil, err
		}
		source, exists := sources[binding.SourceStep]
		if !exists {
			source, err = recipeJSON(outputs[binding.SourceStep])
			if err != nil {
				return nil, err
			}
			sources[binding.SourceStep] = source
		}
		pointer, err := recipePointer(binding.SourcePointer)
		if err != nil {
			return nil, err
		}
		value, err := recipeRead(source, pointer)
		if err != nil {
			return nil, err
		}
		targets[i], values[i] = target, value
	}
	for i, target := range targets {
		if len(target) == 0 {
			root = values[i]
			continue
		}
		parent, err := recipeRead(root, target[:len(target)-1])
		if err != nil {
			return nil, err
		}
		token := target[len(target)-1]
		switch object := parent.(type) {
		case map[string]any:
			object[token] = values[i]
		case []any:
			index, err := recipeIndex(token, len(object))
			if err != nil {
				return nil, err
			}
			object[index] = values[i]
		default:
			return nil, ErrValidation
		}
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, ErrValidation
	}
	result, err := json.Marshal(root)
	if err != nil || len(result) > recipeInputLimit || validateReviewJSON(result) != nil {
		return nil, ErrValidation
	}
	return result, nil
}

// ValidateRecipeRouting checks every locally knowable condition before a parent
// task can start. Future predecessor values are intentionally not fabricated.
func ValidateRecipeRouting(base json.RawMessage, bindings []RecipeValueBinding, allowed []string) error {
	if len(bindings) > 64 || len(allowed) > 32 {
		return ErrValidation
	}
	root, err := recipeJSON(base)
	if err != nil {
		return err
	}
	if _, ok := root.(map[string]any); !ok {
		return ErrValidation
	}
	permissions := map[string]bool{}
	for _, key := range allowed {
		if !capabilityKey.MatchString(key) || permissions[key] {
			return ErrValidation
		}
		permissions[key] = true
	}
	targets := [][]string{}
	for _, binding := range bindings {
		if !permissions[binding.SourceStep] {
			return ErrValidation
		}
		if _, err = recipePointer(binding.SourcePointer); err != nil {
			return err
		}
		target, err := recipePointer(binding.TargetPointer)
		if err != nil {
			return err
		}
		if _, err = recipeRead(root, target); err != nil {
			return err
		}
		for _, prior := range targets {
			if recipeOverlaps(prior, target) {
				return ErrValidation
			}
		}
		targets = append(targets, target)
	}
	return nil
}
