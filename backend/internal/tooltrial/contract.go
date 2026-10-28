// Package tooltrial prepares fixed, approved bytes for isolated evaluation.
// Successful execution is not professional qualification or publication.
package tooltrial

import (
	"bytes"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/skills"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxInputBytes = 256 * 1024
const MaxOutputBytes = 256 * 1024

func Prepare(definition domain.CapabilityDefinition, bundle skills.Bundle, input []byte) (isolation.Request, error) {
	var request isolation.Request
	if definition.Validate() != nil || definition.Kind != "tool" || len(input) > MaxInputBytes {
		return request, domain.ErrValidation
	}
	// The approved guest has a fixed 256 MiB envelope; do not pretend a smaller
	// declared envelope was enforced or enlarge it to fulfill a larger request.
	if definition.Tool.MemoryMiB != 256 || definition.Tool.TimeoutSeconds > 30 {
		return request, domain.ErrValidation
	}
	copyBundle := skills.Bundle{Files: make([]skills.File, len(bundle.Files))}
	for i, file := range bundle.Files {
		copyBundle.Files[i] = skills.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	if copyBundle.Validate() != nil {
		return request, domain.ErrValidation
	}
	raw, err := json.Marshal(copyBundle)
	if err != nil || skills.Hash(raw) != definition.PackageHash {
		return request, domain.ErrConflict
	}
	entry := copyBundle.Find(definition.Tool.Entrypoint)
	if !skills.SafePath(definition.Tool.Entrypoint) || len(entry) == 0 || len(entry) > 256*1024 || !utf8.Valid(entry) || bytes.ContainsRune(entry, 0) {
		return request, domain.ErrValidation
	}
	schema, err := domain.CompileCapabilitySchema(definition.InputSchema)
	if err != nil {
		return request, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil || schema.Validate(value) != nil {
		return request, domain.ErrValidation
	}
	request = isolation.Request{Entrypoint: definition.Tool.Entrypoint, Input: bytes.Clone(input), Timeout: time.Duration(definition.Tool.TimeoutSeconds) * time.Second, MaxOutputBytes: MaxOutputBytes, Files: make([]isolation.File, len(copyBundle.Files))}
	if definition.Tool.Runtime == "linux-python" {
		request.Language = "python"
	} else {
		request.Language = "posix"
	}
	for i, file := range copyBundle.Files {
		request.Files[i] = isolation.File{Path: file.Path, Data: file.Data}
	}
	return request, nil
}

func ValidateOutput(definition domain.CapabilityDefinition, result isolation.Result) error {
	if result.Reason != "completed" || result.ExitCode != 0 || len(result.Stdout) > MaxOutputBytes || !utf8.Valid(result.Stdout) {
		return domain.ErrValidation
	}
	schema, err := domain.CompileCapabilitySchema(definition.OutputSchema)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(result.Stdout))
	if err != nil || schema.Validate(value) != nil {
		return domain.ErrValidation
	}
	return nil
}
