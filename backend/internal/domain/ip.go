package domain

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Entity struct {
	ID          string             `json:"id"`
	Kind        string             `json:"kind"`
	Name        string             `json:"name"`
	Aliases     []string           `json:"aliases"`
	Description string             `json:"description"`
	Status      string             `json:"status,omitempty"`
	Profile     *CapabilityProfile `json:"profile,omitempty"`
	Locked      bool               `json:"locked,omitempty"`
}
type Assertion struct {
	ID             string   `json:"id"`
	Subject        string   `json:"subject"`
	Predicate      string   `json:"predicate"`
	Value          string   `json:"value"`
	ObjectID       string   `json:"object_id,omitempty"`
	Kind           string   `json:"kind"`
	EffectiveFrom  int64    `json:"effective_from"`
	KnownFrom      int64    `json:"known_from"`
	KnownBy        []string `json:"known_by"`
	Visibility     string   `json:"visibility"`
	RecordedAt     string   `json:"recorded_at"`
	Locked         bool     `json:"locked"`
	Source         string   `json:"source"`
	HolderID       string   `json:"holder_id,omitempty"`
	Context        string   `json:"context,omitempty"`
	Status         string   `json:"status,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	EffectiveUntil *int64   `json:"effective_until,omitempty"`
}
type Snapshot struct {
	Entities   []Entity        `json:"entities"`
	Assertions []Assertion     `json:"assertions"`
	Rules      []WorldRule     `json:"rules,omitempty"`
	Events     []StoryEvent    `json:"events,omitempty"`
	Mappings   []EntityMapping `json:"mappings,omitempty"`
}
type MergeConflict struct {
	AssertionID string `json:"assertion_id"`
	Type        string `json:"type"`
	Reason      string `json:"reason"`
}

func ValidateSnapshot(snapshot Snapshot) error {
	entities := map[string]bool{}
	for _, e := range snapshot.Entities {
		if e.ID == "" || len(e.ID) > 128 || strings.TrimSpace(e.Name) == "" || entities[e.ID] {
			return fmt.Errorf("角色/实体身份重复或名称为空: %w", ErrValidation)
		}
		if !slices.Contains(EntityKinds(), e.Kind) {
			return fmt.Errorf("未知实体类型: %w", ErrValidation)
		}
		entities[e.ID] = true
		if !slices.Contains([]string{"", "active", "disabled"}, e.Status) {
			return fmt.Errorf("未知实体状态: %w", ErrValidation)
		}
	}
	assertions := map[string]bool{}
	facts := map[string]string{}
	for _, a := range snapshot.Assertions {
		if err := validateAssertionSemantics(a, entities); err != nil {
			return err
		}
		if a.ID == "" || len(a.ID) > 128 || assertions[a.ID] || !entities[a.Subject] || strings.TrimSpace(a.Predicate) == "" || strings.TrimSpace(a.Value) == "" || a.Source == "" {
			return fmt.Errorf("断言缺少身份、主体、内容或来源: %w", ErrValidation)
		}
		if a.ObjectID != "" && !entities[a.ObjectID] {
			return fmt.Errorf("关系引用不存在的实体: %w", ErrValidation)
		}
		if !slices.Contains([]string{"fact", "belief", "claim", "intent", "hypothesis"}, a.Kind) {
			return fmt.Errorf("未知命题类别: %w", ErrValidation)
		}
		if !slices.Contains([]string{"", "author", "restricted", "public"}, a.Visibility) {
			return fmt.Errorf("未知可见性: %w", ErrValidation)
		}
		for _, id := range a.KnownBy {
			if !entities[id] {
				return fmt.Errorf("知情角色不存在: %w", ErrValidation)
			}
		}
		if authoritative(a) {
			key := fmt.Sprintf("%s\x00%s\x00%d", a.Subject, a.Predicate, a.EffectiveFrom)
			if value, exists := facts[key]; exists && value != a.Value+"\x00"+a.ObjectID {
				return fmt.Errorf("同一时间存在冲突事实: %w", ErrConflict)
			}
			facts[key] = a.Value + "\x00" + a.ObjectID
		}
		assertions[a.ID] = true
	}
	if err := validateProfilesAndMappings(snapshot, entities); err != nil {
		return err
	}
	return validateRulesAndEvents(snapshot, entities)
}
func KnownFacts(snapshot Snapshot, actor string, storyTime int64) []Assertion {
	result := []Assertion{}
	latest := map[string]int64{}
	for _, a := range snapshot.Assertions {
		if !activeFact(a, storyTime) {
			continue
		}
		key := propertyKey(a.Subject, a.Predicate)
		if at, exists := latest[key]; !exists || a.EffectiveFrom > at {
			latest[key] = a.EffectiveFrom
		}
	}
	for _, a := range snapshot.Assertions {
		if !activeFact(a, storyTime) || a.KnownFrom > storyTime {
			continue
		}
		if a.Visibility == "author" || (a.Visibility != "public" && !slices.Contains(a.KnownBy, actor)) {
			continue
		}
		// A more recent fact for the same property supersedes the older one, never merely by retrieval order.
		if latest[propertyKey(a.Subject, a.Predicate)] == a.EffectiveFrom {
			result = append(result, a)
		}
	}
	return result
}
func CheckLocked(base, next Snapshot) error {
	if err := checkLockedItems(base.Entities, next.Entities, func(e Entity) string { return e.ID }, func(e Entity) bool { return e.Locked }); err != nil {
		return err
	}
	values := map[string]Assertion{}
	for _, a := range next.Assertions {
		values[a.ID] = a
	}
	for _, a := range base.Assertions {
		if !a.Locked {
			continue
		}
		b, exists := values[a.ID]
		if !exists || !reflect.DeepEqual(a, b) {
			return fmt.Errorf("锁定事实 %s 不能直接修改或删除: %w", a.ID, ErrConflict)
		}
	}
	if err := checkLockedItems(base.Rules, next.Rules, func(r WorldRule) string { return r.ID }, func(r WorldRule) bool { return r.Locked }); err != nil {
		return err
	}
	if err := checkLockedItems(base.Events, next.Events, func(e StoryEvent) string { return e.ID }, func(e StoryEvent) bool { return e.Locked }); err != nil {
		return err
	}
	return nil
}
func checkLockedItems[T any](base, next []T, id func(T) string, locked func(T) bool) error {
	values := map[string]T{}
	for _, v := range next {
		values[id(v)] = v
	}
	for _, v := range base {
		if locked(v) {
			if n, ok := values[id(v)]; !ok || !reflect.DeepEqual(v, n) {
				return fmt.Errorf("锁定条目 %s 不能直接修改或删除: %w", id(v), ErrConflict)
			}
		}
	}
	return nil
}
func mergeItems[T any](base, upstream, local []T, id func(T) string) ([]T, []MergeConflict) {
	b, u, l := map[string]T{}, map[string]T{}, map[string]T{}
	keys := []string{}
	seen := map[string]bool{}
	for _, group := range [][]T{base, upstream, local} {
		for _, item := range group {
			key := id(item)
			if !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
	}
	for _, v := range base {
		b[id(v)] = v
	}
	for _, v := range upstream {
		u[id(v)] = v
	}
	for _, v := range local {
		l[id(v)] = v
	}
	out := []T{}
	conflicts := []MergeConflict{}
	for _, key := range keys {
		bv, bok := b[key]
		uv, uok := u[key]
		lv, lok := l[key]
		localUnchanged := bok == lok && reflect.DeepEqual(bv, lv)
		upstreamUnchanged := bok == uok && reflect.DeepEqual(bv, uv)
		same := uok == lok && reflect.DeepEqual(uv, lv)
		switch {
		case localUnchanged:
			if uok {
				out = append(out, uv)
			}
		case upstreamUnchanged || same:
			if lok {
				out = append(out, lv)
			}
		default:
			conflicts = append(conflicts, MergeConflict{AssertionID: key, Reason: "上游与本地同时修改或删除，需要明确解决"})
			if lok {
				out = append(out, lv)
			}
		}
	}
	return out, conflicts
}
func MergeSnapshots(base, upstream, local Snapshot) (Snapshot, []MergeConflict) {
	entities, ec := mergeItems(base.Entities, upstream.Entities, local.Entities, func(v Entity) string { return v.ID })
	assertions, ac := mergeItems(base.Assertions, upstream.Assertions, local.Assertions, func(v Assertion) string { return v.ID })
	rules, rc := mergeItems(base.Rules, upstream.Rules, local.Rules, func(v WorldRule) string { return v.ID })
	events, evc := mergeItems(base.Events, upstream.Events, local.Events, func(v StoryEvent) string { return v.ID })
	mappings, mc := mergeItems(base.Mappings, upstream.Mappings, local.Mappings, func(v EntityMapping) string { return v.ID })
	for i := range ec {
		ec[i].Type = "entity"
	}
	for i := range ac {
		ac[i].Type = "assertion"
	}
	for i := range rc {
		rc[i].Type = "rule"
	}
	for i := range evc {
		evc[i].Type = "event"
	}
	for i := range mc {
		mc[i].Type = "mapping"
	}
	conflicts := append(append(append(append(ec, ac...), rc...), evc...), mc...)
	return Snapshot{Entities: entities, Assertions: assertions, Rules: rules, Events: events, Mappings: mappings}, conflicts
}
