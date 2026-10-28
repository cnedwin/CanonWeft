package domain

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

type WorldRule struct {
	EffectiveFrom  *int64          `json:"effective_from,omitempty"`
	EffectiveUntil *int64          `json:"effective_until,omitempty"`
	When           []PredicateTest `json:"when,omitempty"`
	Exceptions     []RuleException `json:"exceptions,omitempty"`
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Subject        string          `json:"subject"`
	Predicate      string          `json:"predicate"`
	Operator       string          `json:"operator"`
	Operand        string          `json:"operand"`
	Source         string          `json:"source"`
	Locked         bool            `json:"locked"`
	RecordedAt     string          `json:"recorded_at"`
}
type StoryEvent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Time         int64    `json:"time"`
	Participants []string `json:"participants"`
	Causes       []string `json:"causes"`
	Source       string   `json:"source"`
	Locked       bool     `json:"locked"`
	RecordedAt   string   `json:"recorded_at"`
}
type RuleResult struct {
	RuleID    string   `json:"rule_id"`
	Status    string   `json:"status"`
	Evidence  []string `json:"evidence"`
	Reason    string   `json:"reason"`
	StoryTime int64    `json:"story_time"`
}

func validateRulesAndEvents(s Snapshot, entities map[string]bool) error {
	ids := map[string]bool{}
	for _, r := range s.Rules {
		if err := validateRuleScope(r, entities); err != nil {
			return err
		}
		if r.ID == "" || len(r.ID) > 128 || ids[r.ID] || strings.TrimSpace(r.Name) == "" || !entities[r.Subject] || r.Predicate == "" || r.Source == "" {
			return fmt.Errorf("世界规则缺少身份、名称、主体或来源: %w", ErrValidation)
		}
		if !slices.Contains([]string{"equals", "not_equals", "number_lte", "number_gte"}, r.Operator) {
			return fmt.Errorf("不支持的规则运算: %w", ErrValidation)
		}
		if strings.HasPrefix(r.Operator, "number_") {
			if _, ok := finiteNumber(r.Operand); !ok {
				return fmt.Errorf("数值规则缺少有效数值: %w", ErrValidation)
			}
		}
		ids[r.ID] = true
	}
	events := map[string]StoryEvent{}
	for _, e := range s.Events {
		if e.ID == "" || len(e.ID) > 128 || strings.TrimSpace(e.Name) == "" || e.Source == "" {
			return fmt.Errorf("事件缺少身份、名称或来源: %w", ErrValidation)
		}
		if _, ok := events[e.ID]; ok {
			return fmt.Errorf("事件身份重复: %w", ErrValidation)
		}
		for _, id := range e.Participants {
			if !entities[id] {
				return fmt.Errorf("事件参与者不存在: %w", ErrValidation)
			}
		}
		events[e.ID] = e
	}
	for _, e := range s.Events {
		for _, id := range e.Causes {
			cause, ok := events[id]
			if !ok || cause.Time > e.Time {
				return fmt.Errorf("事件原因不存在或晚于结果: %w", ErrValidation)
			}
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		visiting[id] = true
		for _, cause := range events[id].Causes {
			if !visit(cause) {
				return false
			}
		}
		visiting[id] = false
		done[id] = true
		return true
	}
	for id := range events {
		if !visit(id) {
			return fmt.Errorf("事件因果形成循环: %w", ErrValidation)
		}
	}
	return nil
}
func finiteNumber(value string) (float64, bool) {
	n, err := strconv.ParseFloat(value, 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

// EvaluateRules uses authoritative facts, regardless of viewpoint visibility. Missing or
// non-numeric evidence stays UNKNOWN and must never be presented as a successful review.
func EvaluateRules(s Snapshot, storyTime int64) []RuleResult {
	results := []RuleResult{}
	for _, r := range s.Rules {
		result := RuleResult{RuleID: r.ID, Status: "UNKNOWN", Evidence: []string{}, Reason: "没有可验证的事实", StoryTime: storyTime}
		scope, exceptionUnknown := ruleScope(s, r, storyTime)
		if scope.Status != "APPLIES" {
			results = append(results, scope)
			continue
		}
		var selected *Assertion
		for i := range s.Assertions {
			a := &s.Assertions[i]
			if activeFact(*a, storyTime) && a.Subject == r.Subject && a.Predicate == r.Predicate && (selected == nil || a.EffectiveFrom > selected.EffectiveFrom) {
				selected = a
			}
		}
		if selected != nil {
			result.Evidence = []string{selected.ID}
			valid, known := false, true
			switch r.Operator {
			case "equals":
				valid = selected.Value == r.Operand
			case "not_equals":
				valid = selected.Value != r.Operand
			case "number_lte", "number_gte":
				left, lok := finiteNumber(selected.Value)
				right, rok := finiteNumber(r.Operand)
				known = lok && rok
				if r.Operator == "number_lte" {
					valid = left <= right
				} else {
					valid = left >= right
				}
			default:
				known = false
			}
			if known {
				result.Status = "CONFLICT"
				result.Reason = "事实违反规则"
				if valid {
					result.Status = "SATISFIED"
					result.Reason = "事实满足规则"
				}
			} else {
				result.Reason = "现有事实无法按规则解释"
			}
		}
		result.Evidence = append(scope.Evidence, result.Evidence...)
		if exceptionUnknown && result.Status != "SATISFIED" {
			result.Status = "UNKNOWN"
			result.Reason = "例外条件缺少证据，不能确定是否豁免"
		}
		results = append(results, result)
	}
	return results
}

func ReviewRules(s Snapshot) []RuleResult {
	results := []RuleResult{}
	for _, rule := range s.Rules {
		moments := map[int64]bool{}
		if rule.EffectiveFrom != nil {
			moments[*rule.EffectiveFrom] = true
		}
		if rule.EffectiveUntil != nil {
			moments[*rule.EffectiveUntil] = true
		}
		for _, a := range s.Assertions {
			if authoritative(a) && ruleUsesFact(rule, a) {
				moments[a.EffectiveFrom] = true
				if a.EffectiveUntil != nil {
					moments[*a.EffectiveUntil] = true
				}
			}
		}
		if len(moments) == 0 {
			moments[0] = true
		}
		times := []int64{}
		for moment := range moments {
			times = append(times, moment)
		}
		slices.Sort(times)
		one := s
		one.Rules = []WorldRule{rule}
		for _, moment := range times {
			results = append(results, EvaluateRules(one, moment)...)
		}
	}
	return results
}
func ValidateRuleEvidence(s Snapshot) error {
	for _, result := range ReviewRules(s) {
		if result.Status == "CONFLICT" {
			return fmt.Errorf("世界规则 %s 在故事时间 %d 存在冲突: %w", result.RuleID, result.StoryTime, ErrConflict)
		}
		if result.Status == "UNKNOWN" {
			return fmt.Errorf("世界规则 %s 缺少可验证事实，请补齐后采用: %w", result.RuleID, ErrNotReady)
		}
	}
	return nil
}

// StampSnapshot never trusts a client-provided recorded time. An unchanged item keeps
// its original timestamp; changed/new content receives the server's current time.
func StampSnapshot(base Snapshot, next *Snapshot, now time.Time) {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	assertions := map[string]Assertion{}
	for _, a := range base.Assertions {
		assertions[a.ID] = a
	}
	for i := range next.Assertions {
		a := &next.Assertions[i]
		old, ok := assertions[a.ID]
		previous := old.RecordedAt
		old.RecordedAt = ""
		a.RecordedAt = ""
		if ok && reflect.DeepEqual(old, *a) {
			a.RecordedAt = previous
		} else {
			a.RecordedAt = timestamp
		}
	}
	rules := map[string]WorldRule{}
	for _, r := range base.Rules {
		rules[r.ID] = r
	}
	for i := range next.Rules {
		r := &next.Rules[i]
		old, ok := rules[r.ID]
		previous := old.RecordedAt
		old.RecordedAt = ""
		r.RecordedAt = ""
		if ok && reflect.DeepEqual(old, *r) {
			r.RecordedAt = previous
		} else {
			r.RecordedAt = timestamp
		}
	}
	events := map[string]StoryEvent{}
	for _, e := range base.Events {
		events[e.ID] = e
	}
	for i := range next.Events {
		e := &next.Events[i]
		old, ok := events[e.ID]
		previous := old.RecordedAt
		old.RecordedAt = ""
		e.RecordedAt = ""
		if ok && reflect.DeepEqual(old, *e) {
			e.RecordedAt = previous
		} else {
			e.RecordedAt = timestamp
		}
	}
}
