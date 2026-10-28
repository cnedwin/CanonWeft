package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type CapabilityRef struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
	Kind string `json:"kind"`
}

type ToolDefinition struct {
	Runtime        string `json:"runtime"`
	Entrypoint     string `json:"entrypoint"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MemoryMiB      int    `json:"memory_mib"`
	Network        bool   `json:"network"`
}

type RecipeStep struct {
	Key                 string   `json:"key"`
	AgentID             string   `json:"agent_id"`
	After               []string `json:"after"`
	WaitForConfirmation bool     `json:"wait_for_confirmation"`
}

// Definitions describe requested behavior. They never grant permissions or qualify themselves.
type CapabilityDefinition struct {
	Kind                  string          `json:"kind"`
	Key                   string          `json:"key"`
	Name                  string          `json:"name"`
	Description           string          `json:"description"`
	Instructions          string          `json:"instructions"`
	InputSchema           json.RawMessage `json:"input_schema"`
	OutputSchema          json.RawMessage `json:"output_schema"`
	Dependencies          []CapabilityRef `json:"dependencies"`
	Role                  string          `json:"role,omitempty"`
	PackageID             string          `json:"package_id,omitempty"`
	PackageHash           string          `json:"package_hash,omitempty"`
	Tool                  *ToolDefinition `json:"tool,omitempty"`
	Steps                 []RecipeStep    `json:"steps,omitempty"`
	MaxAutomaticRevisions int             `json:"max_automatic_revisions,omitempty"`
}

var capabilityKey = regexp.MustCompile(`^[a-z][a-z0-9-]{1,79}$`)
var capabilityID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var capabilityHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

type capabilitySchemaLoader struct{}

func (capabilitySchemaLoader) Load(string) (any, error) { return nil, ErrValidation }

func CompileCapabilitySchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return nil, ErrValidation
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrValidation
	}
	object, ok := v.(map[string]any)
	if !ok || object["type"] != "object" {
		return nil, ErrValidation
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(capabilitySchemaLoader{})
	if err = c.AddResource("https://studio.invalid/capability.json", v); err != nil {
		return nil, ErrValidation
	}
	schema, err := c.Compile("https://studio.invalid/capability.json")
	if err != nil {
		return nil, ErrValidation
	}
	return schema, nil
}

func (d CapabilityDefinition) Validate() error {
	if !slices.Contains([]string{"agent", "skill", "tool", "recipe"}, d.Kind) || !capabilityKey.MatchString(d.Key) || strings.TrimSpace(d.Name) == "" || len(d.Name) > 240 || strings.TrimSpace(d.Description) == "" || len(d.Description) > 4000 || strings.TrimSpace(d.Instructions) == "" || len(d.Instructions) > 64000 || len(d.Dependencies) > 32 {
		return ErrValidation
	}
	if _, err := CompileCapabilitySchema(d.InputSchema); err != nil {
		return err
	}
	if _, err := CompileCapabilitySchema(d.OutputSchema); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ref := range d.Dependencies {
		if !capabilityID.MatchString(ref.ID) || !capabilityHash.MatchString(ref.Hash) || seen[ref.ID] {
			return ErrValidation
		}
		allowed := (d.Kind == "agent" && (ref.Kind == "skill" || ref.Kind == "tool")) || (d.Kind == "skill" && ref.Kind == "tool") || (d.Kind == "recipe" && ref.Kind == "agent")
		if !allowed {
			return ErrValidation
		}
		seen[ref.ID] = true
	}
	if d.Kind != "agent" && d.Role != "" {
		return ErrValidation
	}
	if d.Kind == "agent" && (!slices.Contains(ModelRoles(), d.Role) || d.Role == "image_generator") {
		return ErrValidation
	}
	if d.Kind == "skill" || d.Kind == "tool" {
		if !capabilityID.MatchString(d.PackageID) || !capabilityHash.MatchString(d.PackageHash) {
			return ErrValidation
		}
	} else if d.PackageID != "" || d.PackageHash != "" {
		return ErrValidation
	}
	if d.Kind == "tool" {
		if d.Tool == nil || !slices.Contains([]string{"linux-python", "linux-posix"}, d.Tool.Runtime) || d.Tool.Network || d.Tool.TimeoutSeconds < 1 || d.Tool.TimeoutSeconds > 120 || d.Tool.MemoryMiB < 64 || d.Tool.MemoryMiB > 2048 || d.Tool.Entrypoint == "" {
			return ErrValidation
		}
	} else if d.Tool != nil {
		return ErrValidation
	}
	if d.Kind != "recipe" {
		if len(d.Steps) > 0 || d.MaxAutomaticRevisions != 0 {
			return ErrValidation
		}
		return nil
	}
	if len(d.Steps) < 1 || len(d.Steps) > 32 || d.MaxAutomaticRevisions < 0 || d.MaxAutomaticRevisions > 2 {
		return ErrValidation
	}
	prior := map[string]bool{}
	used := map[string]bool{}
	for _, step := range d.Steps {
		if !capabilityKey.MatchString(step.Key) || prior[step.Key] || !seen[step.AgentID] || len(step.After) > 32 {
			return ErrValidation
		}
		predecessors := map[string]bool{}
		for _, key := range step.After {
			if !prior[key] || predecessors[key] {
				return ErrValidation
			}
			predecessors[key] = true
		}
		prior[step.Key] = true
		used[step.AgentID] = true
	}
	if len(used) != len(seen) {
		return ErrValidation
	}
	return nil
}
