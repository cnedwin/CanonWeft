package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"novelstudio/internal/binlog"
)

type logRollover struct {
	Format     int    `json:"format"`
	Source     Source `json:"source"`
	PreviousID string `json:"previous_id"`
}

func (s *LogStore) beginRollover(ctx context.Context, id string) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	if !hex64.MatchString(id) {
		return errors.New("invalid rollover owner")
	}
	if _, _, err := s.baseline(id); err != nil {
		return err
	}
	raw, err := json.Marshal(logRollover{Format: 1, Source: s.store.Source, PreviousID: id})
	if err != nil {
		return err
	}
	return s.publish(ctx, id+".rollover", raw, 4096)
}
func (s *LogStore) pendingRollovers(ctx context.Context) ([]string, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return nil, err
	}
	entries, err := s.store.entries()
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".rollover") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".rollover")
		if !hex64.MatchString(id) {
			continue
		}
		raw, e := s.store.readRegular(entry.Name(), 4096)
		if e != nil {
			return nil, e
		}
		var record logRollover
		if strictLogJSON(raw, &record) != nil || record.Format != 1 || record.Source != s.store.Source || record.PreviousID != id {
			return nil, errors.New("invalid rollover intent")
		}
		result = append(result, id)
	}
	return result, nil
}
func (s *LogStore) finishRollover(ctx context.Context, id string) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	if !hex64.MatchString(id) {
		return errors.New("invalid rollover owner")
	}
	return s.removeRegular(id + ".rollover")
}

// Recover the old chain before discarding a durable rollover intent. Even a
// crash after the new baseline's publication cannot make missing overlap look
// like an unbroken history. Failed catch-up becomes a persistent visible gap.
func (r *LogRunner) recoverRollovers(ctx context.Context) error {
	ids, err := r.Store.pendingRollovers(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		point, catchupErr := r.collect(ctx, id)
		if catchupErr == nil && r.active.ID != "" && r.active.ID != id {
			if order, e := binlog.ComparePositions(point.Position, r.active.Position); e != nil || order < 0 {
				catchupErr = errors.New("rollover coverage does not reach active baseline")
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if catchupErr != nil {
			if err = r.Store.MarkGap(ctx, id, time.Now().UTC(), "ROLLOVER_GAP"); err != nil {
				return err
			}
		} else if r.active.ID == id {
			r.point = point
		}
		if err = r.Store.finishRollover(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
