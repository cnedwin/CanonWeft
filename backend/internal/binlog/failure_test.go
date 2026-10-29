package binlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReadFailureRetainsSafeLocationAndCancellationWithoutPayload(t *testing.T) {
	for _, cause := range []error{errors.New("private manuscript secret-key"), fmt.Errorf("private query: %w", context.Canceled), fmt.Errorf("private driver data: %w", context.DeadlineExceeded)} {
		err := readFailure("TRANSACTION_REJECTED", "consume", Position{"binlog.000025", 92988644}, 2, cause)
		info, ok := ReadFailureInfo(err)
		if !ok || info.Position.Offset != 92988644 || info.Stage != "consume" || info.EventType != 2 {
			t.Fatal("diagnostic location lost")
		}
		raw, _ := json.Marshal(info)
		for _, out := range []string{err.Error(), fmt.Sprintf("%+v", err), string(raw)} {
			if strings.Contains(out, "private") || strings.Contains(out, "secret-key") {
				t.Fatal("raw cause leaked")
			}
		}
		for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(cause, sentinel) != errors.Is(err, sentinel) {
				t.Fatal("cancellation identity lost")
			}
		}
	}
	if _, ok := ReadFailureInfo(errors.New("unknown private body")); ok {
		t.Fatal("unknown errors must not supply diagnostic content")
	}
}

func TestReadFailureDiscardsUnverifiedSourcePosition(t *testing.T) {
	err := readFailure("DECODE_EVENT_REJECTED", "decode", Position{"private\nquery", 1}, 2, errors.New("sensitive"))
	info, _ := ReadFailureInfo(err)
	if info.Position != (Position{}) || strings.Contains(err.Error(), "private") {
		t.Fatal("unvalidated coordinate leaked")
	}
}
