package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRecipeInputRoutesOnlyExplicitValuesAndPreservesNumbers(t *testing.T) {
	base := json.RawMessage(`{"text":"","count":0,"slot":[null],"a/b":{"~name":null}}`)
	outputs := map[string]json.RawMessage{
		"writer": json.RawMessage(`{"text":"星河🌙","count":9007199254740993,"items":[{"value":"独立资料"}],"~name":null,"private_reasoning":"不能隐式交接"}`),
		"critic": json.RawMessage(`{"private_reasoning":"未声明为前驱"}`),
	}
	bindings := []RecipeValueBinding{
		{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "/text"},
		{TargetPointer: "/count", SourceStep: "writer", SourcePointer: "/count"},
		{TargetPointer: "/slot/0", SourceStep: "writer", SourcePointer: "/items/0/value"},
		{TargetPointer: "/a~1b/~0name", SourceStep: "writer", SourcePointer: "/~0name"},
	}
	got, err := ResolveRecipeInput(base, bindings, []string{"writer"}, outputs)
	if err != nil || string(got) != `{"a/b":{"~name":null},"count":9007199254740993,"slot":["独立资料"],"text":"星河🌙"}` {
		t.Fatal(string(got), err)
	}
	if string(base) != `{"text":"","count":0,"slot":[null],"a/b":{"~name":null}}` || !strings.Contains(string(outputs["writer"]), "private_reasoning") {
		t.Fatal("routing mutated source bytes")
	}
}

func TestRecipeInputPointerEscapesAndRootAreExplicit(t *testing.T) {
	out := map[string]json.RawMessage{"writer": json.RawMessage(`{"~1":"tilde-one","":"empty-key","é":"composed","é":"decomposed"}`)}
	got, err := ResolveRecipeInput(json.RawMessage(`{"":null,"a":null,"b":null}`), []RecipeValueBinding{
		{TargetPointer: "/", SourceStep: "writer", SourcePointer: "/~01"},
		{TargetPointer: "/a", SourceStep: "writer", SourcePointer: "/é"},
		{TargetPointer: "/b", SourceStep: "writer", SourcePointer: "/é"},
	}, []string{"writer"}, out)
	if err != nil || string(got) != `{"":"tilde-one","a":"composed","b":"decomposed"}` {
		t.Fatal(string(got), err)
	}
	got, err = ResolveRecipeInput(json.RawMessage(`{}`), []RecipeValueBinding{{SourceStep: "writer"}}, []string{"writer"}, out)
	if err != nil || !strings.Contains(string(got), `"~1":"tilde-one"`) {
		t.Fatal(string(got), err)
	}
}

func TestRecipeInputRejectsAmbiguousOrUnauthorizedRouting(t *testing.T) {
	base := json.RawMessage(`{"text":"","items":[{"value":""}]}`)
	outputs := map[string]json.RawMessage{"writer": json.RawMessage(`{"text":"正文","items":["value"]}`), "critic": json.RawMessage(`{"text":"秘密"}`)}
	bind := RecipeValueBinding{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "/text"}
	tests := map[string][]RecipeValueBinding{
		"not-predecessor":    {{TargetPointer: "/text", SourceStep: "critic", SourcePointer: "/text"}},
		"missing-output":     {{TargetPointer: "/text", SourceStep: "missing", SourcePointer: "/text"}},
		"missing-source":     {{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "/absent"}},
		"missing-target":     {{TargetPointer: "/new-field", SourceStep: "writer", SourcePointer: "/text"}},
		"invalid-escape":     {{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "/~2"}},
		"uri-fragment":       {{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "#/text"}},
		"leading-zero":       {{TargetPointer: "/text", SourceStep: "writer", SourcePointer: "/items/00"}},
		"append-index":       {{TargetPointer: "/items/-", SourceStep: "writer", SourcePointer: "/text"}},
		"negative-index":     {{TargetPointer: "/items/-1", SourceStep: "writer", SourcePointer: "/text"}},
		"oversized-index":    {{TargetPointer: "/items/9999999999999999999999999", SourceStep: "writer", SourcePointer: "/text"}},
		"duplicate-target":   {bind, bind},
		"overlapping-target": {{TargetPointer: "/items", SourceStep: "writer", SourcePointer: "/items"}, {TargetPointer: "/items/0/value", SourceStep: "writer", SourcePointer: "/text"}},
		"root-overlap":       {{SourceStep: "writer"}, bind},
		"scalar-root":        {{SourceStep: "writer", SourcePointer: "/text"}},
		"too-many-bindings":  make([]RecipeValueBinding, 65),
	}
	for name, bindings := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := ResolveRecipeInput(base, bindings, []string{"writer", "missing"}, outputs); !errors.Is(err, ErrValidation) || got != nil {
				t.Fatal(string(got), err)
			}
		})
	}
}

func TestRecipeInputRejectsAmbiguousJSONAndBounds(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":{"v":1,"v":2}}`, `{} {}`, `[]`, `null`, `{"x":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + `}`, `{"x":"` + strings.Repeat("a", 256*1024) + `"}`} {
		if _, err := ResolveRecipeInput(json.RawMessage(raw), nil, nil, nil); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid base", err)
		}
	}
	for _, output := range []json.RawMessage{json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(`{"x":"` + strings.Repeat("a", 256*1024) + `"}`), {0xff}} {
		if _, err := ResolveRecipeInput(json.RawMessage(`{"x":null}`), []RecipeValueBinding{{TargetPointer: "/x", SourceStep: "writer", SourcePointer: "/x"}}, []string{"writer"}, map[string]json.RawMessage{"writer": output}); !errors.Is(err, ErrValidation) {
			t.Fatal("ambiguous or oversized output", err)
		}
	}
	if _, err := ResolveRecipeInput(json.RawMessage(`{}`), nil, []string{"writer", "writer"}, nil); !errors.Is(err, ErrValidation) {
		t.Fatal("duplicate predecessor", err)
	}
}

func TestRecipeInputRejectsCombinedSizeDepthAndUnpairedSurrogates(t *testing.T) {
	for _, raw := range []string{`{"x":"\ud800"}`, `{"\udfff":0}`, `{"x":"\ud800\u0041"}`} {
		if _, err := ResolveRecipeInput(json.RawMessage(raw), nil, nil, nil); !errors.Is(err, ErrValidation) {
			t.Fatal("lossy Unicode accepted", raw, err)
		}
	}
	valid := json.RawMessage(`{"x":"\ud83c\udf19","literal":"\\ud800"}`)
	if got, err := ResolveRecipeInput(valid, nil, nil, nil); err != nil || !strings.Contains(string(got), "🌙") {
		t.Fatal(string(got), err)
	}
	large := `"` + strings.Repeat("a", 140*1024) + `"`
	if _, err := ResolveRecipeInput(json.RawMessage(`{"a":`+large+`,"b":null}`), []RecipeValueBinding{{TargetPointer: "/b", SourceStep: "writer", SourcePointer: "/text"}}, []string{"writer"}, map[string]json.RawMessage{"writer": json.RawMessage(`{"text":` + large + `}`)}); !errors.Is(err, ErrValidation) {
		t.Fatal("combined output exceeded size limit", err)
	}
	deep := strings.Repeat(`{"x":`, 40) + `null` + strings.Repeat(`}`, 40)
	if _, err := ResolveRecipeInput(json.RawMessage(deep), []RecipeValueBinding{{TargetPointer: strings.Repeat("/x", 40), SourceStep: "writer"}}, []string{"writer"}, map[string]json.RawMessage{"writer": json.RawMessage(deep)}); !errors.Is(err, ErrValidation) {
		t.Fatal("combined nesting exceeded depth limit", err)
	}
}

func TestRecipeRoutingValidatesTemplateBeforeAnyChildOutput(t *testing.T) {
	base := json.RawMessage(`{"source":null,"items":[0]}`)
	valid := []RecipeValueBinding{{TargetPointer: "/source", SourceStep: "writer", SourcePointer: "/not-yet-produced"}}
	if err := ValidateRecipeRouting(base, valid, []string{"writer"}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []RecipeValueBinding{
		{TargetPointer: "/absent", SourceStep: "writer", SourcePointer: "/text"},
		{TargetPointer: "/source", SourceStep: "critic", SourcePointer: "/text"},
		{TargetPointer: "/source", SourceStep: "writer", SourcePointer: "/~2"},
		{TargetPointer: "/items/01", SourceStep: "writer", SourcePointer: "/text"},
	} {
		if err := ValidateRecipeRouting(base, []RecipeValueBinding{binding}, []string{"writer"}); !errors.Is(err, ErrValidation) {
			t.Fatal(binding, err)
		}
	}
	if err := ValidateRecipeRouting(base, append(valid, valid...), []string{"writer"}); !errors.Is(err, ErrValidation) {
		t.Fatal("duplicate target", err)
	}
}
