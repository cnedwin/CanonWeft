package retention

func sourceRequestMatches(source MaterialSource, draft *DraftVersionEvidence, working *WorkingProjectionEvidence, hash string) bool {
	if source.DraftVersion != nil {
		return sameDraftVersion(source.DraftVersion, draft)
	}
	if source.WorkingProjection != nil {
		return matchesWorkingProjection(source.WorkingProjection, working)
	}
	return source.PayloadHash != "" && source.PayloadHash == hash
}

func appendSourceRequest(m *MaterialReference, source MaterialSource) error {
	for i, prior := range m.Sources {
		if prior.ReferenceID != source.ReferenceID {
			continue
		}
		if !source.NotAfter.IsZero() && (prior.NotAfter.IsZero() || source.NotAfter.Before(prior.NotAfter)) {
			m.Sources[i].NotAfter = source.NotAfter
		}
		if prior.DraftVersion != nil || source.DraftVersion != nil {
			if !sameDraftVersion(prior.DraftVersion, source.DraftVersion) {
				return ErrInvalidInventory
			}
			return nil
		}
		if prior.WorkingProjection != nil && source.WorkingProjection != nil {
			left, right := *prior.WorkingProjection, *source.WorkingProjection
			if left.State == "" {
				left.State = right.State
			}
			if right.State == "" {
				right.State = left.State
			}
			if !matchesWorkingProjection(&left, &right) {
				return ErrInvalidInventory
			}
			m.Sources[i].WorkingProjection = &left
			return nil
		}
		if prior.PayloadHash != "" && source.PayloadHash != "" && prior.PayloadHash != source.PayloadHash {
			return ErrInvalidInventory
		}
		if prior.WorkingProjection == nil && source.WorkingProjection != nil {
			m.Sources[i] = source
		}
		return nil
	}
	if len(m.Sources) >= 4096 {
		return ErrInvalidInventory
	}
	m.Sources = append(m.Sources, source)
	return nil
}
