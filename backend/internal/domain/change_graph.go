package domain

// DependencyGraph is the graph captured when a plan was created, not a lookup
// of whichever revisions happen to be current during later review.
type DependencyEntry struct {
	Ref        DependencyRef `json:"ref"`
	Name       string        `json:"name"`
	BranchName string        `json:"branch_name"`
}

type DependencyGraph struct {
	Entries   []DependencyEntry
	Links     []DependencyLink
	Frozen    map[string]bool
	Untracked []DependencyRef
}
