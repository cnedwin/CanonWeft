package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// DeltaSource is a reference, not a grant. Services populate these from records
// already authorized for the current project and exact revision.
type DeltaSource struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
}

type DeltaOperation struct {
	Kind       string          `json:"kind"`
	ObjectKind string          `json:"object_kind"`
	ObjectID   string          `json:"object_id"`
	Field      string          `json:"field"`
	BeforeHash string          `json:"before_hash"`
	AfterHash  string          `json:"after_hash"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Reason     string          `json:"reason"`
	SourceRefs []DeltaSource   `json:"source_refs,omitempty"`
}

func (o DeltaOperation) Key() string { return o.ObjectKind + ":" + o.ObjectID + ":" + o.Field }

type BranchDelta struct {
	Schema     int              `json:"schema"`
	BaseHash   string           `json:"base_hash"`
	ResultHash string           `json:"result_hash"`
	Operations []DeltaOperation `json:"operations"`
}

type EffectiveSnapshot struct {
	Snapshot   Snapshot         `json:"snapshot"`
	Provenance []DeltaOperation `json:"provenance"`
	Conflicts  []MergeConflict  `json:"conflicts"`
}

type deltaObjects map[string]map[string]json.RawMessage

var deltaCollections = map[string]string{"entity": "entities", "assertion": "assertions", "rule": "rules", "event": "events", "mapping": "mappings"}

func deltaDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func deltaRaw(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func snapshotDeltaObjects(snapshot Snapshot) (deltaObjects, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var collections map[string][]json.RawMessage
	if err = json.Unmarshal(raw, &collections); err != nil {
		return nil, err
	}
	result := deltaObjects{}
	for kind, collection := range deltaCollections {
		result[kind] = map[string]json.RawMessage{}
		for _, item := range collections[collection] {
			var identity struct {
				ID string `json:"id"`
			}
			if err = json.Unmarshal(item, &identity); err != nil || identity.ID == "" {
				return nil, ErrValidation
			}
			if _, exists := result[kind][identity.ID]; exists {
				return nil, ErrValidation
			}
			result[kind][identity.ID] = item
		}
	}
	return result, nil
}

// BuildBranchDelta compares typed JSON without decoding numbers through float64.
// Top-level object fields are atomic; nested profiles/rules retain their types.
func BuildBranchDelta(base, next Snapshot, reasons map[string]string) (BranchDelta, error) {
	return buildBranchDelta(base, next, reasons, true)
}

func PreviewBranchDelta(base, next Snapshot) (BranchDelta, error) {
	return buildBranchDelta(base, next, nil, false)
}

func buildBranchDelta(base, next Snapshot, reasons map[string]string, requireReasons bool) (BranchDelta, error) {
	var delta BranchDelta
	before, err := snapshotDeltaObjects(base)
	if err != nil {
		return delta, err
	}
	after, err := snapshotDeltaObjects(next)
	if err != nil {
		return delta, err
	}
	delta = BranchDelta{Schema: 1, BaseHash: deltaDigest(deltaRaw(before)), ResultHash: deltaDigest(deltaRaw(after)), Operations: []DeltaOperation{}}
	used := map[string]bool{}
	add := func(kind, id, field, operation string, old, new json.RawMessage) error {
		if old == nil {
			old = json.RawMessage("null")
		}
		if new == nil {
			new = json.RawMessage("null")
		}
		entry := DeltaOperation{Kind: operation, ObjectKind: kind, ObjectID: id, Field: field, BeforeHash: deltaDigest(old), AfterHash: deltaDigest(new), Before: old, After: new}
		entry.Reason = strings.TrimSpace(reasons[entry.Key()])
		if (requireReasons && entry.Reason == "") || len(entry.Reason) > 4000 {
			return fmt.Errorf("每项变更须填写理由 (%s): %w", entry.Key(), ErrValidation)
		}
		used[entry.Key()] = true
		delta.Operations = append(delta.Operations, entry)
		return nil
	}
	for kind, items := range before {
		ids := map[string]bool{}
		for id := range items {
			ids[id] = true
		}
		for id := range after[kind] {
			ids[id] = true
		}
		for id := range ids {
			old, oldExists := items[id]
			new, newExists := after[kind][id]
			if !oldExists {
				err = add(kind, id, "", "add", nil, new)
			} else if !newExists {
				err = add(kind, id, "", "remove", old, nil)
			} else {
				var oldFields, newFields map[string]json.RawMessage
				if err = json.Unmarshal(old, &oldFields); err != nil {
					return delta, err
				}
				if err = json.Unmarshal(new, &newFields); err != nil {
					return delta, err
				}
				fields := map[string]bool{}
				for field := range oldFields {
					fields[field] = true
				}
				for field := range newFields {
					fields[field] = true
				}
				for field := range fields {
					if bytes.Equal(oldFields[field], newFields[field]) {
						continue
					}
					operation := "replace"
					if field == "status" && string(newFields[field]) == `"disabled"` {
						operation = "disable"
					}
					if err = add(kind, id, "/"+field, operation, oldFields[field], newFields[field]); err != nil {
						return delta, err
					}
				}
			}
			if err != nil {
				return delta, err
			}
		}
	}
	for key := range reasons {
		if !used[key] {
			return delta, fmt.Errorf("理由没有对应的实际变更 (%s): %w", key, ErrValidation)
		}
	}
	slices.SortFunc(delta.Operations, func(a, b DeltaOperation) int { return strings.Compare(a.Key(), b.Key()) })
	return delta, nil
}

// ApplyBranchDelta rejects stale or tampered values before constructing a new
// snapshot. No caller-supplied path can escape one existing typed object field.
func ApplyBranchDelta(base Snapshot, delta BranchDelta) (EffectiveSnapshot, error) {
	result := EffectiveSnapshot{Provenance: delta.Operations, Conflicts: []MergeConflict{}}
	objects, err := snapshotDeltaObjects(base)
	if err != nil {
		return result, err
	}
	if delta.Schema != 1 {
		return result, ErrValidation
	}
	if delta.BaseHash != deltaDigest(deltaRaw(objects)) {
		return result, ErrConflict
	}
	seen := map[string]bool{}
	for _, op := range delta.Operations {
		if seen[op.Key()] || strings.TrimSpace(op.Reason) == "" || len(op.Reason) > 4000 || objects[op.ObjectKind] == nil || op.ObjectID == "" {
			return result, ErrValidation
		}
		seen[op.Key()] = true
		if !json.Valid(op.Before) || !json.Valid(op.After) || op.BeforeHash != deltaDigest(op.Before) || op.AfterHash != deltaDigest(op.After) {
			return result, ErrConflict
		}
		old, exists := objects[op.ObjectKind][op.ObjectID]
		if op.Field == "" {
			switch op.Kind {
			case "add":
				if exists || string(op.Before) != "null" {
					return result, ErrConflict
				}
				var identity struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(op.After, &identity) != nil || identity.ID != op.ObjectID {
					return result, ErrValidation
				}
				objects[op.ObjectKind][op.ObjectID] = op.After
			case "remove":
				if !exists || !bytes.Equal(old, op.Before) || string(op.After) != "null" {
					return result, ErrConflict
				}
				delete(objects[op.ObjectKind], op.ObjectID)
			default:
				return result, ErrValidation
			}
			continue
		}
		if !exists || !strings.HasPrefix(op.Field, "/") || strings.Contains(op.Field[1:], "/") || op.Field == "/id" || !slices.Contains([]string{"replace", "disable"}, op.Kind) {
			return result, ErrValidation
		}
		field := op.Field[1:]
		if op.Kind == "disable" && (field != "status" || string(op.After) != `"disabled"`) {
			return result, ErrValidation
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(old, &fields); err != nil {
			return result, err
		}
		prior := fields[field]
		if prior == nil {
			prior = json.RawMessage("null")
		}
		if !bytes.Equal(prior, op.Before) {
			return result, ErrConflict
		}
		if string(op.After) == "null" {
			delete(fields, field)
		} else {
			fields[field] = op.After
		}
		objects[op.ObjectKind][op.ObjectID] = deltaRaw(fields)
	}
	collections := map[string][]json.RawMessage{}
	for kind, items := range objects {
		keys := make([]string, 0, len(items))
		for id := range items {
			keys = append(keys, id)
		}
		slices.Sort(keys)
		collections[deltaCollections[kind]] = []json.RawMessage{}
		for _, id := range keys {
			collections[deltaCollections[kind]] = append(collections[deltaCollections[kind]], items[id])
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(deltaRaw(collections)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result.Snapshot); err != nil {
		return result, ErrValidation
	}
	if err = ValidateSnapshot(result.Snapshot); err != nil {
		return result, err
	}
	normalized, err := snapshotDeltaObjects(result.Snapshot)
	if err != nil {
		return result, err
	}
	if delta.ResultHash != deltaDigest(deltaRaw(normalized)) {
		return result, ErrConflict
	}
	return result, nil
}
