package domain

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

func EntityKinds() []string {
	return []string{"character", "faction", "species", "location", "event", "item", "profession", "cultivation", "technique", "ability", "relationship", "concept"}
}

type PredicateTest struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Operator  string `json:"operator"`
	Operand   string `json:"operand"`
}
type CapabilityProfile struct {
	Requirements []PredicateTest `json:"requirements"`
	Effects      []string        `json:"effects"`
	Costs        []string        `json:"costs"`
	Limits       []string        `json:"limits"`
	Counters     []string        `json:"counters"`
	Growth       []string        `json:"growth"`
	Related      []string        `json:"related"`
	Source       string          `json:"source"`
}
type EntityMapping struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Target  string   `json:"target"`
	Sources []string `json:"sources"`
	Reason  string   `json:"reason"`
	Source  string   `json:"source"`
}

func authoritative(a Assertion) bool {
	return a.Kind == "fact" && (a.Context == "" || a.Context == "reality" || a.Context == "flashback") && (a.Status == "" || a.Status == "active")
}
func activeFact(a Assertion, at int64) bool {
	return authoritative(a) && a.EffectiveFrom <= at && (a.EffectiveUntil == nil || at < *a.EffectiveUntil)
}

func validateAssertionSemantics(a Assertion, entities map[string]bool) error {
	if !slices.Contains([]string{"", "reality", "flashback", "dream", "rumor", "hypothetical"}, a.Context) || !slices.Contains([]string{"", "active", "disputed", "retracted"}, a.Status) {
		return fmt.Errorf("未知叙述语境或命题状态: %w", ErrValidation)
	}
	if a.HolderID != "" && !entities[a.HolderID] {
		return fmt.Errorf("信念或陈述的持有者不存在: %w", ErrValidation)
	}
	if slices.Contains([]string{"belief", "claim", "intent"}, a.Kind) && a.HolderID == "" {
		return fmt.Errorf("请明确谁相信、说出或计划此事: %w", ErrValidation)
	}
	if a.Confidence != nil && (math.IsNaN(*a.Confidence) || math.IsInf(*a.Confidence, 0) || *a.Confidence < 0 || *a.Confidence > 1) {
		return ErrValidation
	}
	if a.EffectiveUntil != nil && *a.EffectiveUntil <= a.EffectiveFrom {
		return fmt.Errorf("结束时间必须晚于生效时间: %w", ErrValidation)
	}
	return nil
}

func validateProfilesAndMappings(s Snapshot, entities map[string]bool) error {
	for _, e := range s.Entities {
		if slices.Contains([]string{"ability", "profession", "technique", "cultivation"}, e.Kind) && e.Profile == nil {
			return fmt.Errorf("职业、功法和能力需要结构化定义: %w", ErrValidation)
		}
		if e.Profile == nil {
			continue
		}
		p := e.Profile
		if strings.TrimSpace(p.Source) == "" || len(p.Effects) == 0 {
			return fmt.Errorf("能力定义缺少效果或来源: %w", ErrValidation)
		}
		for _, group := range [][]string{p.Effects, p.Costs, p.Limits, p.Counters, p.Growth} {
			for _, text := range group {
				if strings.TrimSpace(text) == "" || len(text) > 10000 {
					return ErrValidation
				}
			}
		}
		for _, id := range p.Related {
			if !entities[id] || id == e.ID {
				return fmt.Errorf("关联职业功法不存在或引用自身: %w", ErrValidation)
			}
		}
		for i, condition := range p.Requirements {
			rule := WorldRule{ID: fmt.Sprint(i), Name: "发动条件", Subject: condition.Subject, Predicate: condition.Predicate, Operator: condition.Operator, Operand: condition.Operand, Source: p.Source}
			if err := validateRulesAndEvents(Snapshot{Rules: []WorldRule{rule}}, entities); err != nil {
				return err
			}
		}
	}
	ids := map[string]bool{}
	graph := map[string][]string{}
	for _, m := range s.Mappings {
		if m.ID == "" || len(m.ID) > 128 || ids[m.ID] || !entities[m.Target] || strings.TrimSpace(m.Reason) == "" || strings.TrimSpace(m.Source) == "" || !slices.Contains([]string{"composite_of", "split_from", "represents"}, m.Kind) {
			return fmt.Errorf("映射身份、对象、理由或来源不完整: %w", ErrValidation)
		}
		if len(m.Sources) == 0 || (m.Kind == "composite_of" && len(m.Sources) < 2) {
			return fmt.Errorf("组合角色至少需要两个来源身份: %w", ErrValidation)
		}
		seen := map[string]bool{}
		for _, source := range m.Sources {
			if !entities[source] || source == m.Target || seen[source] {
				return fmt.Errorf("映射来源不存在、重复或指向自身: %w", ErrValidation)
			}
			seen[source] = true
			graph[m.Target] = append(graph[m.Target], source)
		}
		ids[m.ID] = true
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
		for _, source := range graph[id] {
			if !visit(source) {
				return false
			}
		}
		visiting[id] = false
		done[id] = true
		return true
	}
	for id := range graph {
		if !visit(id) {
			return fmt.Errorf("身份映射存在循环: %w", ErrValidation)
		}
	}
	return nil
}

func EvaluateCapability(s Snapshot, id string, at int64) RuleResult {
	result := RuleResult{RuleID: id, Status: "UNKNOWN", StoryTime: at, Evidence: []string{}, Reason: "能力定义不存在"}
	var profile *CapabilityProfile
	for _, entity := range s.Entities {
		if entity.ID == id && entity.Status != "disabled" {
			profile = entity.Profile
		}
	}
	if profile == nil {
		return result
	}
	result.Status = "SATISFIED"
	result.Reason = "已满足明确的发动条件；效果仍由创作流程审查"
	for i, condition := range profile.Requirements {
		check := s
		check.Rules = []WorldRule{{ID: fmt.Sprint(i), Subject: condition.Subject, Predicate: condition.Predicate, Operator: condition.Operator, Operand: condition.Operand}}
		evaluated := EvaluateRules(check, at)[0]
		result.Evidence = append(result.Evidence, evaluated.Evidence...)
		if evaluated.Status == "CONFLICT" {
			result.Status = "CONFLICT"
			result.Reason = "发动条件不满足"
		} else if evaluated.Status == "UNKNOWN" && result.Status != "CONFLICT" {
			result.Status = "UNKNOWN"
			result.Reason = "发动条件缺少事实证据"
		}
	}
	return result
}
