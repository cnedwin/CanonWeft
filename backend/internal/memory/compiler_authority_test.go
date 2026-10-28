package memory

import "testing"

func TestPrivateControlProposalsRemainUnconfirmed(t *testing.T) {
	for _, purpose := range []string{"author", "critic", "research"} {
		for _, state := range []string{"candidate", "working"} {
			t.Run(purpose+"/"+state, func(t *testing.T) {
				proposal := testItem("theme-proposal")
				proposal.Kind, proposal.State = "theme", state
				proposal.Control, proposal.Visibility = true, "author"
				proposal.Actor, proposal.Session = "owner", "s"
				request := Request{Scope: testScope(), Budget: 8192, SourceEpoch: "e1", CompilerVersion: Version}
				request.Scope.Purpose = purpose
				pack, err := Compile(request, []Item{proposal})
				if err != nil || len(pack.Items) != 1 {
					t.Fatalf("own proposal unavailable: %v", err)
				}
				if pack.Items[0].Channel != "unconfirmed" {
					t.Fatalf("unadopted %s proposal entered %q channel", state, pack.Items[0].Channel)
				}
			})
		}
	}
}

func TestConfirmedControlAndWriterPrivateBoundaryRemainSeparate(t *testing.T) {
	confirmed := testItem("confirmed-theme")
	confirmed.Kind, confirmed.State, confirmed.Visibility = "theme", "adopted_plan", "author"
	confirmed.Control = true
	proposal := confirmed
	proposal.ID, proposal.State = "new-theme-proposal", "candidate"
	proposal.Actor, proposal.Session = "owner", "s"
	request := Request{Scope: testScope(), Budget: 8192, SourceEpoch: "e1", CompilerVersion: Version}
	pack, err := Compile(request, []Item{confirmed, proposal})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.ID != confirmed.ID || pack.Items[0].Channel != "control" {
		t.Fatalf("writer lost confirmed constraints or received a proposal: %+v, %v", pack.Items, err)
	}
}
