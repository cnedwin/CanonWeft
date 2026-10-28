package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type missingCapturedBody struct {
	storage.BlobStore
	hash string
}

func (s missingCapturedBody) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	if hash == s.hash {
		return nil, errors.New("simulated missing original body")
	}
	return s.BlobStore.ReadObject(ctx, project, hash)
}

func TestRetentionAdoptionCapturesOriginalCandidateAndReleaseViews(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "采用前后的来源", "capture-adoption")
	bid := branch(b, project, "正史")
	candidate := propose(b, project, bid, "", "candidate", originalSnapshot())
	rid := resource(b, project, bid, "第一章")
	release := prepareRelease(b, project, bid, "", "", "chapter", []map[string]any{{"resource_id": rid, "text": "雨停后，医师在码头等候来信。"}})
	path := "/api/projects/" + project + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	refs := []any{workingChoice(t, b, path, "ip_candidate", candidate), workingChoice(t, b, path, "release_candidate", release)}
	pack := b.request("POST", path+"/search", map[string]any{"query": "码头医师", "purpose": "author", "limit": 1, "include_working": true, "working_refs": refs}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	if err := db.QueryRow("SELECT workspace_id,role FROM users WHERE id=?", actor.ID).Scan(&actor.WorkspaceID, &actor.Role); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	ctx := context.Background()
	knowledge := privateJobs(t, db).Knowledge
	second, err := knowledge.Search(ctx, actor, project, bid, service.SearchInput{Query: "码头医师", Purpose: "author", Limit: 1, IncludeWorking: true, WorkingRefs: []domain.WorkingRef{
		{Kind: "ip_candidate", ID: candidate, Version: refs[0].(map[string]any)["version"].(string)},
		{Kind: "release_candidate", ID: release, Version: refs[1].(map[string]any)["version"].(string)},
	}})
	if err != nil {
		t.Fatal("capture working inputs on context creation", err)
	}
	// The consumer and its exact PREPARED sources must exist before a scan or
	// adoption. Capturing only source values cannot satisfy this assertion.
	assertPreparedContextPins(t, db, project, second.ID)
	var bodyHash string
	if err := db.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(manifest,'$.artifacts[0].hash')) FROM artifact_releases WHERE id=?", release).Scan(&bodyHash); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ip.Blobs = missingCapturedBody{BlobStore: ip.Blobs, hash: bodyHash}
	if _, err := ip.AdoptRelease(ctx, actor, project, release); err == nil {
		t.Fatal("adoption accepted missing source body")
	}
	ip.Blobs = storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	var state string
	var after int
	if err := db.QueryRow("SELECT status FROM artifact_releases WHERE id=?", release).Scan(&state); err != nil || state != "PREPARED" {
		t.Fatal("failed source proof changed adoption", state, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&after); err != nil || after != before {
		t.Fatal("failed adoption committed partial capture", before, after, err)
	}
	// No full source registration before adoption: the transition itself must
	// preserve the PREPARED views copied into the earlier context.
	if _, err := ip.Adopt(ctx, actor, project, bid, candidate); err != nil {
		t.Fatal("candidate adoption", err)
	}
	if _, err := ip.AdoptRelease(ctx, actor, project, release); err != nil {
		t.Fatal("release adoption", err)
	}
	for _, target := range []struct{ table, id, field string }{{"ip_revisions", candidate, "payload"}, {"artifact_releases", release, "manifest"}} {
		owner, err := retention.RowOwnerKey(target.table, map[string]string{"id": target.id})
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"PREPARED", "READY"} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id WHERE r.project_id=? AND r.owner_table=? AND r.owner_key=? AND r.field_key=? AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.working_projection.state'))=?", project, target.table, owner, target.field, state).Scan(&count); err != nil || count != 1 {
				t.Fatal("transition proof missing", target.table, state, count, err)
			}
		}
	}
	report, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || report.UnboundSources != 0 {
		t.Fatal("original context cannot bind adopted sources", report, err)
	}
	assertPreparedContextPins(t, db, project, pack["id"].(string))
	assertPreparedContextPins(t, db, project, second.ID)
	if _, err := ip.PublishRelease(ctx, actor, project, release); err != nil {
		t.Fatal("release publication", err)
	}
	if _, err := ip.Publish(ctx, actor, project, bid, candidate); err != nil {
		t.Fatal("candidate publication", err)
	}
	for _, table := range []string{"ip_revisions", "artifact_releases"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id WHERE r.project_id=? AND r.owner_table=? AND r.field_key IN ('payload','manifest') AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.working_projection.state'))='PUBLISHED'", project, table).Scan(&count); err != nil || count != 1 {
			t.Fatal("published source view missing", table, count, err)
		}
	}
	if report, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("publication source history diverged", report, err)
	}
	assertPreparedContextPins(t, db, project, pack["id"].(string))
	assertPreparedContextPins(t, db, project, second.ID)
}

func assertPreparedContextPins(t *testing.T, db *sql.DB, project, contextID string) {
	t.Helper()
	owner, err := retention.RowOwnerKey("context_packages", map[string]string{"id": contextID})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='context_packages' AND r.owner_key=?", project, owner).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &version); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pin := range version.Reference.Sources {
		if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=?", pin.ReferenceID, pin.Revision).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var source retention.ReferenceVersion
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			t.Fatal(err)
		}
		if source.WorkingProjection == nil {
			continue
		}
		if source.WorkingProjection.Kind != "ip_candidate" && source.WorkingProjection.Kind != "release_manifest" && source.WorkingProjection.Kind != "release_artifact" {
			continue
		}
		if source.WorkingProjection.State != "PREPARED" {
			t.Fatal("historical context rebound to adopted view", source.WorkingProjection)
		}
		if source.WorkingProjection.Kind == "ip_candidate" && len(source.WorkingProjection.Dependencies) == 0 {
			t.Fatal("candidate omitted original delta evidence")
		}
		seen[source.WorkingProjection.Kind] = true
	}
	for _, kind := range []string{"ip_candidate", "release_manifest", "release_artifact"} {
		if !seen[kind] {
			t.Fatal("original view missing", kind)
		}
	}
}
