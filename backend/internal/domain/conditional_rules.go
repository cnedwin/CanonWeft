package domain

import (
	"fmt"
	"strings"
)

type RuleException struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Source string          `json:"source"`
	When   []PredicateTest `json:"when"`
}

func validateRuleScope(r WorldRule, entities map[string]bool) error {
	if r.EffectiveFrom != nil && r.EffectiveUntil != nil && *r.EffectiveUntil <= *r.EffectiveFrom {
		return fmt.Errorf("规则有效期结束必须晚于开始: %w", ErrValidation)
	}
	if len(r.When) > 32 || len(r.Exceptions) > 16 {
		return ErrValidation
	}
	conditions := append([]PredicateTest(nil), r.When...)
	ids := map[string]bool{}
	for _, exception := range r.Exceptions {
		if exception.ID == "" || len(exception.ID) > 128 || ids[exception.ID] || strings.TrimSpace(exception.Name) == "" || strings.TrimSpace(exception.Source) == "" || len(exception.When) == 0 || len(exception.When) > 32 {
			return fmt.Errorf("规则例外缺少身份、名称、条件或来源: %w", ErrValidation)
		}
		ids[exception.ID] = true
		conditions = append(conditions, exception.When...)
	}
	for index, c := range conditions {
		// Reuse the same bounded primitive predicate contract; nested rules cannot
		// be supplied here, so recursion terminates at a gate-free primitive.
		primitive := WorldRule{ID: fmt.Sprint(index), Name: "条件", Source: "条件", Subject: c.Subject, Predicate: c.Predicate, Operator: c.Operator, Operand: c.Operand}
		if err := validateRulesAndEvents(Snapshot{Rules: []WorldRule{primitive}}, entities); err != nil {
			return err
		}
	}
	return nil
}

// Conditions in a group are AND; exception groups are OR. Unknown never becomes
// a proof of exemption. Evidence includes every primitive examined.
func predicateGroup(s Snapshot, conditions []PredicateTest, at int64) (string, []string) {
	state := "SATISFIED"
	evidence := []string{}
	for index, c := range conditions {
		one := s
		one.Rules = []WorldRule{{ID: fmt.Sprint(index), Subject: c.Subject, Predicate: c.Predicate, Operator: c.Operator, Operand: c.Operand}}
		result := EvaluateRules(one, at)[0]
		evidence = append(evidence, result.Evidence...)
		if result.Status == "CONFLICT" {
			state = "CONFLICT"
		} else if result.Status == "UNKNOWN" && state != "CONFLICT" {
			state = "UNKNOWN"
		}
	}
	return state, evidence
}
func ruleScope(s Snapshot, r WorldRule, at int64) (RuleResult, bool) {
	result := RuleResult{RuleID: r.ID, Status: "APPLIES", Evidence: []string{}, StoryTime: at}
	if (r.EffectiveFrom != nil && at < *r.EffectiveFrom) || (r.EffectiveUntil != nil && at >= *r.EffectiveUntil) {
		result.Status = "NOT_APPLICABLE"
		result.Reason = "不在规则有效期内"
		return result, false
	}
	if len(r.When) > 0 {
		state, evidence := predicateGroup(s, r.When, at)
		result.Evidence = append(result.Evidence, evidence...)
		if state == "CONFLICT" {
			result.Status = "NOT_APPLICABLE"
			result.Reason = "适用条件明确不成立"
			return result, false
		}
		if state == "UNKNOWN" {
			result.Status = "UNKNOWN"
			result.Reason = "适用条件缺少证据"
			return result, false
		}
	}
	unknown := false
	for _, exception := range r.Exceptions {
		state, evidence := predicateGroup(s, exception.When, at)
		result.Evidence = append(result.Evidence, evidence...)
		if state == "SATISFIED" {
			result.Status = "EXEMPT"
			result.Reason = "明确例外成立：" + exception.Name
			return result, false
		}
		if state == "UNKNOWN" {
			unknown = true
		}
	}
	return result, unknown
}
func ruleUsesFact(r WorldRule, a Assertion) bool {
	if r.Subject == a.Subject && r.Predicate == a.Predicate {
		return true
	}
	for _, c := range r.When {
		if c.Subject == a.Subject && c.Predicate == a.Predicate {
			return true
		}
	}
	for _, exception := range r.Exceptions {
		for _, c := range exception.When {
			if c.Subject == a.Subject && c.Predicate == a.Predicate {
				return true
			}
		}
	}
	return false
}
