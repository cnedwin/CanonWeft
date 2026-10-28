package skills

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

var ErrFetch = errors.New("公开技能来源抓取未完成；未激活任何内容")
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,119}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type RemoteRequest struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Path       string `json:"path"`
}
type RemoteSource struct {
	Repository string
	Commit     string
	Path       string
	URL        string
}
type Fetcher struct{ client *http.Client }

func PublicAddress(value netip.Addr) bool {
	value = value.Unmap()
	if !value.IsGlobalUnicast() || value.IsPrivate() || value.IsLoopback() || value.IsLinkLocalUnicast() || value.IsUnspecified() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(prefix).Contains(value) {
			return false
		}
	}
	return true
}
func NewFetcher(transport http.RoundTripper) *Fetcher {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != "443" || (host != "api.github.com" && host != "raw.githubusercontent.com") {
				return nil, ErrFetch
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 {
				return nil, ErrFetch
			}
			for _, ip := range ips {
				if !PublicAddress(ip) {
					return nil, ErrFetch
				}
			}
			dialer := net.Dialer{Timeout: 10 * time.Second}
			// Connect to the already checked IP; do not resolve the hostname a second time.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		transport = t
	}
	return &Fetcher{client: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrFetch }}}
}
func (f *Fetcher) get(ctx context.Context, location string, max int) ([]byte, error) {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || (parsed.Host != "api.github.com" && parsed.Host != "raw.githubusercontent.com") {
		return nil, ErrFetch
	}
	request, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return nil, ErrFetch
	}
	request.Header.Set("User-Agent", "NovelStudio-SkillResearch/1")
	if parsed.Host == "api.github.com" {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, ErrFetch
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, ErrFetch
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(max)+1))
	if err != nil || len(data) > max {
		return nil, ErrFetch
	}
	return data, nil
}
func gitBlobHash(data []byte) string {
	hash := sha1.New()
	fmt.Fprintf(hash, "blob %d\x00", len(data))
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}
func (f *Fetcher) Fetch(ctx context.Context, input RemoteRequest) (Bundle, RemoteSource, error) {
	bundle := Bundle{Files: []File{}}
	source := RemoteSource{}
	if input.Ref == "" {
		input.Ref = "main"
	}
	if !repositoryPattern.MatchString(input.Repository) || !refPattern.MatchString(input.Ref) || strings.Contains(input.Ref, "..") || (input.Path != "" && !SafePath(input.Path)) {
		return bundle, source, ErrFetch
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	root := "https://api.github.com/repos/" + input.Repository
	raw, err := f.get(ctx, root+"/commits/"+url.PathEscape(input.Ref), 2*1024*1024)
	if err != nil {
		return bundle, source, err
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
		return bundle, source, ErrFetch
	}
	raw, err = f.get(ctx, root+"/git/trees/"+commit.Commit.Tree.SHA+"?recursive=1", 8*1024*1024)
	if err != nil {
		return bundle, source, err
	}
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size int    `json:"size"`
	}
	var tree struct {
		Truncated bool    `json:"truncated"`
		Tree      []entry `json:"tree"`
	}
	if json.Unmarshal(raw, &tree) != nil || tree.Truncated {
		return bundle, source, ErrFetch
	}
	prefix := input.Path
	if prefix != "" {
		prefix += "/"
	}
	total := 0
	for _, item := range tree.Tree {
		inside := strings.HasPrefix(item.Path, prefix)
		rootLicense := !inside && !strings.Contains(item.Path, "/") && (strings.HasPrefix(strings.ToLower(item.Path), "license") || strings.HasPrefix(strings.ToLower(item.Path), "licence"))
		if !inside && !rootLicense {
			continue
		}
		if !SafePath(item.Path) {
			return bundle, source, ErrFetch
		}
		if item.Type == "tree" {
			continue
		}
		if item.Type != "blob" || (item.Mode != "100644" && item.Mode != "100755") || item.Size < 0 || item.Size > MaxFileBytes || !shaPattern.MatchString(item.SHA) {
			return bundle, source, ErrFetch
		}
		if len(bundle.Files) >= MaxFiles || total+item.Size > MaxBundleBytes {
			return bundle, source, ErrFetch
		}
		// Ref is immutable; a moving default branch cannot mix files from revisions.
		location := (&url.URL{Scheme: "https", Host: "raw.githubusercontent.com", Path: "/" + input.Repository + "/" + commit.SHA + "/" + item.Path}).String()
		data, readErr := f.get(ctx, location, MaxFileBytes)
		if readErr != nil || len(data) != item.Size || gitBlobHash(data) != item.SHA {
			return bundle, source, ErrFetch
		}
		name := strings.TrimPrefix(item.Path, prefix)
		if rootLicense {
			name = "UPSTREAM-LICENSE-" + path.Base(item.Path)
		}
		bundle.Files = append(bundle.Files, File{Path: name, Data: data})
		total += len(data)
	}
	if err = bundle.Validate(); err != nil {
		return bundle, source, ErrFetch
	}
	source = RemoteSource{Repository: input.Repository, Commit: commit.SHA, Path: input.Path, URL: "https://github.com/" + input.Repository + "/tree/" + commit.SHA + "/" + input.Path}
	return bundle, source, nil
}
