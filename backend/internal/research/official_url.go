package research

import (
	"crypto/sha256"
	"encoding/binary"
	"net/url"
	"strings"
)

const OfficialSource = "official_reference"

// Deliberately exact hosts and public article paths. No suffix trust, arbitrary
// subdomain, URL credentials, query endpoint or redirect is granted by this list.
var officialPaths = map[string][]string{
	"www.gov.cn":       {"/zhengce/", "/xinwen/", "/lianbo/"},
	"www.npc.gov.cn":   {"/npc/", "/c2/"},
	"www.court.gov.cn": {"/zixun/xiangqing/"},
	"www.stats.gov.cn": {"/sj/", "/zs/"},
	"www.nist.gov":     {"/publications/", "/blogs/", "/news-events/news/"},
	"www.archives.gov": {"/research/", "/founding-docs/"},
	"www.loc.gov":      {"/item/"},
}

func CanonicalOfficialURL(input string) (string, error) {
	if len(input) > 2000 {
		return "", ErrSource
	}
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" {
		return "", ErrSource
	}
	prefixes, ok := officialPaths[u.Host]
	if !ok || strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(u.Path, "//") {
		return "", ErrSource
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", ErrSource
		}
	}
	encoded := strings.ToLower(u.EscapedPath())
	if strings.Contains(encoded, "%2f") || strings.Contains(encoded, "%5c") || strings.Contains(encoded, "%00") {
		return "", ErrSource
	}
	allowed := false
	for _, prefix := range prefixes {
		allowed = allowed || strings.HasPrefix(u.Path, prefix)
	}
	if !allowed {
		return "", ErrSource
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), nil
}

// The legacy numeric field is only an opaque collector key here. The canonical
// URL and whole-page fingerprint are the authoritative source identity.
func OfficialReferenceID(target string) int64 {
	hash := sha256.Sum256([]byte(target))
	return int64(binary.BigEndian.Uint64(hash[:8])&((1<<52)-1)) + 1
}

func approvedPublicEndpoint(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	if u.Host == "www.ebi.ac.uk" || u.Host == "www.wikidata.org" {
		return true
	}
	if _, ok := officialPaths[u.Host]; !ok {
		return false
	}
	if u.Path == "/robots.txt" && u.RawQuery == "" {
		return true
	}
	canonical, err := CanonicalOfficialURL(u.String())
	return err == nil && canonical == u.String()
}
