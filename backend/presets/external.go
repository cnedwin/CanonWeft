package presets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"novelstudio/internal/skills"
	"regexp"
	"sort"
	"strings"
)

// External content is untrusted source material, never executable configuration.
//
//go:embed all:external
var externalFiles embed.FS

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ExternalEntry struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Source skills.RemoteSource `json:"-"`
	Report skills.Report       `json:"-"`
	Issue  string              `json:"issue,omitempty"`
}
type snapshotManifest struct {
	Repository string   `json:"repository"`
	Commit     string   `json:"commit"`
	Skills     []string `json:"skills"`
	Files      []struct {
		Path string `json:"path"`
		Size int    `json:"size"`
		Hash string `json:"sha256"`
	} `json:"files"`
}

func readSnapshot(files fs.FS, commit string) (snapshotManifest, error) {
	var m snapshotManifest
	if !commitPattern.MatchString(commit) {
		return m, skills.ErrPackage
	}
	root := "external/shuohao/" + commit + "/"
	raw, err := fs.ReadFile(files, root+"manifest.json")
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Commit != commit || m.Repository != "eternityspring/shuohao-skills" || len(m.Files) == 0 || len(m.Files) > 2000 {
		return m, skills.ErrPackage
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range m.Files {
		if !skills.SafePath(f.Path) || seen[strings.ToLower(f.Path)] {
			return m, skills.ErrPackage
		}
		seen[strings.ToLower(f.Path)] = true
		data, err := fs.ReadFile(files, root+"upstream/"+f.Path)
		if err != nil || len(data) != f.Size || f.Size > skills.MaxFileBytes {
			return m, skills.ErrPackage
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.Hash {
			return m, skills.ErrPackage
		}
		total += f.Size
	}
	if total > 32*1024*1024 || !seen["license"] || !seen["notice"] {
		return m, skills.ErrPackage
	}
	count := 0
	err = fs.WalkDir(files, root+"upstream", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			count++
			if !seen[strings.ToLower(strings.TrimPrefix(path, root+"upstream/"))] {
				return skills.ErrPackage
			}
		}
		return nil
	})
	if err != nil || count != len(m.Files) {
		return m, skills.ErrPackage
	}
	names := map[string]bool{}
	for _, name := range m.Skills {
		if !skillNamePattern.MatchString(name) || names[name] || !seen["skills/"+name+"/skill.md"] {
			return m, skills.ErrPackage
		}
		names[name] = true
	}
	for p := range seen {
		parts := strings.Split(p, "/")
		if len(parts) == 3 && parts[0] == "skills" && parts[2] == "skill.md" && !names[parts[1]] {
			return m, skills.ErrPackage
		}
	}
	return m, nil
}

func ExternalCatalog() ([]ExternalEntry, error) {
	return externalCatalog(externalFiles)
}

func externalCatalog(files fs.FS) ([]ExternalEntry, error) {
	dirs, err := fs.ReadDir(files, "external/shuohao")
	if err != nil {
		return nil, err
	}
	result := []ExternalEntry{}
	for _, dir := range dirs {
		if !dir.IsDir() || !commitPattern.MatchString(dir.Name()) {
			continue
		}
		m, err := readSnapshot(files, dir.Name())
		if err != nil {
			result = append(result, ExternalEntry{ID: "shuohao-" + dir.Name() + "-snapshot", Name: "shuohao-snapshot", Source: skills.RemoteSource{Repository: "eternityspring/shuohao-skills", Commit: dir.Name()}, Issue: "快照完整性检查未通过，已隔离；其他方法仍可使用。"})
			continue
		}
		for _, name := range m.Skills {
			entry := ExternalEntry{ID: "shuohao-" + m.Commit + "-" + name, Name: name, Source: skills.RemoteSource{Repository: m.Repository, Commit: m.Commit, Path: "skills/" + name, URL: "https://github.com/" + m.Repository + "/tree/" + m.Commit + "/skills/" + name}}
			bundle, readErr := snapshotBundle(files, m, name)
			if readErr == nil {
				entry.Report, readErr = skills.Analyze(bundle)
			}
			if readErr != nil {
				entry.Issue = "包格式或方法声明检查未通过，已隔离；原件保留供审查。"
			}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func ExternalBundle(id string) (skills.Bundle, skills.RemoteSource, error) {
	b := skills.Bundle{Files: []skills.File{}}
	var source skills.RemoteSource
	if !strings.HasPrefix(id, "shuohao-") || len(id) < 50 || id[48] != '-' {
		return b, source, skills.ErrPackage
	}
	commit, name := id[8:48], id[49:]
	if !commitPattern.MatchString(commit) || !skillNamePattern.MatchString(name) {
		return b, source, skills.ErrPackage
	}
	m, err := readSnapshot(externalFiles, commit)
	if err != nil {
		return b, source, err
	}
	found := false
	for _, n := range m.Skills {
		if n == name {
			found = true
		}
	}
	if !found {
		return b, source, skills.ErrPackage
	}
	b, err = snapshotBundle(externalFiles, m, name)
	source = skills.RemoteSource{Repository: m.Repository, Commit: commit, Path: "skills/" + name, URL: "https://github.com/" + m.Repository + "/tree/" + commit + "/skills/" + name}
	return b, source, err
}

func snapshotBundle(files fs.FS, m snapshotManifest, name string) (skills.Bundle, error) {
	b := skills.Bundle{Files: []skills.File{}}
	prefix := "skills/" + name + "/"
	for _, f := range m.Files {
		if !strings.HasPrefix(f.Path, prefix) && f.Path != "LICENSE" && f.Path != "NOTICE" {
			continue
		}
		data, err := fs.ReadFile(files, "external/shuohao/"+m.Commit+"/upstream/"+f.Path)
		if err != nil {
			return b, err
		}
		b.Files = append(b.Files, skills.File{Path: strings.TrimPrefix(f.Path, prefix), Data: data})
	}
	return b, b.Validate()
}
