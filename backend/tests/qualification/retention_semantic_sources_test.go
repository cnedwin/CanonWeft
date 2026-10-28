package qualification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func semanticSources(t *testing.T, f *fixture) retention.SourceRegistrationReport {
	t.Helper()
	ctx := context.Background()
	report, err := f.svc.IP.Workspace.RegisterRetentionSources(ctx, f.actor.ID, f.project)
	if err != nil || report.UnboundSources != 0 {
		tx, beginErr := f.db.BeginTx(ctx, nil)
		if beginErr == nil {
			_, detail := retention.RegisterSourceObservations(ctx, tx, f.ledger, f.project, f.svc.IP.Blobs.ReadObject)
			tx.Rollback()
			t.Log("semantic source diagnostic", detail)
		}
		t.Fatal("semantic sources incomplete", report, err)
	}
	return report
}

func assertSemanticCaptured(t *testing.T, f *fixture, table, id, field string, minimumSources int) {
	t.Helper()
	owner, err := retention.RowOwnerKey(table, map[string]string{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	err = f.db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table=? AND r.owner_key=? AND r.field_key=?`, f.project, table, owner, field).Scan(&raw)
	var version retention.ReferenceVersion
	if err != nil || json.Unmarshal([]byte(raw), &version) != nil || len(version.Reference.Sources) < minimumSources {
		t.Fatal("live semantic writer did not pin original sources", table, field, err)
	}
	for _, source := range version.Reference.Sources {
		if source.Revision < 1 {
			t.Fatal("live semantic writer left an unresolved source", table, field)
		}
	}
}

func TestRetentionSemanticCandidatePinsOriginalCoverage(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	installQualificationSourceFixture(t, f)
	candidate, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: f.svc.IP}}
	run, err := svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: candidate.ID, TargetHash: candidate.Hash, BranchID: f.branch, MaxOutputTokens: 1024})
	if err != nil || run.State != "BLOCKED" {
		t.Fatal("unconfigured review did not preserve original input", run.State, err)
	}
	assertSemanticCaptured(t, f, "semantic_review_runs", run.ID, "frozen", 1)
	assertSemanticCaptured(t, f, "semantic_review_runs", run.ID, "frozen.input_hash", 1)
	semanticSources(t, f)
	raw, err := f.svc.IP.Blobs.ReadObject(ctx, f.project, run.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err := f.db.QueryRow("SELECT frozen FROM semantic_review_runs WHERE id=?", run.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"anchor", "coverage", "snapshot", "target"} {
		var doc domain.SemanticReviewDocument
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatal("original semantic input unreadable")
		}
		switch field {
		case "anchor":
			doc.Anchors[0].Text = "substituted anchor"
		case "coverage":
			doc.RequiredCoverage = []string{"invented"}
		case "snapshot":
			doc.Snapshot.Entities[0].Name = "另一个角色"
		case "target":
			doc.Target.ID = service.NewID()
		}
		changed, _ := json.Marshal(doc)
		object, err := f.svc.IP.Blobs.PutObject(ctx, f.project, bytes.NewReader(changed))
		if err != nil {
			t.Fatal(err)
		}
		frozen := run.Frozen
		frozen.InputHash = object.Hash
		// The copied scope is also changed; self-consistent metadata is no proof
		// that the review actually used the original candidate.
		frozen.ContentScopeHash, _ = domain.SemanticContentScopeHash(doc)
		changed, _ = json.Marshal(frozen)
		if _, err := f.db.Exec("UPDATE semantic_review_runs SET frozen=? WHERE id=?", changed, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.IP.Workspace.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("substituted review input accepted", field, err)
		}
	}
	if _, err := f.db.Exec("UPDATE semantic_review_runs SET frozen=? WHERE id=?", original, run.ID); err != nil {
		t.Fatal(err)
	}
	if report := semanticSources(t, f); report.UpdatedReferences != 0 {
		t.Fatal("restored review changed original pins", report)
	}
	var calls int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&calls); err != nil || calls != 0 {
		t.Fatal("source registration called a model", calls, err)
	}
}

func TestRetentionSemanticReportPinsReceiptAfterPublication(t *testing.T) {
	f, source, binding, protocol, price := semanticReviewFixture(t)
	ctx := context.Background()
	installQualificationSourceFixture(t, f)
	// Historical qualification records are backfilled before new live writers.
	semanticSources(t, f)
	release := prepareGenerationRelease(t, f, source, "人工整理的章节，与原生成正文不同。")
	review := completeSemanticReview(t, f, release, binding, price)
	assertSemanticCaptured(t, f, "semantic_review_runs", review.ID, "report_hash", 3)
	svc := &service.SemanticReviews{Generations: f.svc.Generations}
	var versionsBefore int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", f.project).Scan(&versionsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("CREATE TRIGGER reject_semantic_selection_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected selection proof'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec("DROP TRIGGER IF EXISTS reject_semantic_selection_proof") })
	callsBefore := protocol.calls.Load()
	if _, err := svc.Select(ctx, f.actor, f.project, review.ID, 0); err == nil {
		t.Fatal("selection committed without its original-source proof")
	}
	var acceptances, versionsAfter int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM semantic_review_acceptances WHERE project_id=?", f.project).Scan(&acceptances); err != nil || acceptances != 0 {
		t.Fatal("failed proof committed author acceptance", acceptances, err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", f.project).Scan(&versionsAfter); err != nil || versionsAfter != versionsBefore || protocol.calls.Load() != callsBefore {
		t.Fatal("failed selection changed history or resent a model call", versionsBefore, versionsAfter, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER reject_semantic_selection_proof"); err != nil {
		t.Fatal(err)
	}
	selection, err := svc.Select(ctx, f.actor, f.project, review.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "semantic_review_acceptances", selection.ID, "report_hash", 1)
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.PublishRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal(err)
	}
	beforeCalls := protocol.calls.Load()
	semanticSources(t, f)
	var proofText string
	if err := f.db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='semantic_review_runs' AND r.field_key='report_hash'`, f.project).Scan(&proofText); err != nil {
		t.Fatal(err)
	}
	var proof retention.ReferenceVersion
	if json.Unmarshal([]byte(proofText), &proof) != nil || len(proof.Reference.Sources) < 3 {
		t.Fatal("report lacks input and actual result provenance")
	}
	for _, tc := range []struct{ table, id, field, value string }{
		{"semantic_review_runs", review.ID, "generation_id", source.ID},
		{"semantic_review_runs", review.ID, "verdict", "FAIL"},
		{"semantic_review_acceptances", selection.ID, "report_hash", source.Frozen.InputHash},
	} {
		var original string
		if err := f.db.QueryRow("SELECT "+tc.field+" FROM "+tc.table+" WHERE id=?", tc.id).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("UPDATE "+tc.table+" SET "+tc.field+"=? WHERE id=?", tc.value, tc.id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.IP.Workspace.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("substituted semantic receipt accepted", tc.field, err)
		}
		if _, err := f.db.Exec("UPDATE "+tc.table+" SET "+tc.field+"=? WHERE id=?", original, tc.id); err != nil {
			t.Fatal(err)
		}
	}
	if report := semanticSources(t, f); report.UpdatedReferences != 0 || protocol.calls.Load() != beforeCalls {
		t.Fatal("report replay changed source pins or invoked a model", report)
	}
}
