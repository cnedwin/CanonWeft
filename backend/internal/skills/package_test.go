package skills

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func fixtureArchive(t *testing.T, files map[string]string, symlink string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for name, text := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if name == symlink {
			h.SetMode(os.ModeSymlink | 0777)
		}
		entry, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

const testSkill = "---\nname: test-skill\ndescription: Use when analyzing a text source.\nlicense: MIT\nallowed-tools: Bash(*)\n---\nRead references/rules.md. Propose results only."

func TestArchiveSymlinkEscapeAndDuplicatePathsRejected(t *testing.T) {
	for _, path := range []string{"../SKILL.md", "/SKILL.md", "C:/SKILL.md", "CON.txt", "references/../../outside", "references\\outside.txt"} {
		if _, err := ReadArchive(fixtureArchive(t, map[string]string{"SKILL.md": testSkill, path: "bad"}, "")); err == nil {
			t.Fatal("unsafe path accepted", path)
		}
	}
	if _, err := ReadArchive(fixtureArchive(t, map[string]string{"SKILL.md": testSkill, "references/link": "../../secret"}, "references/link")); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := ReadArchive(fixtureArchive(t, map[string]string{"SKILL.md": testSkill, "skill.md": "duplicate"}, "")); err == nil {
		t.Fatal("case-fold duplicate accepted")
	}
}

func TestPackageAnalysisRetainsFilesAndOnlyReportsRequestedTools(t *testing.T) {
	b, err := ReadArchive(fixtureArchive(t, map[string]string{"test-skill/SKILL.md": testSkill, "test-skill/references/rules.md": "Treat statements as claims.", "test-skill/scripts/run.py": "print('this must never run')", "test-skill/LICENSE": "MIT License\nCopyright Test Fixture"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Name != "test-skill" || len(report.Files) != 4 || len(report.RequestedTools) != 1 || len(report.ScriptFiles) != 1 || report.Qualification != "STATIC_ONLY" {
		t.Fatalf("lost package or forged qualification: %+v", report)
	}
	if report.Hash == "" || report.LicenseClaim != "MIT" || len(report.LicenseFiles) != 1 {
		t.Fatal("source/hash/license not recorded")
	}
}

func TestUpstreamMergePreservesLocalChangesAndRequiresConflictChoice(t *testing.T) {
	base := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill)}, {Path: "references/rules.md", Data: []byte("base")}}}
	local := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill + " Local changes.")}, {Path: "references/rules.md", Data: []byte("local")}}}
	up := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill)}, {Path: "references/rules.md", Data: []byte("upstream")}, {Path: "assets/template.txt", Data: []byte("new")}}}
	result, conflicts, err := Merge(base, up, local, nil)
	if err != nil || len(conflicts) != 1 || string(result.Find("SKILL.md")) != testSkill+" Local changes." || string(result.Find("assets/template.txt")) != "new" {
		t.Fatal("merge lost local edits", conflicts, err)
	}
	result, conflicts, err = Merge(base, up, local, map[string]string{"references/rules.md": "local"})
	if err != nil || len(conflicts) != 0 || string(result.Find("references/rules.md")) != "local" {
		t.Fatal("explicit resolution failed", err)
	}
}

func TestMergeDistinguishesEmptyFilesFromDeletedFiles(t *testing.T) {
	base := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill)}, {Path: "assets/empty.txt", Data: []byte{}}}}
	upstream := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill)}}}
	local := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte(testSkill)}, {Path: "assets/empty.txt", Data: []byte("local addition")}}}
	_, conflicts, err := Merge(base, upstream, local, nil)
	if err != nil || len(conflicts) != 1 {
		t.Fatal("deletion of an empty file silently discarded a concurrent edit", conflicts, err)
	}
	result, conflicts, err := Merge(base, upstream, local, map[string]string{"assets/empty.txt": "upstream"})
	if err != nil || len(conflicts) != 0 || result.Find("assets/empty.txt") != nil {
		t.Fatal("explicit deletion failed", err)
	}
	result, _, err = Merge(base, base, base, nil)
	if err != nil || len(result.Files) != 2 {
		t.Fatal("empty file was dropped", err)
	}
}
