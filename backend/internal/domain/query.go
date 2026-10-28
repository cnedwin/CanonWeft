package domain

import (
	"errors"
	"strings"
)

var ErrContextBudget = errors.New("上下文容量不足")

// ExpandQuery adds retrieval terms without replacing the original query, evidence,
// or stable identities. An ambiguous alias keeps all matches; it is not a merge.
func ExpandQuery(query string, entities []Entity) (string, []string) {
	terms := []string{query}
	ids := []string{}
	seen := map[string]bool{query: true}
	size := len(query)
	for _, e := range entities {
		names := append([]string{e.Name}, e.Aliases...)
		matched := false
		for _, name := range names {
			if name != "" && strings.Contains(strings.ToLower(query), strings.ToLower(name)) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		ids = append(ids, e.ID)
		for _, name := range names {
			if name != "" && !seen[name] && size+len(name) < 8000 {
				terms = append(terms, name)
				seen[name] = true
				size += len(name) + 1
			}
		}
	}
	return strings.Join(terms, " "), ids
}
