package binlog

import (
	"context"
	"errors"
	"fmt"
)

// ReadFailureDetails contains only verified coordinates and locally assigned
// codes. Driver/parser errors may include SQL or raw row bytes and are never
// formatted into public diagnostics.
type ReadFailureDetails struct {
	Code      string   `json:"code"`
	Stage     string   `json:"stage"`
	Position  Position `json:"position"`
	EventType uint8    `json:"event_type,omitempty"`
}

type sourceReadFailure struct {
	details ReadFailureDetails
	cause   error
}

func (e *sourceReadFailure) Error() string {
	return fmt.Sprintf("source log %s stage=%s position=%s:%d event=%d", e.details.Code, e.details.Stage, e.details.Position.File, e.details.Position.Offset, e.details.EventType)
}
func (e *sourceReadFailure) Unwrap() error { return e.cause }

func readFailure(code, stage string, point Position, kind uint8, cause error) error {
	if cause == nil {
		return nil
	}
	if !validPosition(point) {
		point = Position{}
	}
	if errors.Is(cause, context.Canceled) {
		code = "READ_CANCELLED"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		code = "READ_DEADLINE_EXCEEDED"
	}
	return &sourceReadFailure{ReadFailureDetails{Code: code, Stage: stage, Position: point, EventType: kind}, cause}
}

func ReadFailureInfo(err error) (ReadFailureDetails, bool) {
	var failure *sourceReadFailure
	if !errors.As(err, &failure) {
		return ReadFailureDetails{}, false
	}
	return failure.details, true
}
