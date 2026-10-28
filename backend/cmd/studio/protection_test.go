package main

import (
	"path/filepath"
	"testing"
)

func TestAutomaticProtectionDirectoryIsDatabaseScoped(t *testing.T) {
	base := t.TempDir()
	one, err := protectionDirectory(filepath.Join(base, "blobs"), "studio", "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := protectionDirectory(filepath.Join(base, "blobs"), "studio_test", "")
	if err != nil || one == two || one != filepath.Join(base, "backups", "studio") {
		t.Fatalf("database protection directories collide: %q %q %v", one, two, err)
	}
	for _, tc := range []struct{ blobs, database, configured string }{
		{"relative", "studio", ""},
		{base, "../outside", ""},
		{base, "studio", "relative"},
	} {
		if _, err := protectionDirectory(tc.blobs, tc.database, tc.configured); err == nil {
			t.Fatalf("unsafe directory accepted: %+v", tc)
		}
	}
}
