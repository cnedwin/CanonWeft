package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"
)

type logGap struct {
	Format     int       `json:"format"`
	Source     Source    `json:"source"`
	BaselineID string    `json:"baseline_id"`
	ObservedAt time.Time `json:"observed_at"`
	Reason     string    `json:"reason"`
}

func (s *LogStore) readGap(id string) (logGap, error) {
	var gap logGap
	if !hex64.MatchString(id) {
		return gap, errors.New("invalid gap owner")
	}
	raw, err := s.store.readRegular(id+".gap", 4096)
	if err != nil {
		return gap, err
	}
	if strictLogJSON(raw, &gap) != nil || gap.Format != 1 || gap.Source != s.store.Source || gap.BaselineID != id || gap.ObservedAt.IsZero() || !slices.Contains([]string{"INVALID_HISTORY", "CAPTURE_GAP", "ROLLOVER_GAP", "SCHEMA_CHANGE"}, gap.Reason) {
		return gap, errors.New("invalid log recovery gap record")
	}
	return gap, nil
}
func (s *LogStore) MarkGap(ctx context.Context, id string, at time.Time, reason string) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	if !hex64.MatchString(id) || at.IsZero() || !slices.Contains([]string{"INVALID_HISTORY", "CAPTURE_GAP", "ROLLOVER_GAP", "SCHEMA_CHANGE"}, reason) {
		return errors.New("invalid recovery gap")
	}
	if _, err := s.readGap(id); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := json.Marshal(logGap{Format: 1, Source: s.store.Source, BaselineID: id, ObservedAt: at.UTC(), Reason: reason})
	if err != nil {
		return err
	}
	return s.publish(ctx, id+".gap", raw, 4096)
}
func (s *LogStore) GapCount(ctx context.Context) (int, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return 0, err
	}
	entries, err := s.store.entries()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		if !strings.HasSuffix(entry.Name(), ".gap") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".gap")
		if !hex64.MatchString(id) {
			continue
		}
		if _, err = s.readGap(id); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
