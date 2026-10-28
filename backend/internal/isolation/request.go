package isolation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/text/cases"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxRequestJSONBytes bounds decoding before any guest or CLI allocations.
// It covers base64 for 8 MiB of files, stdin, paths, and JSON escaping.
const MaxRequestJSONBytes = 16 * 1024 * 1024

// RequestHash identifies the exact normalized guest input used by Run.
// Computing it validates bytes only; it never starts an execution environment.
func RequestHash(request Request) (string, error) {
	payload, err := prepareRequest(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func prepareRequest(request Request) ([]byte, error) {
	if request.Language != "python" && request.Language != "posix" {
		return nil, errors.New("isolation: unsupported language")
	}
	if len(request.Input) > 256*1024 || request.Timeout < 100*time.Millisecond || request.Timeout > 30*time.Second || request.MaxOutputBytes < 1 || request.MaxOutputBytes > 1024*1024 {
		return nil, errors.New("isolation: request outside fixed resource policy")
	}
	packageMode := len(request.Files) > 0 || request.Entrypoint != ""
	if packageMode {
		if request.Script != "" {
			return nil, errors.New("isolation: script and package modes are mutually exclusive")
		}
		if err := validatePackage(request.Files, request.Entrypoint); err != nil {
			return nil, err
		}
	} else if len(request.Script) == 0 || len(request.Script) > 256*1024 || !utf8.ValidString(request.Script) {
		return nil, errors.New("isolation: invalid script")
	}
	wire := struct {
		Language       string `json:"language"`
		Script         string `json:"script"`
		Input          []byte `json:"input"`
		TimeoutMS      int64  `json:"timeout_ms"`
		MaxOutputBytes int    `json:"max_output_bytes"`
		Files          []File `json:"files,omitempty"`
		Entrypoint     string `json:"entrypoint,omitempty"`
	}{Language: request.Language, Script: request.Script, Input: request.Input, TimeoutMS: request.Timeout.Milliseconds(), MaxOutputBytes: request.MaxOutputBytes, Entrypoint: request.Entrypoint}
	if wire.Input == nil {
		wire.Input = []byte{}
	}
	if packageMode {
		wire.Files = append([]File(nil), request.Files...)
		sort.Slice(wire.Files, func(i, j int) bool { return wire.Files[i].Path < wire.Files[j].Path })
		for i := range wire.Files {
			if wire.Files[i].Data == nil {
				wire.Files[i].Data = []byte{}
			}
		}
	}
	return json.Marshal(wire)
}

func validatePackage(files []File, entrypoint string) error {
	if len(files) == 0 || len(files) > 128 || !validPackagePath(entrypoint) {
		return errors.New("isolation: invalid package or entrypoint")
	}
	total := 0
	found := false
	// Record directories as well as files, so case aliases and file/directory
	// collisions cannot depend on extraction order on either host or guest.
	type pathEntry struct {
		original string
		isFile   bool
	}
	names := map[string]pathEntry{}
	folder := cases.Fold()
	for _, file := range files {
		if !validPackagePath(file.Path) || len(file.Data) > 2*1024*1024 {
			return errors.New("isolation: unsafe package path or file size")
		}
		total += len(file.Data)
		if total > 8*1024*1024 {
			return errors.New("isolation: package exceeds total byte limit")
		}
		parts := strings.Split(file.Path, "/")
		for i := range parts {
			name := strings.Join(parts[:i+1], "/")
			isFile := i == len(parts)-1
			folded := folder.String(name)
			if existing, ok := names[folded]; ok && (existing.original != name || existing.isFile || isFile) {
				return errors.New("isolation: duplicate or conflicting package path")
			}
			names[folded] = pathEntry{name, isFile}
		}
		if file.Path == entrypoint {
			found = true
			if len(file.Data) == 0 || len(file.Data) > 256*1024 || !utf8.Valid(file.Data) {
				return errors.New("isolation: entrypoint must be bounded UTF-8")
			}
		}
	}
	if !found {
		return errors.New("isolation: entrypoint missing from package")
	}
	return nil
}

func validPackagePath(path string) bool {
	if path == "" || len(path) > 1024 || !utf8.ValidString(path) || strings.ContainsAny(path, "\\:<>\"|?*") {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	parts := strings.Split(path, "/")
	if len(parts) > 32 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CLOCK$" || stem == "CONIN$" || stem == "CONOUT$" {
			return false
		}
		for _, prefix := range []string{"COM", "LPT"} {
			if strings.HasPrefix(stem, prefix) && strings.Contains("123456789¹²³", strings.TrimPrefix(stem, prefix)) && len([]rune(stem)) == 4 {
				return false
			}
		}
	}
	return true
}
