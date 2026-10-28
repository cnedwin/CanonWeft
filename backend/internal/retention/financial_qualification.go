package retention

import "slices"

// QualificationAccountingSummary is a balance snapshot, never authorization
// to dispatch a new call. Historical frozen grants remain separate because
// editing a schedule does not rewrite the authority captured by an older run.
type QualificationAccountingSummary struct {
	RunID          string                `json:"run_id"`
	StepID         string                `json:"step_id"`
	Origin         string                `json:"origin"`
	State          string                `json:"state"`
	ReservedMicros int64                 `json:"reserved_micros"`
	HeldMicros     int64                 `json:"held_micros"`
	ChargedMicros  int64                 `json:"charged_micros"`
	Weekly         *WeeklyBalanceSummary `json:"weekly,omitempty"`
}

type WeeklyBalanceSummary struct {
	ID             string `json:"id"`
	ScheduleID     string `json:"schedule_id"`
	Revision       int64  `json:"revision"`
	Week           string `json:"week"`
	Currency       string `json:"currency"`
	CapMicros      int64  `json:"cap_micros"`
	ReservedMicros int64  `json:"reserved_micros"`
	SpentMicros    int64  `json:"spent_micros"`
}

func validateQualificationAccounting(summary EvidenceSummary) error {
	q := summary.QualificationAccounting
	if q == nil {
		return nil
	}
	if !id32.MatchString(summary.CallID) || !id32.MatchString(q.RunID) || !id32.MatchString(q.StepID) || !currencyCode.MatchString(summary.Currency) || !slices.Contains([]string{"NATIVE_29", "NATIVE_30", "LEGACY_28_DB", "LEGACY_28_ARCHIVE"}, q.Origin) {
		return ErrInvalidInventory
	}
	expectedState := summary.State
	if q.State != expectedState || q.ReservedMicros != summary.ReservedMicros || q.ChargedMicros != summary.ChargedMicros || q.ReservedMicros < 0 || q.ReservedMicros > 1000000000000 || q.ChargedMicros < 0 || q.ChargedMicros > 1000000000000 {
		return ErrInvalidInventory
	}
	expectedHeld := int64(0)
	if slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN"}, summary.State) {
		expectedHeld = summary.ReservedMicros
	}
	if q.HeldMicros != expectedHeld {
		return ErrInvalidInventory
	}
	if w := q.Weekly; w != nil {
		if !id32.MatchString(w.ID) || !id32.MatchString(w.ScheduleID) || w.Revision < 1 || !weekKey.MatchString(w.Week) || w.Currency != summary.Currency || w.CapMicros < 0 || w.CapMicros > 1000000000000 || w.ReservedMicros < q.HeldMicros || w.SpentMicros < q.ChargedMicros {
			return ErrInvalidInventory
		}
	}
	return nil
}

func sourceFinancialAmount(row SourceRow, column string) (int64, error) {
	if row.Nulls[column] {
		return 0, ErrInvalidInventory
	}
	return financialInteger(row.Values[column])
}

func (index *SourceIndex) qualificationAccounting(summary *EvidenceSummary, owner SourceOwner) error {
	key, err := RowOwnerKey("qualification_calls", map[string]string{"call_id": summary.CallID})
	if err != nil {
		return err
	}
	link, exists := index.rows["qualification_calls"][key]
	if !exists {
		return nil
	}
	linkOwner, err := index.Owner("qualification_calls", link.Values)
	if err != nil || linkOwner != owner {
		return ErrSourceOwnership
	}
	step, err := index.sourceRow("qualification_steps", map[string]string{"id": link.Values["step_id"]})
	if err != nil || step.Values["run_id"] != link.Values["run_id"] {
		return ErrSourceOwnership
	}
	stepOwner, err := index.Owner("qualification_steps", step.Values)
	if err != nil || stepOwner != owner {
		return ErrSourceOwnership
	}
	q := &QualificationAccountingSummary{RunID: link.Values["run_id"], StepID: link.Values["step_id"], Origin: link.Values["origin"], State: link.Values["state"]}
	for column, target := range map[string]*int64{"reserved_micros": &q.ReservedMicros, "held_micros": &q.HeldMicros, "charged_micros": &q.ChargedMicros} {
		*target, err = sourceFinancialAmount(link, column)
		if err != nil {
			return err
		}
	}
	if grantID := link.Values["grant_id"]; grantID != "" {
		grant, err := index.sourceRow("qualification_week_grants", map[string]string{"id": grantID})
		if err != nil {
			return err
		}
		grantOwner, err := index.Owner("qualification_week_grants", grant.Values)
		if err != nil || grantOwner != owner {
			return ErrSourceOwnership
		}
		scheduleOwner, err := index.Owner("qualification_schedules", map[string]string{"id": grant.Values["schedule_id"]})
		if err != nil || scheduleOwner != owner {
			return ErrSourceOwnership
		}
		w := &WeeklyBalanceSummary{ID: grantID, ScheduleID: grant.Values["schedule_id"], Week: grant.Values["week_key"], Currency: grant.Values["currency"]}
		for column, target := range map[string]*int64{"schedule_revision": &w.Revision, "cap_micros": &w.CapMicros, "reserved_micros": &w.ReservedMicros, "spent_micros": &w.SpentMicros} {
			*target, err = sourceFinancialAmount(grant, column)
			if err != nil {
				return err
			}
		}
		q.Weekly = w
	}
	summary.QualificationAccounting = q
	return validateQualificationAccounting(*summary)
}
