package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"novelstudio/internal/storage"
)

type ProtectionRunner struct {
	Store   *ProtectionStore
	db      *sql.DB
	blobs   storage.BlobStore
	mu      sync.Mutex
	stateMu sync.RWMutex
	last    ProtectionTick
	running bool
}

type ProtectionTick struct {
	CheckedAt       time.Time        `json:"checked_at"`
	State           string           `json:"state"`
	CheckpointState string           `json:"checkpoint_state"`
	Captured        bool             `json:"captured"`
	Record          ProtectionRecord `json:"record"`
	Expired         int              `json:"expired"`
	Cleaned         int              `json:"cleaned"`
}

type captureIntent struct {
	Format    int       `json:"format"`
	ID        string    `json:"id"`
	Source    Source    `json:"source"`
	StartedAt time.Time `json:"started_at"`
}

func NewProtectionRunner(ctx context.Context, db *sql.DB, blobs storage.BlobStore, path string) (*ProtectionRunner, error) {
	if db == nil || blobs == nil {
		return nil, errors.New("protection source required")
	}
	source, err := sourceIdentity(ctx, db)
	if err != nil {
		return nil, err
	}
	store, err := OpenProtection(path, source)
	if err != nil {
		return nil, err
	}
	return &ProtectionRunner{Store: store, db: db, blobs: blobs}, nil
}

func (r *ProtectionRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Store.Close()
}

func captureKind(status ProtectionStatus, now time.Time) string {
	if len(status.Records) == 0 || status.State == "CORRUPT" {
		return "baseline"
	}
	today := now.UTC().Format("2006-01-02")
	baseline := false
	for _, record := range status.Records {
		if record.Kind == "baseline" && record.SnapshotAt.UTC().Format("2006-01-02") == today {
			baseline = true
			break
		}
	}
	if !baseline {
		return "baseline"
	}
	if now.Sub(status.Records[0].SnapshotAt) >= 10*time.Minute {
		return "checkpoint"
	}
	return ""
}

// Tick does real database/object snapshots. Its ten-minute cadence is an extra
// fallback; it does not establish the separate binary-log recovery objective.
func (r *ProtectionRunner) Tick(ctx context.Context, now time.Time) (result ProtectionTick, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result = ProtectionTick{CheckedAt: now.UTC(), State: "ERROR"}
	r.stateMu.Lock()
	r.running = true
	r.stateMu.Unlock()
	defer func() {
		if writeErr := r.Store.WriteHealth(result); writeErr != nil {
			err = errors.Join(err, writeErr)
			result.State = "HEALTH_WRITE_FAILED"
		}
		r.stateMu.Lock()
		r.last, r.running = result, false
		r.stateMu.Unlock()
	}()
	if result.Expired, err = r.Store.Prune(ctx, now); err != nil {
		return result, err
	}
	if result.Cleaned, err = r.Store.CleanupInterrupted(ctx, now); err != nil {
		return result, err
	}
	status, statusErr := r.Store.Status(ctx, now)
	if statusErr != nil && status.State != "CORRUPT" {
		return result, statusErr
	}
	if status.State == "CLOCK_ERROR" {
		return result, errors.New("protection clock moved backwards")
	}
	result.State = status.State
	result.CheckpointState = status.State
	if len(status.Records) > 0 {
		result.Record = status.Records[0]
	}
	kind := captureKind(status, now)
	if kind == "" {
		return result, nil
	}
	record, err := r.capture(ctx, now, kind)
	if err != nil {
		result.State = "CAPTURE_FAILED"
		return result, err
	}
	result.Captured = true
	result.Record = record
	result.State = "CURRENT"
	result.CheckpointState = "CURRENT"
	if time.Since(record.SnapshotAt) > ProtectionMaxAge {
		result.State = "STALE"
		result.CheckpointState = "STALE"
	}
	return result, nil
}

func (r *ProtectionRunner) capture(ctx context.Context, now time.Time, kind string) (result ProtectionRecord, resultErr error) {
	s := r.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return ProtectionRecord{}, errors.New("protection repository closed")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return ProtectionRecord{}, err
	}
	id := hex.EncodeToString(idBytes)
	name, intentName := id+".capture", id+".capture.intent"
	raw, err := json.Marshal(captureIntent{Format: 1, ID: id, Source: s.Source, StartedAt: now.UTC()})
	if err != nil {
		return ProtectionRecord{}, err
	}
	if err = s.writeNew(intentName, strings.NewReader(string(raw))); err != nil {
		return ProtectionRecord{}, err
	}
	created := false
	defer func() {
		if created {
			resultErr = errors.Join(resultErr, s.discardOwnedPayload(name, intentName))
		} else {
			resultErr = errors.Join(resultErr, s.root.Remove(intentName))
		}
	}()
	_, err = createSnapshot(ctx, r.db, r.blobs, func() (*os.File, error) {
		f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		created = err == nil
		return f, err
	}, func() { s.root.Remove(name) })
	if err != nil {
		return ProtectionRecord{}, err
	}
	f, err := s.root.Open(name)
	if err != nil {
		return ProtectionRecord{}, err
	}
	defer f.Close()
	// Import time is completion time; snapshot time remains the database read
	// snapshot and is never advanced to hide a slow or failed capture.
	return s.importFile(ctx, f, time.Now().UTC(), kind)
}

// Run stops promptly when its parent process shuts down; checkpoints are
// idempotent by the latest committed snapshot, including after process restart.
func (r *ProtectionRunner) Run(ctx context.Context, report func(ProtectionTick, error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 4*time.Minute)
		result, err := r.Tick(attempt, time.Now().UTC())
		cancel()
		if report != nil {
			report(result, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
