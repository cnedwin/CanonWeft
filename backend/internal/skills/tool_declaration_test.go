package skills

import (
	"reflect"
	"testing"
)

func TestToolDeclarationsSupportStringAndStringListWithoutGrantingExecution(t *testing.T) {
	for _, declaration := range []string{"Read Write Bash", "\n  - Read\n  - Write\n  - Bash"} {
		b := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte("---\nname: sample\ndescription: example\nallowed-tools: " + declaration + "\n---\nInstructions.")}}}
		r, err := Analyze(b)
		if err != nil || !reflect.DeepEqual(r.RequestedTools, []string{"Read", "Write", "Bash"}) || r.Qualification != "STATIC_ONLY" {
			t.Fatal(r, err)
		}
	}
	for _, declaration := range []string{"{command: Bash}", "[Read, 123]", "[true]", "[[Bash]]", "false", "null"} {
		b := Bundle{Files: []File{{Path: "SKILL.md", Data: []byte("---\nname: sample\ndescription: example\nallowed-tools: " + declaration + "\n---\nInstructions.")}}}
		if _, err := Analyze(b); err == nil {
			t.Fatal("invalid declaration accepted", declaration)
		}
	}
}
