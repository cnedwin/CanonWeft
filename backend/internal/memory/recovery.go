package memory

import (
	"encoding/json"
	"errors"
	"reflect"
)

// RecoveryMap contains identities assigned by the project-copy transaction.
// It is not authority to read, restore or publish anything. Resource pins must
// describe already verified, byte-identical content in the destination graph.
type RecoveryMap struct {
	SourceProject string
	TargetProject string
	Branches      map[string]string
	Sessions      map[string]string
	Items         map[string]string
	Revisions     map[string]string
	Resources     map[RecoverySource]RecoverySource
}

type RecoverySource struct {
	Resource string
	Revision string
	Hash     string
}

type RecoveryRevision struct {
	Item   Item
	Parent string
}

type RecoveryGraph struct {
	Sessions         []RecoverySession
	Heads            []Item
	Revisions        []RecoveryRevision
	ExpiredHeads     []RecoveryHeadIdentity
	ExpiredRevisions []RecoveryRevisionIdentity
}

type RecoverySession struct {
	// Expired carries metadata only. The service must write the independently
	// verified expiry envelope, never serialize these empty fields as a session.
	Expired  bool
	ID       string
	Project  string
	Branch   string
	Actor    string
	Revision int64
	Title    string
	Goal     string
	Notes    string
	FocusIDs []string
}

var ErrRecoveryGraph = errors.New("memory recovery graph is incomplete or inconsistent")

// RemapRecovery preserves all current and historical memory documents, with
// exact provenance and privacy. It never promotes a candidate, changes user
// text, rewrites a character identity or transfers ownership of private work.
// Nothing is returned on failure, and the source graph is never mutated.
func RemapRecovery(source RecoveryGraph, ids RecoveryMap) (RecoveryGraph, error) {
	if !nonempty(ids.SourceProject) || !nonempty(ids.TargetProject) || ids.SourceProject == ids.TargetProject {
		return RecoveryGraph{}, ErrRecoveryGraph
	}
	for _, group := range []map[string]string{ids.Branches, ids.Sessions, ids.Items, ids.Revisions} {
		seen := map[string]bool{}
		for from, to := range group {
			if !nonempty(from) || !nonempty(to) || from == to || seen[to] {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
			if _, overlapsSource := group[to]; overlapsSource {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
			seen[to] = true
		}
	}
	resourceIDs, releaseIDs := map[string]string{}, map[string]string{}
	resourceTargets, releaseTargets := map[string]string{}, map[string]string{}
	for from, to := range ids.Resources {
		for _, pair := range []struct {
			from, to         string
			forward, reverse map[string]string
		}{{from.Resource, to.Resource, resourceIDs, resourceTargets}, {from.Revision, to.Revision, releaseIDs, releaseTargets}} {
			if !nonempty(pair.from) || !nonempty(pair.to) || pair.from == pair.to || (pair.forward[pair.from] != "" && pair.forward[pair.from] != pair.to) || (pair.reverse[pair.to] != "" && pair.reverse[pair.to] != pair.from) {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
			pair.forward[pair.from], pair.reverse[pair.to] = pair.to, pair.from
		}
		if from.Hash != to.Hash || !hashPattern.MatchString(from.Hash) {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
	}
	for from := range resourceIDs {
		if resourceTargets[from] != "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
	}
	for from := range releaseIDs {
		if releaseTargets[from] != "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
	}
	heads, _, err := recoveryStructure(source, ids.SourceProject)
	if err != nil {
		return RecoveryGraph{}, err
	}
	versions := make(map[string]RecoveryRevision, len(source.Revisions))
	for _, revision := range source.Revisions {
		item := revision.Item
		if _, duplicate := versions[item.Revision]; duplicate {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		versions[item.Revision] = revision
	}
	for _, head := range source.Heads {
		version, ok := versions[head.Revision]
		if ok && !reflect.DeepEqual(head, version.Item) {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
	}
	sessions := make(map[string]RecoverySession, len(source.Sessions))
	for _, session := range source.Sessions {
		if session.Expired && (session.Title != "" || session.Goal != "" || session.Notes != "" || len(session.FocusIDs) != 0) {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		if _, duplicate := sessions[session.ID]; duplicate || session.Project != ids.SourceProject || !nonempty(session.Actor) || session.Revision < 1 || ids.Sessions[session.ID] == "" || ids.Branches[session.Branch] == "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		sessions[session.ID] = session
	}
	remap := func(item Item) (Item, error) {
		if item.Project != ids.SourceProject || ValidateItem(item) != nil {
			return Item{}, ErrRecoveryGraph
		}
		// Copy slices, nested summaries and optional clocks before changing IDs.
		raw, err := json.Marshal(item)
		if err != nil {
			return Item{}, ErrRecoveryGraph
		}
		var next Item
		if json.Unmarshal(raw, &next) != nil {
			return Item{}, ErrRecoveryGraph
		}
		next.Project = ids.TargetProject
		next.ID = ids.Items[item.ID]
		next.Revision = ids.Revisions[item.Revision]
		next.Branch = ids.Branches[item.Branch]
		if item.Session != "" {
			session, ok := sessions[item.Session]
			if !ok || session.Actor != item.Actor || session.Branch != item.Branch {
				return Item{}, ErrRecoveryGraph
			}
			next.Session = ids.Sessions[item.Session]
			if next.Session == "" {
				return Item{}, ErrRecoveryGraph
			}
		}
		for i, pin := range item.Sources {
			mapped, ok := ids.Resources[RecoverySource{pin.Resource, pin.Revision, pin.Hash}]
			if !ok || mapped.Resource == "" || mapped.Revision == "" || mapped.Resource == pin.Resource || mapped.Revision == pin.Revision || mapped.Hash != pin.Hash {
				return Item{}, ErrRecoveryGraph
			}
			next.Sources[i].Resource, next.Sources[i].Revision = mapped.Resource, mapped.Revision
		}
		for i, dependency := range item.Dependencies {
			if _, ok := heads[dependency]; !ok || ids.Items[dependency] == "" {
				return Item{}, ErrRecoveryGraph
			}
			next.Dependencies[i] = ids.Items[dependency]
		}
		if item.Summary != nil {
			for i, pin := range item.Summary.Inputs {
				child, ok := versions[pin.Revision]
				if !ok || ValidateSummarySource(item, child.Item, pin) != nil || ids.Items[pin.ID] == "" || ids.Revisions[pin.Revision] == "" {
					return Item{}, ErrRecoveryGraph
				}
				next.Summary.Inputs[i].ID = ids.Items[pin.ID]
				next.Summary.Inputs[i].Revision = ids.Revisions[pin.Revision]
			}
		}
		if ValidateItem(next) != nil {
			return Item{}, ErrRecoveryGraph
		}
		return next, nil
	}
	result := RecoveryGraph{Sessions: make([]RecoverySession, 0, len(source.Sessions)), Heads: make([]Item, 0, len(source.Heads)), Revisions: make([]RecoveryRevision, 0, len(source.Revisions))}
	for _, head := range source.ExpiredHeads {
		next := head
		next.ID, next.Project, next.Branch, next.Revision = ids.Items[head.ID], ids.TargetProject, ids.Branches[head.Branch], ids.Revisions[head.Revision]
		if next.ID == "" || next.Branch == "" || next.Revision == "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		if head.Session != "" {
			session, ok := sessions[head.Session]
			if !ok || session.Actor != head.Actor || session.Branch != head.Branch || ids.Sessions[head.Session] == "" {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
			next.Session = ids.Sessions[head.Session]
		} else if head.Actor != "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		result.ExpiredHeads = append(result.ExpiredHeads, next)
	}
	for _, version := range source.ExpiredRevisions {
		next := version
		next.ID, next.Project, next.ItemID = ids.Revisions[version.ID], ids.TargetProject, ids.Items[version.ItemID]
		if next.ID == "" || next.ItemID == "" {
			return RecoveryGraph{}, ErrRecoveryGraph
		}
		if version.Parent != "" {
			next.Parent = ids.Revisions[version.Parent]
			if next.Parent == "" {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
		}
		result.ExpiredRevisions = append(result.ExpiredRevisions, next)
	}
	for _, session := range source.Sessions {
		next := session
		next.ID, next.Project, next.Branch = ids.Sessions[session.ID], ids.TargetProject, ids.Branches[session.Branch]
		next.FocusIDs = make([]string, len(session.FocusIDs))
		seen := map[string]bool{}
		for i, focus := range session.FocusIDs {
			item, ok := heads[focus]
			if !ok || seen[focus] || item.Branch != session.Branch || (item.Actor != "" && item.Actor != session.Actor) || ids.Items[focus] == "" {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
			seen[focus], next.FocusIDs[i] = true, ids.Items[focus]
		}
		result.Sessions = append(result.Sessions, next)
	}
	for _, head := range source.Heads {
		next, err := remap(head)
		if err != nil {
			return RecoveryGraph{}, err
		}
		result.Heads = append(result.Heads, next)
	}
	for _, version := range source.Revisions {
		next, err := remap(version.Item)
		if err != nil {
			return RecoveryGraph{}, err
		}
		parent := ""
		if version.Parent != "" {
			parent = ids.Revisions[version.Parent]
			if parent == "" {
				return RecoveryGraph{}, ErrRecoveryGraph
			}
		}
		result.Revisions = append(result.Revisions, RecoveryRevision{Item: next, Parent: parent})
	}
	return result, nil
}
