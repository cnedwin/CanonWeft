package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type ProtectionView struct {
	CheckpointState  string           `json:"checkpoint_state"`
	AttemptState     string           `json:"attempt_state"`
	CheckedAt        time.Time        `json:"checked_at"`
	Latest           ProtectionRecord `json:"latest"`
	Running          bool             `json:"running"`
	RetentionDays    int              `json:"retention_days"`
	LogRecovery      string           `json:"log_recovery"`
	IndependentMedia string           `json:"independent_media"`
	LogCoverage      *LogRecoveryView `json:"log_coverage,omitempty"`
}

// View never substitutes recent scheduler activity for the database snapshot
// timestamp. Its log/media labels deliberately remain unqualified.
func ProtectionViewFromTick(result ProtectionTick, now time.Time) ProtectionView {
	view := ProtectionView{CheckpointState: "MISSING", AttemptState: result.State, CheckedAt: result.CheckedAt, Latest: result.Record, RetentionDays: 35, LogRecovery: "NOT_QUALIFIED", IndependentMedia: "NOT_QUALIFIED"}
	if view.AttemptState == "" {
		view.AttemptState = "STARTING"
	}
	if result.Record.ID != "" && !result.Record.SnapshotAt.IsZero() {
		view.CheckpointState = "CURRENT"
		age := now.Sub(result.Record.SnapshotAt)
		if age < -time.Minute {
			view.CheckpointState = "CLOCK_ERROR"
		} else if age > ProtectionMaxAge {
			view.CheckpointState = "STALE"
		}
	}
	if result.State == "CORRUPT" || result.CheckpointState == "CORRUPT" {
		view.CheckpointState = "CORRUPT"
	}
	return view
}

func (r *ProtectionRunner) View(now time.Time) ProtectionView {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	view := ProtectionViewFromTick(r.last, now)
	view.Running = r.running
	return view
}

func (s *ProtectionStore) WriteHealth(result ProtectionTick) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || result.CheckedAt.IsZero() {
		return errors.New("protection health unavailable")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	name := hex.EncodeToString(id) + ".health.tmp"
	if err := s.writeNew(name, strings.NewReader(string(raw))); err != nil {
		return err
	}
	defer s.root.Remove(name)
	return s.root.Rename(name, "health.json")
}
