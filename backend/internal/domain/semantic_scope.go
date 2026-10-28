package domain

import (
	"encoding/json"
	"slices"
	"strings"
)

// SemanticContentScopeHash identifies equivalent negative-review material. It
// never grants a PASS to another proposal. Receipt authorization still requires
// the exact original target ID/hash. Only administrative identity, system record
// times and unordered object/set ordering are removed; actual prose is unchanged.
func SemanticContentScopeHash(doc SemanticReviewDocument) (string, error) {
	objects, err := snapshotDeltaObjects(doc.Snapshot)
	if err != nil {
		return "", err
	}
	for kind, items := range objects {
		for id, raw := range items {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				return "", err
			}
			if kind == "assertion" || kind == "rule" || kind == "event" {
				delete(fields, "recorded_at")
			}
			for _, field := range []string{"aliases", "known_by", "participants", "causes", "sources"} {
				if value, ok := fields[field]; ok {
					var set []string
					if err = json.Unmarshal(value, &set); err != nil {
						return "", err
					}
					set = append([]string{}, set...)
					slices.Sort(set)
					set = slices.Compact(set)
					fields[field], err = json.Marshal(set)
					if err != nil {
						return "", err
					}
				}
			}
			items[id], err = json.Marshal(fields)
			if err != nil {
				return "", err
			}
		}
	}
	type artifactScope struct{ Resource, Base, IPHead, Content string }
	artifacts := make([]artifactScope, 0, len(doc.Artifacts))
	for _, a := range doc.Artifacts {
		content := deltaDigest([]byte(a.Text))
		if a.Target.ContentHash != "" && a.Target.ContentHash != content {
			return "", ErrConflict
		}
		artifacts = append(artifacts, artifactScope{a.Target.ResourceID, a.Target.ExpectedHead, a.Target.IPHead, content})
	}
	slices.SortFunc(artifacts, func(a, b artifactScope) int { return strings.Compare(a.Resource, b.Resource) })
	peerInputs := []string{}
	for _, anchor := range doc.Anchors {
		parts := strings.SplitN(anchor.Key, ":", 3)
		if len(parts) != 3 || parts[0] != "peer" {
			continue
		}
		branch := ""
		for _, artifact := range doc.Artifacts {
			if artifact.Target.ReleaseID == parts[1] {
				branch = artifact.Target.BranchID
				break
			}
		}
		if branch == "" {
			return "", ErrConflict
		}
		peerInputs = append(peerInputs, branch+":"+parts[2]+":"+deltaDigest([]byte(anchor.Text)))
	}
	slices.Sort(peerInputs)
	value := struct {
		Schema                    int
		Kind, Branch, Base, Scope string
		Snapshot                  deltaObjects
		Artifacts                 []artifactScope
		PeerInputs                []string `json:"PeerInputs,omitempty"`
	}{1, doc.Target.Kind, doc.Target.BranchID, doc.Target.ExpectedIPHead, doc.Scope, objects, artifacts, peerInputs}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return deltaDigest(raw), nil
}
