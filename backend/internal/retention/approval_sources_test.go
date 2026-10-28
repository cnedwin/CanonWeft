package retention

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/qualification"
)

func TestApprovalSourcePinsExactReportAndRejectsSubstitution(t *testing.T) {
	for _, mode := range []string{"valid", "report", "runtime", "manifest", "suite", "policy", "wrong-bytes", "wrong-frozen", "failed-report", "professional", "not-tool"} {
		t.Run(mode, func(t *testing.T) {
			project, branch, runID := strings.Repeat("2", 32), strings.Repeat("5", 32), strings.Repeat("a", 32)
			frozen := qualification.QualificationFrozen{
				Input:             qualification.QualificationInput{BranchID: branch, VersionID: strings.Repeat("b", 32), SuiteID: "shared-contract-v1"},
				Candidate:         domain.CapabilityRef{ID: strings.Repeat("b", 32), Kind: "tool"},
				CandidateManifest: strings.Repeat("c", 64), RuntimeHash: strings.Repeat("d", 64),
				Suite: qualification.CatalogEntry{ID: "shared-contract-v1", Hash: strings.Repeat("e", 64)},
			}
			if mode == "not-tool" {
				frozen.Candidate.Kind = "agent"
			}
			report := qualification.QualificationReport{RunID: runID, Frozen: frozen, Passed: true}
			if mode == "failed-report" {
				report.Passed = false
			}
			if mode == "professional" {
				report.ProfessionalQualified = true
			}
			if mode == "wrong-frozen" {
				report.Frozen.RuntimeHash = strings.Repeat("f", 64)
			}
			reportBytes, _ := json.Marshal(report)
			reportHash := financialDigest(reportBytes)
			payload := map[string]any{"report_hash": reportHash, "manifest_hash": frozen.CandidateManifest, "runtime_hash": frozen.RuntimeHash, "suite_hash": frozen.Suite.Hash, "note": "审阅固定执行依赖", "policy": "owner-maintenance-separate-from-authored-skill-v1"}
			if mode == "report" || mode == "runtime" || mode == "manifest" || mode == "suite" {
				payload[mode+"_hash"] = strings.Repeat("f", 64)
			}
			if mode == "policy" {
				payload["policy"] = "automatic-author-approval"
			}
			approvalBytes, _ := json.Marshal(payload)
			frozenBytes, _ := json.Marshal(frozen)
			rows := sourceOwnershipRows()
			approval := inventoryRow("qualification_approvals", map[string]string{"run_id": runID, "project_id": project, "actor_id": strings.Repeat("4", 32), "payload": string(approvalBytes)})
			rows = append(rows, inventoryRow("qualification_runs", map[string]string{"id": runID, "project_id": project, "branch_id": branch, "actor_id": strings.Repeat("4", 32), "frozen": string(frozenBytes), "report_hash": reportHash}), approval)
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			g := materialCollector{index: index, project: project, refs: map[string]MaterialReference{}}
			if err := g.add(approval, "payload", string(approvalBytes)); err != nil {
				t.Fatal(err)
			}
			materials := []ObservedMaterial{}
			for _, ref := range g.refs {
				materials = append(materials, ObservedMaterial{MaterialReference: ref})
			}
			read := func(_ context.Context, gotProject, hash string) ([]byte, error) {
				if gotProject != project || hash != reportHash {
					t.Fatal("wrong report scope")
				}
				if mode == "wrong-bytes" {
					return []byte("different report"), nil
				}
				return reportBytes, nil
			}
			err = index.attachQualificationSources(context.Background(), project, materials, read)
			if mode != "valid" {
				if err == nil {
					t.Fatal("substituted approval accepted")
				}
				return
			}
			if err != nil || len(materials[0].Sources) != 2 || materials[0].ContentRoot {
				t.Fatal("approval did not pin raw report and frozen execution", materials, err)
			}
		})
	}
}
