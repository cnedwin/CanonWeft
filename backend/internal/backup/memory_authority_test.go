package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/memory"
)

func TestMemoryArchivePrivateControlChannelCompatibility(t *testing.T) {
	for _, mode := range []string{"current", "legacy", "legacy-working", "legacy-tight-budget", "evidence", "plan", "ordinary-control", "bad-hash"} {
		t.Run(mode, func(t *testing.T) {
			a := memoryArchiveFixture(t)
			itemSpec, revisionSpec := memorySpec("memory_items"), memorySpec("memory_revisions")
			var item memory.Item
			if err := json.Unmarshal([]byte(itemSpec.value(a.Data[itemSpec.name][0], "document")), &item); err != nil {
				t.Fatal(err)
			}
			item.Control = mode != "ordinary-control"
			if mode == "legacy-working" {
				item.State = "working"
				itemSpec.set(a.Data[itemSpec.name][0], "state", item.State)
			}
			itemRaw, _ := json.Marshal(item)
			itemSpec.set(a.Data[itemSpec.name][0], "document", string(itemRaw))
			revisionSpec.set(a.Data[revisionSpec.name][0], "document", string(itemRaw))
			request := memory.Request{Scope: memory.Scope{Project: item.Project, Branch: item.Branch, Canon: "empty", Actor: item.Actor, Session: item.Session, Timeline: "main", Purpose: "critic", PermissionEpoch: "epoch"}, Budget: 8192, SourceEpoch: "epoch", CompilerVersion: memory.Version}
			if mode == "legacy-tight-budget" {
				request.RequiredIDs = []string{item.ID}
			}
			pack, err := memory.Compile(request, []memory.Item{item})
			if err != nil || len(pack.Items) != 1 || pack.Items[0].Channel != "unconfirmed" {
				t.Fatalf("invalid current proposal fixture: %+v, %v", pack.Items, err)
			}
			if mode == "legacy" || mode == "legacy-working" || mode == "legacy-tight-budget" || mode == "bad-hash" {
				pack, err = memory.CompileLegacyPrivateControl(request, []memory.Item{item})
				if err != nil || pack.Items[0].Channel != "control" {
					t.Fatalf("invalid historical proposal fixture: %v", err)
				}
				if mode == "legacy-tight-budget" {
					// The old classification used fewer serialized bytes. Preserve
					// that valid historical allocation without raising its budget.
					request.Budget = pack.EstimatedTokens + 8
					pack, err = memory.CompileLegacyPrivateControl(request, []memory.Item{item})
					if err != nil {
						t.Fatalf("old tight budget fixture is invalid: %v", err)
					}
					if _, err := memory.Compile(request, []memory.Item{item}); !errors.Is(err, memory.ErrBudget) {
						t.Fatalf("fixture did not exercise the classification budget boundary: %v", err)
					}
				}
			} else if mode == "evidence" || mode == "plan" {
				pack.Items[0].Channel = mode
			} else if mode == "ordinary-control" {
				pack.Items[0].Channel = "control"
			}
			pack.Hash = ""
			raw, _ := json.Marshal(pack)
			pack.Hash = digest(raw)
			if mode == "bad-hash" {
				pack.Hash = strings.Repeat("f", 64)
			}
			payload, _ := json.Marshal(map[string]any{"memory": pack})
			testRow(a, "context_packages", map[string]string{"id": strings.Repeat("d", 64), "project_id": item.Project, "branch_id": item.Branch, "actor_id": item.Actor, "payload": string(payload)})
			before, _ := json.Marshal(a.Data)
			err = validateMemoryContexts(a.Data)
			valid := mode == "current" || mode == "legacy" || mode == "legacy-working" || mode == "legacy-tight-budget"
			if valid != (err == nil) {
				t.Fatalf("unexpected archive acceptance for %s: %v", mode, err)
			}
			after, _ := json.Marshal(a.Data)
			if !bytes.Equal(before, after) {
				t.Fatal("archive validation rewrote immutable historical context")
			}
		})
	}
}
