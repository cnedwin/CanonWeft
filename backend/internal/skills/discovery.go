package skills

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strings"
)

type DiscoveryInventory struct {
	Commit string   `json:"commit"`
	Paths  []string `json:"paths"`
	Total  int      `json:"total"`
}

func ValidRemoteSource(input RemoteRequest) bool {
	return repositoryPattern.MatchString(input.Repository) && refPattern.MatchString(input.Ref) && !strings.Contains(input.Ref, "..") && (input.Path == "" || SafePath(input.Path))
}

// Discover inventories only a registered public repository. Keywords are applied
// locally and never become a GitHub search query or request parameter.
func (f *Fetcher) Discover(ctx context.Context, input RemoteRequest, keyword string, limit int) (DiscoveryInventory, error) {
	result := DiscoveryInventory{Paths: []string{}}
	if input.Ref == "" {
		input.Ref = "main"
	}
	if !ValidRemoteSource(input) || limit < 1 || limit > 1000 || len(keyword) > 120 {
		return result, ErrFetch
	}
	root := "https://api.github.com/repos/" + input.Repository
	raw, err := f.get(ctx, root+"/commits/"+url.PathEscape(input.Ref), 2*1024*1024)
	if err != nil {
		return result, err
	}
	var commit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if json.Unmarshal(raw, &commit) != nil || !shaPattern.MatchString(commit.SHA) || !shaPattern.MatchString(commit.Commit.Tree.SHA) {
		return result, ErrFetch
	}
	raw, err = f.get(ctx, root+"/git/trees/"+commit.Commit.Tree.SHA+"?recursive=1", 8*1024*1024)
	if err != nil {
		return result, err
	}
	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Mode string `json:"mode"`
		} `json:"tree"`
	}
	if json.Unmarshal(raw, &tree) != nil || tree.Truncated {
		return result, ErrFetch
	}
	prefix := input.Path
	if prefix != "" {
		prefix += "/"
	}
	for _, entry := range tree.Tree {
		if !strings.HasPrefix(entry.Path, prefix) || path.Base(entry.Path) != "SKILL.md" {
			continue
		}
		if !SafePath(entry.Path) || entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
			return result, ErrFetch
		}
		directory := path.Dir(entry.Path)
		if directory == "." {
			directory = ""
		}
		if keyword != "" && !strings.Contains(strings.ToLower(directory), strings.ToLower(keyword)) {
			continue
		}
		result.Paths = append(result.Paths, directory)
	}
	slices.Sort(result.Paths)
	result.Paths = slices.Compact(result.Paths)
	result.Total = len(result.Paths)
	if len(result.Paths) > limit {
		result.Paths = result.Paths[:limit]
	}
	result.Commit = commit.SHA
	return result, nil
}
