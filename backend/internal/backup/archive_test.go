package backup

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"testing"
)

type archiveEntry struct {
	header zip.FileHeader
	body   []byte
}

func validArchiveEntries(t *testing.T) []archiveEntry {
	t.Helper()
	raw := archiveBytes(t, emptyArchive(t))
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("attack baseline is not valid", err)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []archiveEntry
	for _, f := range z.File {
		reader, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, archiveEntry{f.FileHeader, body})
	}
	return entries
}
func encodeEntries(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, entry := range entries {
		h := entry.header
		w, err := z.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Every case starts from a fully valid archive and changes one entry property.
// Checking the specific guard failure prevents an unrelated missing-manifest or
// inventory error from disguising a removed security check.
func TestArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name, entry, renamed, want string
		mode                       os.FileMode
		inflate                    bool
	}{
		{name: "traversal", entry: "data.json", renamed: "../escape", want: "unrecognized archive entry"},
		{name: "absolute", entry: "data.json", renamed: "/absolute", want: "unrecognized archive entry"},
		{name: "backslash", entry: "data.json", renamed: "blobs\\escape", want: "unrecognized archive entry"},
		{name: "extra-hook", renamed: "restore.ps1", mode: 0700, want: "unrecognized archive entry"},
		{name: "symlink", entry: "manifest.json", mode: os.ModeSymlink | 0600, want: "duplicate or unsafe entry"},
		{name: "executable-mode", entry: "manifest.json", mode: 0700, want: "duplicate or unsafe entry"},
		{name: "inflation", entry: "manifest.json", inflate: true, want: "entry expansion limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := validArchiveEntries(t)
			if tc.entry == "" {
				header := zip.FileHeader{Name: tc.renamed, Method: zip.Deflate}
				header.SetMode(tc.mode)
				entries = append(entries, archiveEntry{header, []byte("Write-Output 'untrusted hook'")})
			}
			for i := range entries {
				if entries[i].header.Name != tc.entry {
					continue
				}
				if tc.renamed != "" {
					entries[i].header.Name = tc.renamed
				}
				if tc.mode != 0 {
					entries[i].header.SetMode(tc.mode)
				}
				if tc.inflate {
					entries[i].body = append(entries[i].body, bytes.Repeat([]byte(" "), 2<<20)...)
				}
			}
			raw := encodeEntries(t, entries)
			if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || err.Error() != tc.want {
				t.Fatalf("guard missing: want %q, got %v", tc.want, err)
			}
		})
	}
}
func TestArchiveRejectsDuplicateEntry(t *testing.T) {
	entries := validArchiveEntries(t)
	for _, entry := range entries {
		if entry.header.Name == "manifest.json" {
			entries = append(entries, entry)
			break
		}
	}
	raw := encodeEntries(t, entries)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || err.Error() != "duplicate or unsafe entry" {
		t.Fatalf("duplicate guard missing: %v", err)
	}
}
