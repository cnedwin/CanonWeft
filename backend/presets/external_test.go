package presets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"novelstudio/internal/skills"
	"strings"
	"testing"
	"testing/fstest"
)

func TestShuohaoCatalogRetainsFullPackagesAndPinnedProvenance(t *testing.T) {
	entries, err := ExternalCatalog()
	if err != nil || len(entries) < 7 {
		t.Fatal("missing seven upstream skills", len(entries), err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		b, source, err := ExternalBundle(entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		report, err := skills.Analyze(b)
		if err != nil || report.Name != entry.Name || report.Qualification != "STATIC_ONLY" {
			t.Fatal(report, err)
		}
		if len(b.Find("LICENSE")) == 0 || len(b.Find("NOTICE")) == 0 || len(report.ScriptFiles) == 0 {
			t.Fatal("incomplete upstream bundle", entry.Name)
		}
		if len(source.Commit) != 40 || source.Repository != "eternityspring/shuohao-skills" || source.Path != "skills/"+entry.Name {
			t.Fatal(source)
		}
		found[entry.Name] = true
	}
	for _, name := range []string{"character-refs", "novel-art", "novel-characters", "novel-outline", "novel-script", "novel-staging", "novel-storyboard"} {
		if !found[name] {
			t.Fatal("missing", name)
		}
	}
	for _, bad := range []string{"../README.md", "shuohao-latest-novel-outline", ""} {
		if _, _, err := ExternalBundle(bad); err == nil {
			t.Fatal("invalid ID accepted", bad)
		}
	}
}

func TestBrokenUpstreamCandidatesDoNotHideExistingCatalog(t *testing.T) {
	const commit = "6dbddc64fed306fc96d22674cd36b717a8190d90"
	root := "external/shuohao/" + commit + "/"
	fixture := fstest.MapFS{}
	if err := fs.WalkDir(externalFiles, "external", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// This fixture exercises one pinned good snapshot and a broken update.
		// Future embedded snapshots are covered by the full catalog test above.
		if entry.IsDir() && path != "external" && path != "external/shuohao" && path != "external/shuohao/"+commit && !strings.HasPrefix(path, root) {
			return fs.SkipDir
		}
		if !entry.IsDir() {
			data, readErr := externalFiles.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			fixture[path] = &fstest.MapFile{Data: data}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bad := []byte("---\nname: character-refs\n---\nmissing description")
	fixture[root+"upstream/skills/character-refs/SKILL.md"] = &fstest.MapFile{Data: bad}
	var m snapshotManifest
	if err := json.Unmarshal(fixture[root+"manifest.json"].Data, &m); err != nil {
		t.Fatal(err)
	}
	for i := range m.Files {
		if m.Files[i].Path == "skills/character-refs/SKILL.md" {
			sum := sha256.Sum256(bad)
			m.Files[i].Hash = hex.EncodeToString(sum[:])
			m.Files[i].Size = len(bad)
		}
	}
	raw, _ := json.Marshal(m)
	fixture[root+"manifest.json"] = &fstest.MapFile{Data: raw}
	fixture["external/shuohao/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/manifest.json"] = &fstest.MapFile{Data: []byte("broken JSON")}
	entries, err := externalCatalog(fixture)
	if err != nil || len(entries) != 8 {
		t.Fatal("bad update hid existing catalog", len(entries), err)
	}
	valid, quarantined := 0, 0
	for _, e := range entries {
		if e.Issue == "" {
			valid++
		} else {
			quarantined++
		}
	}
	if valid != 6 || quarantined != 2 {
		t.Fatal(valid, quarantined)
	}
}

func TestNameSeedProvenanceDoesNotInventCultureOrPopularity(t *testing.T) {
	b, err := Bundle("character-naming")
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Commit  string `json:"source_commit"`
		License string `json:"license"`
		Hash    string `json:"source_sha256"`
		Records []struct {
			ID      string `json:"entity_id"`
			Kind    string `json:"kind"`
			Culture string `json:"culture"`
			State   string `json:"review_state"`
		} `json:"records"`
	}
	if err := json.Unmarshal(b.Find("references/surname-seeds.json"), &seed); err != nil {
		t.Fatal(err)
	}
	if len(seed.Records) != 500 || len(seed.Commit) != 40 || len(seed.Hash) != 64 || seed.License != "CC0-1.0" {
		t.Fatal("incomplete seed provenance")
	}
	for _, r := range seed.Records {
		if r.ID == "" || r.Kind != "surname" || r.Culture != "unknown" || r.State != "unverified" {
			t.Fatal("seed overstated", r)
		}
	}
}

func TestCreativeResearchMethodsAreBundled(t *testing.T) {
	for _, name := range []string{"character-naming", "genre-research"} {
		b, err := Bundle(name)
		if err != nil || len(b.Find("input.schema.json")) == 0 || len(b.Find("output.schema.json")) == 0 {
			t.Fatal(name, err)
		}
	}
}
