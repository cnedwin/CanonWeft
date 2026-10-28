package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestSemanticReviewBackupPreservesObjectsButNeverAuthority(t *testing.T) {
	a := emptyArchive(t)
	if _, ok := a.Data["semantic_review_runs"]; !ok {
		t.Fatal("semantic review archive contract missing")
	}
	p, run, target := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	input, report := []byte("fixed semantic review input"), []byte("historical report")
	frozen, _ := json.Marshal(domain.SemanticReviewFrozen{Schema: 1, InputHash: digest(input), Scope: "current target"})
	testRow(a, "semantic_review_runs", map[string]string{"id": run, "project_id": p, "target_kind": "ip_candidate", "target_id": target, "target_hash": strings.Repeat("d", 64), "frozen": string(frozen), "state": "COMPLETED", "verdict": "PASS", "paused": "0", "cancel_requested": "0", "restored_disabled": "0", "report_hash": digest(report)})
	testRow(a, "semantic_review_acceptances", map[string]string{"id": strings.Repeat("e", 32), "project_id": p, "target_kind": "ip_candidate", "target_id": target, "target_hash": strings.Repeat("d", 64), "revision": "1", "review_id": run, "report_hash": digest(report)})
	a.Blobs["blobs/"+p+"/"+digest(input)] = input
	a.Blobs["blobs/"+p+"/"+digest(report)] = report
	if err := validateData(a.Data); err == nil {
		t.Fatal("enabled review authority accepted")
	}
	sanitize(a.Data)
	raw := archiveBytes(t, a)
	read, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Blobs) != 2 || len(read.Data["semantic_review_acceptances"]) != 1 {
		t.Fatal("historical report or approval lost")
	}
	for _, s := range tableCatalog {
		if s.name == "semantic_review_acceptances" {
			s.set(read.Data[s.name][0], "report_hash", strings.Repeat("f", 64))
			if err := validateData(read.Data); err == nil {
				t.Fatal("mismatched historical receipt accepted")
			}
		}
	}
	for _, s := range tableCatalog {
		if s.name == "semantic_review_runs" {
			row := read.Data[s.name][0]
			if s.value(row, "restored_disabled") != "1" || s.value(row, "state") != "COMPLETED" || s.value(row, "verdict") != "PASS" {
				t.Fatal("history changed or review reenabled")
			}
		}
	}
	delete(a.Blobs, "blobs/"+p+"/"+digest(input))
	raw = archiveBytes(t, a)
	if _, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("missing frozen input accepted")
	}
}

func TestSemanticReviewBackupCancelsPendingAndRejectsMismatchedReceipt(t *testing.T) {
	a := emptyArchive(t)
	if _, ok := a.Data["semantic_review_runs"]; !ok {
		t.Fatal("semantic review archive contract missing")
	}
	p, run := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for i, state := range []string{"PREPARED", "QUEUED", "RUNNING", "UNKNOWN", "BLOCKED"} {
		id := strings.Repeat(string(rune('b'+i)), 32)
		testRow(a, "semantic_review_runs", map[string]string{"id": id, "project_id": p, "state": state, "frozen": `{"schema":1,"input_hash":"` + strings.Repeat("c", 64) + `"}`})
	}
	sanitize(a.Data)
	for _, s := range tableCatalog {
		if s.name == "semantic_review_runs" {
			for _, row := range a.Data[s.name] {
				if s.value(row, "state") != "CANCELLED" || s.value(row, "cancel_requested") != "1" || s.value(row, "paused") != "0" {
					t.Fatal("pending review survived restore")
				}
			}
		}
	}
	testRow(a, "semantic_review_acceptances", map[string]string{"project_id": p, "review_id": run, "report_hash": strings.Repeat("d", 64)})
	if err := validateData(a.Data); err == nil {
		t.Fatal("unmatched acceptance accepted")
	}
}
