package qualification_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/qualification"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

// The oracle is exclusively a local protocol fixture. It probes the trusted
// scorer to exercise execution/storage; it is not a tested semantic model.
func novelSemanticOracle(t *testing.T, suite qualification.Suite) map[string]domain.SemanticReviewReport {
	t.Helper()
	result := map[string]domain.SemanticReviewReport{}
	for _, c := range suite.Public().Cases {
		var input domain.SemanticReviewDocument
		if err := json.Unmarshal(c.Input, &input); err != nil {
			t.Fatal(err)
		}
		report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "Local transport fixture only; no model quality claim"}
		raw, _ := json.Marshal(report)
		if !suite.Score(c.ID, raw).Passed {
			found := false
			for _, category := range domain.SemanticReviewCategories() {
				report.Verdict = "FAIL"
				report.Issues = []domain.SemanticReviewIssue{{Category: category, Severity: "blocker", TargetKey: "resource:scene", AnchorKeys: []string{"canon", "source"}, Explanation: "Local fixture checks both fixed anchors"}}
				raw, _ = json.Marshal(report)
				if suite.Score(c.ID, raw).Passed {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("fixture cannot find a contract-valid answer", c.ID)
			}
		}
		result[input.Target.Hash] = report
	}
	return result
}

func TestNovelSemanticAcceptanceRunsAllCasesWithPersistentMetrics(t *testing.T) {
	f, _, input := modelFixture(t)
	ctx := context.Background()
	suite, err := qualification.Lookup(qualification.NovelSemanticSuiteID)
	if err != nil {
		t.Fatal(err)
	}
	protocol := &semanticProtocolFixture{byTarget: novelSemanticOracle(t, suite)}
	f.svc.Generations.Adapter = models.New(protocol)
	if _, err = (&service.Models{Workspace: f.svc.IP.Workspace}).Bind(ctx, f.actor.ID, domain.ModelBinding{Role: "critic", ConnectionID: inputConnection(t, f, input.PriceID), ConnectionRevision: 1}); err != nil {
		t.Fatal(err)
	}
	installed, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: qualification.NovelSemanticSuiteID, Kind: "agent", Role: "critic"})
	if err != nil {
		t.Fatal(err)
	}
	input.SuiteID, input.Role, input.VersionID, input.BaselineID, input.MaxOutputTokens = qualification.NovelSemanticSuiteID, "critic", installed.BaselineID, installed.BaselineID, 1024
	budgets := &service.Budgets{Workspace: f.svc.IP.Workspace}
	currentBudget, err := budgets.Read(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	lowBudget, err := budgets.Configure(ctx, f.actor.ID, service.Budget{Revision: currentBudget.Revision, Currency: "USD", Limit: 1, PerCall: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || quote.Ready || quote.Calls != 600 || quote.Frozen.Estimate <= 1 || protocol.calls.Load() != 0 {
		t.Fatal("insufficient budget must block all 600 calls without dispatch", quote.Ready, quote.Calls, quote.Reasons, err)
	}
	if _, err = budgets.Configure(ctx, f.actor.ID, service.Budget{Revision: lowBudget.Revision, Currency: "USD", Limit: quote.Frozen.Estimate + 1000000, PerCall: 1000000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	quote, err = f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || !quote.Ready || protocol.calls.Load() != 0 {
		t.Fatal("local fixture estimate must be free", quote.Ready, quote.Reasons, err)
	}
	run, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	for iteration := 0; iteration < quote.Calls*3+10; iteration++ {
		state, advanceErr := f.svc.Advance(ctx, run.ID)
		if advanceErr != nil {
			t.Fatal("advance", advanceErr)
		}
		if state != "QUEUED" && state != "RUNNING" {
			break
		}
		var child string
		readErr := f.db.QueryRow("SELECT child_id FROM qualification_steps WHERE run_id=? AND state='RUNNING' AND child_id<>'' LIMIT 1", run.ID).Scan(&child)
		if readErr == nil {
			if _, err = f.svc.Generations.Execute(ctx, child); err != nil {
				t.Fatal("execute", err)
			}
		} else if readErr != sql.ErrNoRows {
			t.Fatal(readErr)
		}
		if !paused && protocol.calls.Load() >= 17 {
			if _, err = f.svc.Control(ctx, f.actor, f.project, run.ID, "pause"); err != nil {
				t.Fatal(err)
			}
			before := protocol.calls.Load()
			state, err = f.svc.Advance(ctx, run.ID)
			if err != nil || state != "PAUSED" || protocol.calls.Load() != before {
				t.Fatal("paused run dispatched", state, err)
			}
			f.svc = &service.Qualifications{IP: f.svc.IP, Generations: &service.Generations{IP: f.svc.IP, Adapter: models.New(protocol)}}
			if _, err = f.svc.Control(ctx, f.actor, f.project, run.ID, "resume"); err != nil {
				t.Fatal(err)
			}
			paused = true
		}
	}
	run, err = f.svc.Read(ctx, f.actor, f.project, run.ID)
	if err != nil || run.State != "PASSED" || run.NovelSemantics == nil || len(run.Steps) != 600 || protocol.calls.Load() != 600 || !paused {
		t.Fatal("full run missing", run.State, len(run.Steps), protocol.calls.Load(), err)
	}
	if run.NovelSemantics.Arms[0].Recall != 0 || run.NovelSemantics.Arms[0].Passed || !run.NovelSemantics.Arms[2].Passed || run.NovelSemantics.Arms[2].TruePositive != 100 || run.NovelSemantics.Arms[2].TrueNegative != 100 {
		t.Fatal("wrong three-group metrics", run.NovelSemantics)
	}
	raw, err := f.svc.Report(ctx, f.actor, f.project, run.ID)
	var report qualification.QualificationReport
	if err != nil || json.Unmarshal(raw, &report) != nil || report.ProfessionalQualified || report.NovelSemantics == nil || !report.NovelSemantics.Arms[2].Passed {
		t.Fatal("immutable report lost metrics or claimed literary quality", err)
	}
	if _, err = f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: run.QualificationID, TaskKind: "shared.semantic-review", ExpectedBinding: ""}); err != nil {
		t.Fatal("exact bounded qualification activation", err)
	}
	if _, err = f.svc.Create(ctx, f.actor, f.project, input); err != nil || protocol.calls.Load() != 600 {
		t.Fatal("run replay changed calls", err)
	}
	if path := os.Getenv("STUDIO_NOVEL_SEMANTIC_REPORT"); path != "" {
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if node := os.Getenv("STUDIO_NOVEL_SEMANTIC_BROWSER_NODE"); node != "" {
		web, pathErr := filepath.Abs("../../../web")
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		server := httptest.NewUnstartedServer(nil)
		origin := "http://" + server.Listener.Addr().String()
		app := httpapi.New(f.db, origin)
		app.Static("/", filepath.Join(web, "dist"))
		server.Config.Handler = app
		server.Start()
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		out, browserErr := exec.CommandContext(browserCtx, node, filepath.Join(web, "tests", "novel-semantic-browser.cjs"), origin, run.ID).CombinedOutput()
		if browserErr != nil {
			t.Fatalf("novel semantic browser: %v\n%s", browserErr, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	verifyNovelSemanticRestore(t, f, run)
	t.Log("600 real local protocol requests, 200 distinct cases per group, pause/service recreation/resume without resend; exact activation and immutable statistical report. No paid model or human quality validation.")
}

func verifyNovelSemanticRestore(t *testing.T, f *fixture, run service.QualificationRun) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	archive := filepath.Join(root, "novel-semantic.zip")
	if _, err := backup.Create(ctx, f.db, f.svc.IP.Blobs, archive); err != nil {
		t.Fatal("novel semantic snapshot", err)
	}
	cfg := f.config.Clone()
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := f.admin.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "restored-blobs")}
	if _, err := backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobs.Root, Authority: backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger}}); err != nil {
		t.Fatal("novel semantic restore", err)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var enabled int
	if err = db.QueryRow("SELECT COUNT(*) FROM capability_qualifications WHERE enabled<>0").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("restored qualification regained authority", enabled, err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM qualification_steps WHERE run_id=?", run.ID).Scan(&count); err != nil || count != 600 {
		t.Fatal("restored steps missing", count, err)
	}
	raw, err := blobs.ReadObject(ctx, f.project, run.ReportHash)
	var report qualification.QualificationReport
	if err != nil || json.Unmarshal(raw, &report) != nil || report.NovelSemantics == nil || report.NovelSemantics.Arms[2].TruePositive != 100 || report.ProfessionalQualified {
		t.Fatal("restored corpus report lost statistics or boundary", err)
	}
	t.Log("Independent snapshot restore retained all 600 steps and exact corpus report; restored qualification authority remained disabled.")
}

func inputConnection(t *testing.T, f *fixture, priceID string) string {
	t.Helper()
	prices, err := f.svc.Generations.Prices(context.Background(), f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, price := range prices {
		if price.ID == priceID {
			return price.ConnectionID
		}
	}
	t.Fatal("fixture price connection missing")
	return ""
}
