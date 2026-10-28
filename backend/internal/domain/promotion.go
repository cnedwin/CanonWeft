package domain

type PromotionSelection struct {
	Entities   []string `json:"entities"`
	Assertions []string `json:"assertions"`
	Rules      []string `json:"rules"`
	Events     []string `json:"events"`
}

func selectItems[T any](base, source []T, selection []string, id func(T) string) ([]T, error) {
	result := append([]T{}, base...)
	values := map[string]T{}
	for _, item := range source {
		values[id(item)] = item
	}
	seen := map[string]bool{}
	for _, key := range selection {
		value, ok := values[key]
		if !ok || seen[key] {
			return nil, ErrValidation
		}
		seen[key] = true
		result = replaceMergeItem(result, []T{value}, key, id)
	}
	return result, nil
}

// Promotion transfers only selected shared semantics. Identity mappings and
// unselected medium-specific records never implicitly flow upstream.
func SelectPromotion(parent, source Snapshot, selection PromotionSelection) (Snapshot, error) {
	result := parent
	if len(selection.Entities)+len(selection.Assertions)+len(selection.Rules)+len(selection.Events) == 0 {
		return result, ErrValidation
	}
	var err error
	result.Entities, err = selectItems(parent.Entities, source.Entities, selection.Entities, func(e Entity) string { return e.ID })
	if err != nil {
		return result, err
	}
	result.Assertions, err = selectItems(parent.Assertions, source.Assertions, selection.Assertions, func(a Assertion) string { return a.ID })
	if err != nil {
		return result, err
	}
	result.Rules, err = selectItems(parent.Rules, source.Rules, selection.Rules, func(r WorldRule) string { return r.ID })
	if err != nil {
		return result, err
	}
	result.Events, err = selectItems(parent.Events, source.Events, selection.Events, func(e StoryEvent) string { return e.ID })
	if err != nil {
		return result, err
	}
	return result, ValidateSnapshot(result)
}
