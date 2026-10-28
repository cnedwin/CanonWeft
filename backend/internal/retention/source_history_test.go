package retention

import (
	"strings"
	"testing"
)

func TestSourceHistoryBranchPointersRespectForkLineage(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	for _, mode := range []string{"valid-own", "valid-inherited", "foreign-project", "sibling", "missing", "cycle", "unpublished"} {
		t.Run(mode, func(t *testing.T) {
			rows := sourceOwnershipRows()
			rows = append(rows,
				inventoryRow("branches", map[string]string{"id": id("7"), "project_id": id("2"), "parent_id": id("5"), "base_revision": id("8"), "head_revision": id("8")}),
				inventoryRow("ip_revisions", map[string]string{"id": id("8"), "branch_id": id("5"), "status": "PUBLISHED"}),
				inventoryRow("ip_revisions", map[string]string{"id": id("9"), "branch_id": id("7"), "base_head": id("8"), "status": "PUBLISHED"}),
			)
			rows[4].Values["head_revision"] = id("8")
			child := &rows[8]
			switch mode {
			case "valid-own":
				child.Values["head_revision"] = id("9")
			case "foreign-project":
				rows[9].Values["branch_id"] = id("6")
			case "sibling":
				rows = append(rows, inventoryRow("branches", map[string]string{"id": id("a"), "project_id": id("2")}))
				rows[9].Values["branch_id"] = id("a")
			case "missing":
				child.Values["head_revision"] = id("f")
			case "cycle":
				rows[4].Values["parent_id"] = id("7")
			case "unpublished":
				rows[9].Values["status"] = "PREPARED"
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			_, err = index.Owner("branches", map[string]string{"id": id("7")})
			valid := strings.HasPrefix(mode, "valid-")
			if valid != (err == nil) {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestSourceHistoryRejectsForeignSemanticTarget(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	rows := sourceOwnershipRows()
	rows = append(rows,
		inventoryRow("ip_revisions", map[string]string{"id": id("a"), "branch_id": id("6"), "status": "PREPARED"}),
		inventoryRow("semantic_review_runs", map[string]string{"id": id("b"), "project_id": id("2"), "branch_id": id("5"), "target_kind": "ip_candidate", "target_id": id("a")}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = index.Owner("semantic_review_runs", map[string]string{"id": id("b")}); err == nil {
		t.Fatal("foreign semantic target accepted")
	}
	rows[8].Values["branch_id"] = id("5")
	index, _ = NewSourceIndex(rows)
	if _, err = index.Owner("semantic_review_runs", map[string]string{"id": id("b")}); err != nil {
		t.Fatal(err)
	}
}
