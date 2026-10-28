package retention

import "encoding/json"

type EventExpirySummary struct {
	SubjectID string `json:"subject_id"`
}

func eventSubject(row SourceRow) (string, error) {
	var payload map[string]json.RawMessage
	if authorDocument(row.Values["payload"], &payload) != nil || payload == nil {
		return "", ErrInvalidInventory
	}
	field := ""
	if row.Values["kind"] == "qualification.schedule" {
		field = "schedule_id"
	}
	if row.Values["kind"] == "qualification.receipt" {
		field = "run_id"
	}
	if field == "" {
		if !id32.MatchString(row.Values["resource_id"]) {
			return "", ErrInvalidInventory
		}
		return row.Values["resource_id"], nil
	}
	var id string
	if len(payload) != 1 || json.Unmarshal(payload[field], &id) != nil || !id32.MatchString(id) {
		return "", ErrInvalidInventory
	}
	return id, nil
}

// Resolves the structural subject before the independent journal is bound.
// This parsing step alone never authorizes expiry or executable replay.
func eventOwnershipSubject(row SourceRow) (string, error) {
	if proof, err := ParseSourceProjection([]byte(row.Values["payload"]), "outbox.payload"); err == nil {
		if validateTransientOwner(row, "payload", proof) != nil || proof.Summary.Event == nil {
			return "", ErrSourceOwnership
		}
		return proof.Summary.Event.SubjectID, nil
	}
	return eventSubject(row)
}
