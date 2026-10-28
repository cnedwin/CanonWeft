package retention

import (
	"slices"
	"strings"
	"time"
)

// AccountingMetadata is a closed, body-free projection of existing authority.
// It is never a settlement, grant, dispatch permission or second ledger.
type AccountingMetadata struct {
	Schema  int                   `json:"schema"`
	Summary *EvidenceSummary      `json:"summary,omitempty"`
	Weekly  *WeeklyBalanceSummary `json:"weekly,omitempty"`
	Audit   *AuditMetadata        `json:"audit,omitempty"`
}

type AuditMetadata struct {
	ID         string `json:"id"`
	ActorID    string `json:"actor_id"`
	Action     string `json:"action"`
	ResourceID string `json:"resource_id"`
	// Source wall time is retained verbatim; legacy values are not asserted UTC.
	SourceWallTime string `json:"source_wall_time"`
}

func accountingField(table string) string {
	switch table {
	case "audit_events", "model_call_accounting", "model_call_receipts":
		return "metadata"
	case "model_calls", "model_reconciliations", "qualification_calls", "qualification_week_grants":
		return "accounting"
	default:
		return ""
	}
}

func (m AccountingMetadata) open() bool {
	if m.Summary != nil {
		return slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN"}, m.Summary.State)
	}
	return m.Weekly != nil && m.Weekly.ReservedMicros > 0
}

func validWeeklyBalance(w WeeklyBalanceSummary) bool {
	return id32.MatchString(w.ID) && id32.MatchString(w.ScheduleID) && w.Revision > 0 && weekKey.MatchString(w.Week) && currencyCode.MatchString(w.Currency) && w.CapMicros >= 0 && w.CapMicros <= 1000000000000 && w.ReservedMicros >= 0 && w.SpentMicros >= 0
}

func validAccountingMetadata(m AccountingMetadata, table, owner string) bool {
	if m.Schema != 1 || accountingField(table) == "" {
		return false
	}
	var primary map[string]string
	switch table {
	case "audit_events":
		if m.Audit == nil || m.Summary != nil || m.Weekly != nil {
			return false
		}
		a := m.Audit
		n, err := financialInteger(a.ID)
		at, clockErr := time.Parse("2006-01-02 15:04:05.999999", a.SourceWallTime)
		if err != nil || n < 1 || !id32.MatchString(a.ActorID) || !id32.MatchString(a.ResourceID) || auditOwnerTable(a.Action) == "" || clockErr != nil || at.IsZero() || a.SourceWallTime != at.Format("2006-01-02 15:04:05.000000") {
			return false
		}
		primary = map[string]string{"id": a.ID}
	case "qualification_week_grants":
		if m.Weekly == nil || m.Summary != nil || m.Audit != nil || !validWeeklyBalance(*m.Weekly) {
			return false
		}
		primary = map[string]string{"id": m.Weekly.ID}
	default:
		if m.Summary == nil || m.Weekly != nil || m.Audit != nil {
			return false
		}
		s := *m.Summary
		if ValidateSummary(s, Inline) != nil || !id32.MatchString(s.CallID) || !currencyCode.MatchString(s.Currency) || !hash64.MatchString(s.InputHash) || !slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN", "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED"}, s.State) || s.ReservedMicros > 1000000000000 || s.ChargedMicros > 1000000000000 {
			return false
		}
		// Other evidence vocabularies cannot be smuggled into an accounting row.
		minimal := EvidenceSummary{CallID: s.CallID, Currency: s.Currency, State: s.State, InputHash: s.InputHash, OutputHash: s.OutputHash, ReservedMicros: s.ReservedMicros, ChargedMicros: s.ChargedMicros, AccountingRevision: s.AccountingRevision, Receipt: s.Receipt, Reconciliation: s.Reconciliation, QualificationAccounting: s.QualificationAccounting}
		if stableHash(s) != stableHash(minimal) {
			return false
		}
		if q := s.QualificationAccounting; q != nil && q.Weekly != nil && !validWeeklyBalance(*q.Weekly) {
			return false
		}
		switch table {
		case "model_calls":
			primary = map[string]string{"id": s.CallID}
		case "model_call_accounting", "qualification_calls":
			primary = map[string]string{"call_id": s.CallID}
		case "model_call_receipts":
			if s.Receipt == nil {
				return false
			}
			primary = map[string]string{"id": s.Receipt.ID}
		case "model_reconciliations":
			if s.Reconciliation == nil || s.Reconciliation.Revision > s.AccountingRevision || s.Reconciliation.Currency != s.Currency {
				return false
			}
			primary = map[string]string{"id": s.Reconciliation.ID}
		}
		if (table != "model_call_receipts" && s.Receipt != nil) || (table != "model_reconciliations" && s.Reconciliation != nil) || (table == "qualification_calls" && s.QualificationAccounting == nil) {
			return false
		}
	}
	key, err := RowOwnerKey(table, primary)
	return err == nil && key == owner
}

func (index *SourceIndex) accountingMetadata(row SourceRow) (AccountingMetadata, error) {
	m := AccountingMetadata{Schema: 1}
	switch row.Table {
	case "audit_events":
		m.Audit = &AuditMetadata{ID: row.Values["id"], ActorID: row.Values["actor_id"], Action: row.Values["action"], ResourceID: row.Values["resource_id"], SourceWallTime: row.Values["created_at"]}
		// database/sql can expose DATETIME as an RFC3339 time in the driver's
		// configured location. Preserve its wall components, never convert zones.
		at, err := time.Parse(time.RFC3339Nano, m.Audit.SourceWallTime)
		if err != nil {
			at, err = time.Parse("2006-01-02 15:04:05.999999", m.Audit.SourceWallTime)
		}
		if err != nil {
			return AccountingMetadata{}, ErrInvalidInventory
		}
		m.Audit.SourceWallTime = at.Format("2006-01-02 15:04:05.000000")
	case "qualification_week_grants":
		w := &WeeklyBalanceSummary{ID: row.Values["id"], ScheduleID: row.Values["schedule_id"], Week: row.Values["week_key"], Currency: row.Values["currency"]}
		for field, target := range map[string]*int64{"schedule_revision": &w.Revision, "cap_micros": &w.CapMicros, "reserved_micros": &w.ReservedMicros, "spent_micros": &w.SpentMicros} {
			n, err := sourceFinancialAmount(row, field)
			if err != nil {
				return AccountingMetadata{}, err
			}
			*target = n
		}
		m.Weekly = w
	default:
		summary, err := index.FinancialSummary(row.Table, row.Values)
		if err != nil {
			return AccountingMetadata{}, err
		}
		m.Summary = &summary
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil || !validAccountingMetadata(m, row.Table, key) {
		return AccountingMetadata{}, ErrInvalidInventory
	}
	return m, nil
}

// AccountingReferences collects only the seven explicit virtual fields. Their
// observation digest covers the canonical minimal projection, not source prose.
func (index *SourceIndex) AccountingReferences(project string) ([]MaterialReference, error) {
	if index == nil || !id32.MatchString(project) {
		return nil, ErrSourceOwnership
	}
	var result []MaterialReference
	for _, rule := range referenceRules {
		if rule.Storage != Metadata {
			continue
		}
		parts := strings.SplitN(rule.Key, ".", 2)
		if len(parts) != 2 || accountingField(parts[0]) != parts[1] {
			return nil, ErrInvalidInventory
		}
		for key, row := range index.rows[parts[0]] {
			owner, err := index.Owner(row.Table, row.Values)
			if err != nil {
				return nil, err
			}
			if owner.Shared || owner.ProjectID != project {
				continue
			}
			ref, err := index.accountingReference(project, row)
			if err != nil {
				return nil, err
			}
			if ref.OwnerKey != key {
				return nil, ErrInvalidInventory
			}
			result = append(result, ref)
		}
	}
	slices.SortFunc(result, func(a, b MaterialReference) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func (index *SourceIndex) accountingReference(project string, row SourceRow) (MaterialReference, error) {
	metadata, err := index.accountingMetadata(row)
	if err != nil {
		return MaterialReference{}, err
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return MaterialReference{}, err
	}
	field := accountingField(row.Table)
	id, err := ReferenceID(project, row.Table, key, field)
	if err != nil {
		return MaterialReference{}, err
	}
	hash := stableHash(metadata)
	return MaterialReference{ID: id, Table: row.Table, OwnerKey: key, Field: field, PayloadHash: hash, SourceID: id, SourcePayloadHash: hash, Storage: Metadata, Class: Audit, Accounting: &metadata}, nil
}
