package memory

import (
	"reflect"
	"strings"
	"testing"
)

func TestRecoveryCarriesErasedMemoryNodesWithoutInventingItems(t *testing.T) {
	for _, partial := range []bool{false, true} {
		graph, ids := memoryRecoveryFixture()
		// Retire a complete independent item, keeping its private session.
		item := graph.Heads[0]
		graph.Heads = graph.Heads[1:]
		graph.ExpiredHeads = []RecoveryHeadIdentity{recoveryHeadIdentity(item)}
		for i := range graph.Revisions {
			if graph.Revisions[i].Item.ID == item.ID && !partial {
				v := graph.Revisions[i]
				graph.ExpiredRevisions = append(graph.ExpiredRevisions, recoveryRevisionIdentity(v))
			}
		}
		if !partial {
			retained := []RecoveryRevision{}
			for _, v := range graph.Revisions {
				if v.Item.ID != item.ID {
					retained = append(retained, v)
				}
			}
			graph.Revisions = retained
		}
		// The fixture's other item summarizes this one. An erased summary input
		// must not be silently reconstructed to make the graph pass.
		if !partial {
			if _, err := RemapRecovery(graph, ids); err == nil {
				t.Fatal("erased summary input accepted")
			}
		}
		graph.Heads = nil
		kept := []RecoveryRevision{}
		for _, v := range graph.Revisions {
			if v.Item.ID == item.ID {
				kept = append(kept, v)
			}
		}
		graph.Revisions = kept
		for i := range graph.Sessions {
			graph.Sessions[i].Branch = item.Branch
			graph.Sessions[i].FocusIDs = []string{item.ID}
		}
		before := digest(graph)
		copy, err := RemapRecovery(graph, ids)
		if err != nil {
			t.Fatal(partial, err)
		}
		if len(copy.Heads) != 0 || len(copy.ExpiredHeads) != 1 || copy.ExpiredHeads[0].ID != ids.Items[item.ID] {
			t.Fatal("erased head became an Item")
		}
		if !reflect.DeepEqual(copy.Sessions[0].FocusIDs, []string{ids.Items[item.ID]}) || before != digest(graph) {
			t.Fatal("focus lost or source mutated")
		}
		bad := graph
		bad.ExpiredHeads = append([]RecoveryHeadIdentity(nil), graph.ExpiredHeads...)
		bad.ExpiredHeads[0].ContentHash = strings.Repeat("f", 64)
		if _, err := RemapRecovery(bad, ids); err == nil {
			t.Fatal("unbound erased head accepted")
		}
	}
}
