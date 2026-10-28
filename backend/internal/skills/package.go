// Package skills inspects untrusted skill bundles as data. It never runs their code.
package skills

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxBundleBytes = 8 * 1024 * 1024
const MaxFileBytes = 2 * 1024 * 1024
const MaxFiles = 128

var ErrPackage = errors.New("技能包格式或文件范围不合格")
var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)`)

type File struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}
type Bundle struct {
	Files []File `json:"files"`
}
type FileInfo struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int    `json:"size"`
	Text bool   `json:"text"`
}
type Report struct {
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Hash           string     `json:"hash"`
	LicenseClaim   string     `json:"license_claim"`
	LicenseFiles   []string   `json:"license_files"`
	RequestedTools []string   `json:"requested_tools"`
	ScriptFiles    []string   `json:"script_files"`
	Files          []FileInfo `json:"files"`
	Findings       []string   `json:"findings"`
	Qualification  string     `json:"qualification"`
}

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func SafePath(name string) bool {
	if name == "" || len(name) > 240 || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == "." || p == ".." || strings.TrimSpace(p) != p || strings.HasSuffix(p, ".") || windowsDevice.MatchString(p) {
			return false
		}
	}
	return true
}
func (b Bundle) Find(name string) []byte {
	for _, f := range b.Files {
		if f.Path == name {
			return f.Data
		}
	}
	return nil
}
func (b *Bundle) Validate() error {
	if len(b.Files) == 0 || len(b.Files) > MaxFiles {
		return ErrPackage
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range b.Files {
		key := strings.ToLower(f.Path)
		if !SafePath(f.Path) || seen[key] || len(f.Data) > MaxFileBytes {
			return ErrPackage
		}
		seen[key] = true
		total += len(f.Data)
	}
	if total > MaxBundleBytes || b.Find("SKILL.md") == nil {
		return ErrPackage
	}
	slices.SortFunc(b.Files, func(a, c File) int { return strings.Compare(a.Path, c.Path) })
	return nil
}
func ReadArchive(raw []byte) (Bundle, error) {
	b := Bundle{Files: []File{}}
	if len(raw) == 0 || len(raw) > MaxBundleBytes {
		return b, ErrPackage
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) > MaxFiles*2 {
		return b, ErrPackage
	}
	total := 0
	for _, entry := range archive.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !SafePath(name) {
			return b, ErrPackage
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() || entry.UncompressedSize64 > MaxFileBytes {
			return b, ErrPackage
		}
		reader, openErr := entry.Open()
		if openErr != nil {
			return b, ErrPackage
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, MaxFileBytes+1))
		reader.Close()
		if readErr != nil || len(data) > MaxFileBytes {
			return b, ErrPackage
		}
		total += len(data)
		if total > MaxBundleBytes {
			return b, ErrPackage
		}
		b.Files = append(b.Files, File{Path: name, Data: data})
	}
	if b.Find("SKILL.md") == nil {
		prefix := ""
		for _, f := range b.Files {
			if strings.HasSuffix(f.Path, "/SKILL.md") {
				if prefix != "" {
					return b, ErrPackage
				}
				prefix = strings.TrimSuffix(f.Path, "SKILL.md")
			}
		}
		if prefix == "" {
			return b, ErrPackage
		}
		for i := range b.Files {
			if !strings.HasPrefix(b.Files[i].Path, prefix) {
				return b, ErrPackage
			}
			b.Files[i].Path = strings.TrimPrefix(b.Files[i].Path, prefix)
		}
	}
	return b, b.Validate()
}

func Analyze(b Bundle) (Report, error) {
	report := Report{Qualification: "STATIC_ONLY", Files: []FileInfo{}, LicenseFiles: []string{}, RequestedTools: []string{}, ScriptFiles: []string{}, Findings: []string{}}
	if err := b.Validate(); err != nil {
		return report, err
	}
	data := b.Find("SKILL.md")
	if !utf8.Valid(data) || len(data) > 256*1024 {
		return report, ErrPackage
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return report, ErrPackage
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 || end > 8192 {
		return report, ErrPackage
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &node); err != nil {
		return report, ErrPackage
	}
	nodes := 0
	var validNode func(*yaml.Node, int) bool
	validNode = func(n *yaml.Node, depth int) bool {
		nodes++
		if nodes > 256 || depth > 6 || n.Kind == yaml.AliasNode || (strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!")) {
			return false
		}
		for _, child := range n.Content {
			if !validNode(child, depth+1) {
				return false
			}
		}
		return true
	}
	if !validNode(&node, 0) {
		return report, ErrPackage
	}
	var meta struct {
		Name        string    `yaml:"name"`
		Description string    `yaml:"description"`
		License     string    `yaml:"license"`
		Tools       yaml.Node `yaml:"allowed-tools"`
	}
	if err := node.Decode(&meta); err != nil {
		return report, ErrPackage
	}
	if !skillName.MatchString(meta.Name) || len(meta.Name) > 64 || strings.TrimSpace(meta.Description) == "" || utf8.RuneCountInString(meta.Description) > 1024 || len(meta.License) > 1000 || strings.TrimSpace(text[4+end+5:]) == "" {
		return report, ErrPackage
	}
	report.Name = meta.Name
	report.Description = meta.Description
	report.LicenseClaim = meta.License
	tools, err := declaredTools(meta.Tools)
	if err != nil {
		return report, err
	}
	report.RequestedTools = tools
	raw, _ := json.Marshal(b)
	report.Hash = Hash(raw)
	for _, f := range b.Files {
		report.Files = append(report.Files, FileInfo{Path: f.Path, Hash: Hash(f.Data), Size: len(f.Data), Text: utf8.Valid(f.Data) && !bytes.ContainsRune(f.Data, 0)})
		basename := strings.ToLower(path.Base(f.Path))
		if strings.HasPrefix(basename, "license") || strings.HasPrefix(basename, "licence") || strings.HasPrefix(basename, "upstream-license") {
			report.LicenseFiles = append(report.LicenseFiles, f.Path)
		}
		if strings.HasPrefix(f.Path, "scripts/") || slices.Contains([]string{".py", ".js", ".sh", ".ps1", ".bat", ".exe", ".dll", ".wasm"}, strings.ToLower(path.Ext(f.Path))) {
			report.ScriptFiles = append(report.ScriptFiles, f.Path)
		}
	}
	if len(report.RequestedTools) > 0 {
		report.Findings = append(report.Findings, "包内工具声明仅记录为请求；不会授予工具或执行权限")
	}
	if len(report.ScriptFiles) > 0 {
		report.Findings = append(report.Findings, "包含可执行内容；未执行，必须通过隔离环境和独立评测")
	}
	if len(report.LicenseFiles) == 0 {
		report.Findings = append(report.Findings, "缺少包内许可全文；声明不等于已取得使用授权")
	}
	if meta.License == "" {
		report.Findings = append(report.Findings, "未声明许可，保持待核对")
	}
	if b.Find("input.schema.json") == nil || b.Find("output.schema.json") == nil {
		report.Findings = append(report.Findings, "缺少平台输入/输出契约，尚不能绑定生产任务")
	}
	report.Findings = append(report.Findings, "静态检查不证明方法效果、许可适用性或模型能力；不得据此标为生产合格")
	return report, nil
}

// A declaration describes requested capabilities; it never grants permission.
func declaredTools(node yaml.Node) ([]string, error) {
	result := []string{}
	if node.Kind == 0 {
		return result, nil
	}
	values := []*yaml.Node{&node}
	if node.Kind == yaml.SequenceNode {
		values = node.Content
	}
	for _, value := range values {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || len(value.Value) > 1024 {
			return nil, ErrPackage
		}
		result = append(result, strings.Fields(value.Value)...)
	}
	if len(result) > 64 {
		return nil, ErrPackage
	}
	return result, nil
}

type Conflict struct {
	Path           string `json:"path"`
	Base           []byte `json:"base"`
	Upstream       []byte `json:"upstream"`
	Local          []byte `json:"local"`
	BaseExists     bool   `json:"base_exists"`
	UpstreamExists bool   `json:"upstream_exists"`
	LocalExists    bool   `json:"local_exists"`
}

type fileState struct {
	data   []byte
	exists bool
}

func (b Bundle) state(name string) fileState {
	for _, file := range b.Files {
		if file.Path == name {
			return fileState{data: file.Data, exists: true}
		}
	}
	return fileState{}
}

func (f fileState) equal(other fileState) bool {
	return f.exists == other.exists && bytes.Equal(f.data, other.data)
}

func Merge(base, upstream, local Bundle, choices map[string]string) (Bundle, []Conflict, error) {
	for _, b := range []*Bundle{&base, &upstream, &local} {
		if err := b.Validate(); err != nil {
			return Bundle{}, nil, err
		}
	}
	keys := map[string]bool{}
	for _, b := range []Bundle{base, upstream, local} {
		for _, f := range b.Files {
			keys[f.Path] = true
		}
	}
	result := Bundle{Files: []File{}}
	conflicts := []Conflict{}
	for name := range choices {
		if !keys[name] {
			return result, nil, ErrPackage
		}
	}
	for name := range keys {
		old, up, ours := base.state(name), upstream.state(name), local.state(name)
		var chosen fileState
		switch {
		case up.equal(old):
			chosen = ours
		case ours.equal(old), up.equal(ours):
			chosen = up
		default:
			switch choices[name] {
			case "local":
				chosen = ours
			case "upstream":
				chosen = up
			case "delete":
				chosen = fileState{}
			case "":
				conflicts = append(conflicts, Conflict{Path: name, Base: old.data, Upstream: up.data, Local: ours.data, BaseExists: old.exists, UpstreamExists: up.exists, LocalExists: ours.exists})
				chosen = ours
			default:
				return result, nil, fmt.Errorf("未知合并选择: %w", ErrPackage)
			}
		}
		if chosen.exists {
			result.Files = append(result.Files, File{Path: name, Data: chosen.data})
		}
	}
	slices.SortFunc(conflicts, func(a, b Conflict) int { return strings.Compare(a.Path, b.Path) })
	return result, conflicts, result.Validate()
}
