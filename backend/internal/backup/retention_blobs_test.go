package backup

import (
	"bytes"
	"strings"
	"testing"

	"novelstudio/internal/retention"
)

func retainedBlobArchiveFixture(t *testing.T) (*Archive, string, []byte) {
	t.Helper()
	a, _ := archiveCleanupFixture(t)
	project, branch := strings.Repeat("a", 32), strings.Repeat("1", 32)
	body := []byte("retained original source input")
	hash := digest(body)
	testRow(a, "branches", map[string]string{"id": branch, "project_id": project})
	testRow(a, "generation_tasks", map[string]string{"id": strings.Repeat("2", 32), "project_id": project, "workspace_id": strings.Repeat("b", 32), "actor_id": strings.Repeat("c", 32), "branch_id": branch, "frozen": `{"input_hash":"` + hash + `"}`, "state": "PREPARED"})
	path := "blobs/" + project + "/" + hash
	a.Blobs[path] = body
	return a, path, body
}

func TestRetentionArchiveRetainedBlobSelectionKeepsSharedAndPreservedReferences(t *testing.T) {
	hash := digest([]byte("still retained after enclosing raw text expiry"))
	project, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	// The collection has already resolved enclosing expiry, author roots and
	// immutable nested pins. Selection must not reapply TTL or parse cleared JSON.
	materials := map[string][]retention.MaterialReference{
		project: {{Storage: retention.Blob, ObjectHash: hash, ContentRoot: true}, {Storage: retention.Blob, ObjectHash: hash}, {Storage: retention.Inline}},
		other:   {{Storage: retention.Blob, ObjectHash: hash}},
	}
	refs, err := archiveRetainedBlobReferences(materials)
	if err != nil || len(refs) != 2 || !refs["blobs/"+project+"/"+hash] || !refs["blobs/"+other+"/"+hash] {
		t.Fatal("shared or preserved source lost its project identity", refs, err)
	}
	for _, bad := range []map[string][]retention.MaterialReference{
		{"../private": {{Storage: retention.Blob, ObjectHash: hash}}},
		{project: {{Storage: retention.Blob, ObjectHash: "../private"}}},
	} {
		if _, err := archiveRetainedBlobReferences(bad); err == nil {
			t.Fatal("unsafe file identity accepted")
		}
	}
}

func TestRetentionArchiveRequiresExactRetainedFilesBeforeRegistration(t *testing.T) {
	a, path, _ := retainedBlobArchiveFixture(t)
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("valid unregistered retained file rejected", err)
	}
	delete(a.Blobs, path)
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "blob") {
		t.Fatal("missing retained source file passed archive gate", err)
	}
}

func TestRetentionArchiveRejectsUnreferencedDeletedFiles(t *testing.T) {
	a, _ := objectCleanupFixture(t)
	body := []byte("already removed unreferenced content")
	a.Blobs["blobs/"+strings.Repeat("a", 32)+"/"+digest(body)] = body
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "blob") {
		t.Fatal("unreferenced private file passed archive gate", err)
	}
}

func TestRetentionArchiveFileScopeCannotMoveBetweenProjects(t *testing.T) {
	a, path, body := retainedBlobArchiveFixture(t)
	delete(a.Blobs, path)
	a.Blobs["blobs/"+strings.Repeat("f", 32)+"/"+digest(body)] = body
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatal("same hash in wrong project replaced original file", err)
	}
}
