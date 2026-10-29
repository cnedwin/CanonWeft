package backup

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
)

type LogRecoveryView struct {
	Failure          *binlog.ReadFailureDetails `json:"failure,omitempty"`
	State            string                     `json:"state"`
	CheckedAt        time.Time                  `json:"checked_at"`
	CoverageFrom     time.Time                  `json:"coverage_from"`
	Latest           LogPoint                   `json:"latest"`
	BaselineID       string                     `json:"baseline_id"`
	Running          bool                       `json:"running"`
	HistoryGaps      int                        `json:"history_gaps"`
	CleanupState     string                     `json:"cleanup_state"`
	RetentionDays    int                        `json:"retention_days"`
	IndependentMedia string                     `json:"independent_media"`
}

type LogRunner struct {
	Store         *LogStore
	db            *sql.DB
	blobs         storage.BlobStore
	reader        *binlog.Reader
	mu            sync.Mutex
	stateMu       sync.RWMutex
	view          LogRecoveryView
	active        LogBaselineRecord
	point         LogPoint
	initialized   bool
	auditedDay    string
	prunedDay     string
	needsBaseline bool
}

func NewLogRunner(ctx context.Context, db *sql.DB, blobs storage.BlobStore, dsn, path string) (*LogRunner, error) {
	if db == nil || blobs == nil {
		return nil, errors.New("log source required")
	}
	source, err := sourceIdentity(ctx, db)
	if err != nil {
		return nil, errors.New("log source identity unavailable")
	}
	reader, err := binlog.NewReader(dsn, source.ServerUUID)
	if err != nil {
		return nil, err
	}
	store, err := OpenLogStore(path, source)
	if err != nil {
		return nil, err
	}
	return &LogRunner{Store: store, db: db, blobs: blobs, reader: reader, view: LogRecoveryView{State: "STARTING", RetentionDays: 35, IndependentMedia: "NOT_QUALIFIED"}}, nil
}

func logCaptureAction(baseline, point, now time.Time, force bool) string {
	if now.IsZero() || baseline.After(now.Add(time.Minute)) || point.After(now.Add(time.Minute)) {
		return "clock_error"
	}
	if baseline.IsZero() {
		return "baseline"
	}
	if baseline.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return "rollover"
	}
	if force || point.IsZero() || now.Sub(point) >= 10*time.Minute {
		return "collect"
	}
	return ""
}

func (r *LogRunner) Close() error { r.mu.Lock(); defer r.mu.Unlock(); return r.Store.Close() }
func (r *LogRunner) View(now time.Time) LogRecoveryView {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	v := r.view
	if v.State == "CURRENT" || v.State == "CURRENT_WITH_GAPS" {
		if now.Sub(v.Latest.ObservedAt) > ProtectionMaxAge {
			v.State = "STALE"
		} else if v.Latest.ObservedAt.After(now.Add(time.Minute)) {
			v.State = "CLOCK_ERROR"
		}
	}
	return v
}

func (r *LogRunner) Tick(ctx context.Context, now time.Time) (LogRecoveryView, error) {
	return r.tick(ctx, now, false)
}

// Collect is also the explicit "back up now" action; it does not wait for the
// normal ten-minute cadence or change the source's observed timestamp.
func (r *LogRunner) Collect(ctx context.Context) (LogRecoveryView, error) {
	return r.tick(ctx, time.Now().UTC(), true)
}

func (r *LogRunner) tick(ctx context.Context, now time.Time, force bool) (result LogRecoveryView, resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stateMu.Lock()
	result = r.view
	result.Running = true
	r.view = result
	r.stateMu.Unlock()
	result.CheckedAt = now.UTC()
	result.State = "ERROR"
	result.Failure = nil
	defer func() {
		if details, ok := binlog.ReadFailureInfo(resultErr); ok {
			result.Failure = &details
		}
		result.Running = false
		result.BaselineID = r.active.ID
		result.CoverageFrom = r.active.ObservedAt
		result.Latest = r.point
		r.stateMu.Lock()
		r.view = result
		r.stateMu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if now.IsZero() {
		result.State = "CLOCK_ERROR"
		return result, errors.New("source observation time required")
	}
	var sourceSchema int
	if err := r.db.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&sourceSchema); err != nil {
		return result, errors.New("log source schema unavailable")
	}
	if _, err := snapshotCatalog(sourceSchema); err != nil {
		return result, err
	}
	day := now.UTC().Format("2006-01-02")
	if day != r.prunedDay {
		if err := r.Store.Prune(ctx, now); err != nil {
			result.CleanupState = "NEEDS_ATTENTION"
		} else {
			result.CleanupState = "CURRENT"
			r.prunedDay = day
		}
	}
	if !r.initialized || r.auditedDay != day || r.active.Schema != sourceSchema {
		records, err := r.Store.Catalog(ctx)
		if err != nil {
			return result, err
		}
		var active LogBaselineRecord
		var previousSchema LogBaselineRecord
		var latest LogPoint
		needsBaseline := false
		for _, record := range records {
			if record.State != "BASELINE" {
				if err := r.Store.MarkGap(ctx, record.ID, now, "INVALID_HISTORY"); err != nil {
					return result, err
				}
			}
			_, schemaErr := logSchemaVersion(record.Schema)
			if record.State == "BASELINE" && schemaErr == nil && validLogAge(record.ObservedAt, now) {
				selected := record.Schema == sourceSchema && active.ID == ""
				if record.Schema != sourceSchema && previousSchema.ID == "" {
					previousSchema = record
				}
				if selected {
					active = record
				}
				replay, loadErr := r.Store.Load(ctx, record.ID, now)
				if loadErr != nil {
					if ctx.Err() != nil {
						return result, ctx.Err()
					}
					if e := r.Store.MarkGap(ctx, record.ID, now, "INVALID_HISTORY"); e != nil {
						return result, e
					}
					if selected {
						needsBaseline = true
					}
				} else if selected {
					latest = replay.Point()
				}
			}
		}
		if active.ID == "" && previousSchema.ID != "" {
			// Historical points remain readable, but no source reader can prove
			// continuity across a DDL boundary with the old row contract.
			if err := r.Store.MarkGap(ctx, previousSchema.ID, now, "SCHEMA_CHANGE"); err != nil {
				return result, err
			}
		}
		r.active = active
		r.point = latest
		r.needsBaseline = needsBaseline
		r.initialized = true
		r.auditedDay = day
	}
	if err := r.recoverRollovers(ctx); err != nil {
		return result, err
	}
	action := logCaptureAction(r.active.ObservedAt, r.point.ObservedAt, now, force)
	if r.needsBaseline {
		action = "baseline"
	}
	if action == "clock_error" {
		result.State = "CLOCK_ERROR"
		return result, errors.New("log recovery clock mismatch")
	}
	if action == "baseline" || action == "rollover" {
		previous := r.active
		if action == "rollover" && previous.ID != "" {
			if err := r.Store.beginRollover(ctx, previous.ID); err != nil {
				return result, err
			}
		}
		fresh, err := r.Store.CaptureBaseline(ctx, r.db, r.blobs)
		if err != nil {
			result.State = "BASELINE_FAILED"
			return result, err
		}
		// Capture the new baseline first, then extend the old chain beyond it.
		// This overlaps coverage instead of leaving a rollover-time hole.
		if action == "rollover" && previous.ID != "" {
			var through LogPoint
			through, err = r.collect(ctx, previous.ID)
			if err == nil {
				if order, e := binlog.ComparePositions(through.Position, fresh.Position); e != nil || order < 0 {
					err = errors.New("rollover coverage does not reach new baseline")
				}
			}
			if err != nil {
				if e := r.Store.MarkGap(ctx, previous.ID, time.Now().UTC(), "ROLLOVER_GAP"); e != nil {
					return result, e
				}
			}
			if e := r.Store.finishRollover(ctx, previous.ID); e != nil {
				return result, e
			}
		}
		replay, err := r.Store.Load(ctx, fresh.ID, time.Now().UTC())
		if err != nil {
			return result, err
		}
		r.active = fresh
		r.point = replay.Point()
		r.needsBaseline = false
	} else if action == "collect" {
		point, err := r.collect(ctx, r.active.ID)
		if err != nil {
			result.State = "CAPTURE_FAILED"
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if e := r.Store.MarkGap(ctx, r.active.ID, time.Now().UTC(), "CAPTURE_GAP"); e != nil {
				return result, e
			}
			r.needsBaseline = true
			return result, err
		}
		r.point = point
	}
	gaps, err := r.Store.GapCount(ctx)
	if err != nil {
		return result, err
	}
	result.HistoryGaps = gaps
	result.State = "CURRENT"
	if gaps > 0 {
		result.State = "CURRENT_WITH_GAPS"
	}
	if time.Since(r.point.ObservedAt) > ProtectionMaxAge {
		result.State = "STALE"
	}
	return result, nil
}
func (r *LogRunner) collect(ctx context.Context, id string) (LogPoint, error) {
	ctx, cancel := context.WithTimeout(ctx, binlog.ReadTimeout)
	defer cancel()
	replay, err := r.Store.Load(ctx, id, time.Now().UTC())
	if err != nil {
		return LogPoint{}, err
	}
	return replay.CaptureSegments(ctx, r.db, r.reader, r.blobs, func(ctx context.Context, raw []byte) (LogPoint, error) {
		return r.Store.Append(ctx, id, raw, time.Now().UTC())
	})
}
func (r *LogRunner) Run(ctx context.Context, report func(LogRecoveryView, error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 4*time.Minute)
		view, err := r.Tick(attempt, time.Now().UTC())
		cancel()
		if report != nil {
			report(view, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
