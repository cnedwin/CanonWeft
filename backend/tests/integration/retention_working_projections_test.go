package integration

import (
	"context"
	"encoding/json"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionWorkingProjectionsBindIPAndReleaseSources(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "完整私人投影", "working-projection-origins")
	bid := branch(b, project, "正史")
	branchRoot := "/api/projects/" + project + "/branches/" + bid
	b.request("PUT", branchRoot+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 200)
	candidate := propose(b, project, bid, "", "projection-candidate", originalSnapshot())
	rid := resource(b, project, bid, "待发布资料")
	prepareRelease(b, project, bid, "", "", "projection-release", []map[string]any{{"resource_id": rid, "text": "药师在雨夜寻找她的旧友。"}})
	job := b.request("POST", branchRoot+"/knowledge/jobs", map[string]any{"operation_id": "projection-private-index", "kind": "working"}, 202)
	jobs := privateJobs(t, db)
	if state, err := jobs.Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	report, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || report.UnboundSources != 0 {
		t.Fatal("working projections have unresolved origins", report, err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var before retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &before); err != nil || len(before.Reference.Sources) < 4 {
		t.Fatal("missing typed working sources", before, err)
	}
	for _, table := range []string{"ip_drafts", "ip_revisions", "artifact_releases"} {
		found := false
		for _, pin := range before.Reference.Sources {
			var actual string
			if err := db.QueryRow("SELECT owner_table FROM retention_references WHERE id=?", pin.ReferenceID).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			found = found || actual == table
		}
		if !found {
			t.Fatal("missing source table", table)
		}
	}
	if repeated, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || repeated.UpdatedReferences != 0 || repeated.NewReferences != 0 {
		t.Fatal("unstable projection replay", repeated, err)
	}
	// Use the same production service under an explicit actor; the pending
	// migration fixture intentionally keeps ordinary HTTP startup gates closed.
	if _, err := jobs.Knowledge.IP.Adopt(ctx, domain.Actor{ID: actor}, project, bid, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", before.Reference.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var after retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &after); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before.Reference.Sources)
	bPins, _ := json.Marshal(after.Reference.Sources)
	if string(a) != string(bPins) {
		t.Fatal("adoption rebound original private inputs")
	}
}

func TestRetentionWorkingProjectionHistoryKeepsPreAdoptionPolicy(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "投影历史状态", "working-projection-history")
	bid := branch(b, project, "正史")
	branchRoot := "/api/projects/" + project + "/branches/" + bid
	b.request("PUT", branchRoot+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 200)
	candidate := propose(b, project, bid, "", "history-ip-candidate", originalSnapshot())
	rid := resource(b, project, bid, "历史资料")
	release := prepareRelease(b, project, bid, "", "", "history-artifact-candidate", []map[string]any{{"resource_id": rid, "text": "药师曾在雨夜寻找故人。"}})
	job := b.request("POST", branchRoot+"/knowledge/jobs", map[string]any{"operation_id": "projection-old-index", "kind": "working"}, 202)
	jobs := privateJobs(t, db)
	ctx := context.Background()
	if state, err := jobs.Execute(ctx, job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	var index string
	if err := db.QueryRow("SELECT id FROM working_indexes WHERE project_id=?", project).Scan(&index); err != nil {
		t.Fatal(err)
	}
	restoreIndex := holdRetentionIndexRows(t, db, "working_indexes", "id", index)
	restoreChunks := holdRetentionIndexRows(t, db, "working_chunks", "index_id", index)
	if _, err := db.Exec("DELETE FROM working_chunks WHERE index_id=?", index); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM working_indexes WHERE id=?", index); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Knowledge.IP.Adopt(ctx, domain.Actor{ID: actor}, project, bid, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Knowledge.IP.AdoptRelease(ctx, domain.Actor{ID: actor}, project, release); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE ip_drafts SET revision=2 WHERE branch_id=? AND actor_id=?", bid, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var ref, record, hash, clock string
	if err := db.QueryRow("SELECT v.reference_id,v.snapshot,v.record_hash,CAST(v.recorded_at AS CHAR) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='ip_revisions' AND r.field_key='payload' AND v.revision=1", project).Scan(&ref, &record, &hash, &clock); err != nil {
		t.Fatal(err)
	}
	var original retention.ReferenceVersion
	if err := json.Unmarshal([]byte(record), &original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM retention_reference_versions WHERE reference_id=? AND revision=1", ref); err != nil {
		t.Fatal(err)
	}
	restoreIndex()
	restoreChunks()
	partial, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || partial.UnboundSources != 1 {
		t.Fatal("missing pre-adoption state was replaced", partial, err)
	}
	if again, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || again.UpdatedReferences != 0 || again.UnboundSources != 1 {
		t.Fatal("missing projection retry changed bindings", again, err)
	}
	if _, err := db.Exec("INSERT INTO retention_reference_versions(reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,recorded_at) VALUES(?,1,?,?,?,?,?,?)", ref, project, original.SourceID, original.SourcePayloadHash, hash, record, clock); err != nil {
		t.Fatal(err)
	}
	if complete, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || complete.UnboundSources != 0 {
		t.Fatal("historical proof did not recover", complete, err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var copy retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &copy); err != nil || len(copy.Reference.Sources) != 5 {
		t.Fatal("projection dependency closure missing", copy, err)
	}
	for _, pin := range copy.Reference.Sources {
		if pin.Revision != 1 {
			t.Fatal("old copy gained a post-adoption source", pin)
		}
	}
	if repeated, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || repeated.UpdatedReferences != 0 || repeated.NewReferences != 0 || repeated.UnboundSources != 0 {
		t.Fatal("historical projection replay unstable", repeated, err)
	}
}

func TestRetentionWorkingReleaseDeduplicatesSameBatchIP(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "同批设定来源", "working-same-batch-ip")
	bid := branch(b, project, "正史")
	candidate := propose(b, project, bid, "", "same-batch-candidate", originalSnapshot())
	b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	rid := resource(b, project, bid, "同批资料")
	prepareRelease(b, project, bid, "", candidate, "same-batch-release", []map[string]any{{"resource_id": rid, "text": "药师在村外见到了旧友。"}})
	job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "same-batch-index", "kind": "working"}, 202)
	jobs := privateJobs(t, db)
	if state, err := jobs.Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	if report, err := w.RegisterRetentionSources(context.Background(), actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("same candidate aliases failed", report, err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &v); err != nil || len(v.Reference.Sources) != 4 {
		t.Fatal("same candidate duplicated or missing auxiliary proof", v, err)
	}
	var root bool
	if err := db.QueryRow("SELECT content_root FROM retention_references WHERE project_id=? AND owner_table='ip_branch_deltas' AND field_key='payload'", project).Scan(&root); err != nil || !root {
		t.Fatal("adopted delta left on candidate TTL", root, err)
	}
}
