// skillprobe validates public source fetching without a model call or code execution.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"novelstudio/internal/skills"
	"os"
)

func main() {
	repo := flag.String("repo", "anthropics/skills", "public repository")
	ref := flag.String("ref", "main", "branch, tag or commit")
	directory := flag.String("path", "skills/skill-creator", "skill directory")
	flag.Parse()
	bundle, source, err := skills.NewFetcher(nil).Fetch(context.Background(), skills.RemoteRequest{Repository: *repo, Ref: *ref, Path: *directory})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report, err := skills.Analyze(bundle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(struct {
		Source skills.RemoteSource `json:"source"`
		Report skills.Report       `json:"report"`
	}{source, report}); err != nil {
		os.Exit(1)
	}
}
