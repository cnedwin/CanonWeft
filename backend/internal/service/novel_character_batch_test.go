package service

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"testing"
)

func TestCharacterBatchRejectsAlteredCoverageAndRoutes(t *testing.T) {
	root := characterBatchRoot{IDs: []string{"a", "b"}, BookID: "book", ResourceID: "design", Genre: "悬疑", ContextID: "context", Request: CharacterBatchInput{Size: 1, Notes: "原始要求"}}
	makeRun := func() RecipeExecution {
		r := RecipeExecution{}
		for i, id := range root.IDs {
			in := characterBatchStep{BookID: "book", ResourceID: "design", IDs: []string{id}, Genre: root.Genre, Notes: root.Request.Notes, Previous: map[string]json.RawMessage{}}
			after := []string{}
			bindings := []domain.RecipeValueBinding{}
			if i > 0 {
				after = append(after, characterBatchKey(0))
				in.Previous[after[0]] = json.RawMessage(`{}`)
				bindings = append(bindings, domain.RecipeValueBinding{TargetPointer: "/previous/" + after[0], SourceStep: after[0]})
			}
			raw, _ := json.Marshal(in)
			r.Steps = append(r.Steps, RecipeStepRun{Active: true, Ordinal: i, Key: characterBatchKey(i), Frozen: RecipeStepFrozen{Step: domain.RecipeStep{After: after}, Input: RecipeStepInput{Input: raw, ContextID: "context", Bindings: bindings}, Agent: domain.CapabilityVersion{Definition: domain.CapabilityDefinition{Role: "character_designer"}}}})
		}
		return r
	}
	if err := validateCharacterBatchSteps(makeRun(), root); err != nil {
		t.Fatal(err)
	}
	r := makeRun()
	r.Steps = r.Steps[:1]
	if validateCharacterBatchSteps(r, root) == nil {
		t.Fatal("missing pass accepted")
	}
	r = makeRun()
	r.Steps[1].Frozen.Input.Bindings[0].TargetPointer = "/ids"
	if validateCharacterBatchSteps(r, root) == nil {
		t.Fatal("binding can rewrite target identity")
	}
	r = makeRun()
	var in characterBatchStep
	json.Unmarshal(r.Steps[1].Frozen.Input.Input, &in)
	in.IDs = []string{"a"}
	r.Steps[1].Frozen.Input.Input, _ = json.Marshal(in)
	if validateCharacterBatchSteps(r, root) == nil {
		t.Fatal("repeated instead of missing character accepted")
	}
}
