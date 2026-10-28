package backup

import (
	"bytes"
	"strings"
	"testing"
)

func TestPreviewArchiveIntegrityAndLegacyUpgrade(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "generation_previews", map[string]string{"generation_id": strings.Repeat("a", 32), "content": "未完成的私人文字", "sequence": "2", "content_hash": digest([]byte("未完成的私人文字"))})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	a.Data["generation_previews"][0][1] = []byte("changed without new hash")
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("corrupt preview accepted")
	}
	for _, version := range []int{24, 25} {
		a = emptyArchive(t)
		beforePrivateIndexes(a)
		a.Manifest.Schema = version
		a.Manifest.Migrations = a.Manifest.Migrations[:version]
		delete(a.Data, "generation_previews")
		raw = archiveBytes(t, a)
		converted, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(version, err)
		}
		if len(converted.Data["generation_previews"]) != 0 {
			t.Fatal("legacy preview manufactured")
		}
		a.Data["generation_previews"] = nil
		raw = archiveBytes(t, a)
		if _, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Fatal("legacy table injection accepted", version)
		}
	}
}
