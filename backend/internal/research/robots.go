package research

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type robotRule struct {
	pattern string
	allow   bool
}
type robotGroup struct {
	agents []string
	rules  []robotRule
	delay  time.Duration
}

func robotsPermission(text, path string) (bool, time.Duration, error) {
	if len(text) > 128*1024 {
		return false, 0, ErrSource
	}
	groups := []robotGroup{}
	group := robotGroup{}
	startedRules := false
	flush := func() {
		if len(group.agents) > 0 {
			groups = append(groups, group)
		}
		group = robotGroup{}
		startedRules = false
	}
	for _, line := range strings.Split(strings.TrimPrefix(text, "\uFEFF"), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			if startedRules {
				flush()
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return false, 0, ErrSource
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if startedRules {
				flush()
			}
			if len(value) > 128 {
				return false, 0, ErrSource
			}
			group.agents = append(group.agents, strings.ToLower(value))
		case "allow", "disallow":
			if len(group.agents) == 0 {
				continue
			}
			startedRules = true
			if len(value) > 2000 || len(group.rules) >= 256 {
				return false, 0, ErrSource
			}
			if value != "" {
				group.rules = append(group.rules, robotRule{value, key == "allow"})
			}
		case "crawl-delay":
			if len(group.agents) == 0 {
				continue
			}
			startedRules = true
			n, e := strconv.ParseFloat(value, 64)
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 10 {
				return false, 0, ErrSource
			}
			group.delay = time.Duration(n * float64(time.Second))
		}
	}
	flush()
	bestAgent := -1
	applicable := []robotGroup{}
	for _, g := range groups {
		score := -1
		for _, agent := range g.agents {
			if agent == "*" && score < 0 {
				score = 0
			}
			if agent != "" && agent != "*" && strings.Contains("novelstudio-research", agent) && len(agent) > score {
				score = len(agent)
			}
		}
		if score > bestAgent {
			applicable = nil
			bestAgent = score
		}
		if score >= 0 && score == bestAgent {
			applicable = append(applicable, g)
		}
	}
	allowed, bestLength, delay := true, -1, time.Duration(0)
	for _, g := range applicable {
		if g.delay > delay {
			delay = g.delay
		}
		for _, rule := range g.rules {
			pattern := rule.pattern
			anchored := strings.HasSuffix(pattern, "$")
			pattern = strings.TrimSuffix(pattern, "$")
			expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), "\\*", ".*")
			if anchored {
				expression += "$"
			}
			matched, e := regexp.MatchString(expression, path)
			if e != nil {
				return false, 0, ErrSource
			}
			length := len(strings.ReplaceAll(pattern, "*", ""))
			if matched && (length > bestLength || (length == bestLength && rule.allow)) {
				allowed, bestLength = rule.allow, length
			}
		}
	}
	return allowed, delay, nil
}
