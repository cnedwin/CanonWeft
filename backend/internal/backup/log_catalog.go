package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
)

type LogBaselineRecord struct {
	ID         string          `json:"id"`
	Schema     int             `json:"schema"`
	ObservedAt time.Time       `json:"observed_at"`
	State      string          `json:"state"`
	Position   binlog.Position `json:"position"`
}

// Catalog describes baseline files only. A record becomes usable for recovery
// only after Load verifies its complete committed chain and retention boundary.
func (s *LogStore) Catalog(ctx context.Context) ([]LogBaselineRecord, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return nil, err
	}
	entries, err := s.store.entries()
	if err != nil {
		return nil, err
	}
	var records []LogBaselineRecord
	owners := map[string]bool{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := logBaselineName(entry.Name())
		if !ok {
			continue
		}
		owners[id] = true
		record := LogBaselineRecord{ID: id, State: "CORRUPT"}
		b, _, e := s.baseline(id)
		if e == nil {
			record.Schema = b.Schema
			record.ObservedAt = b.ObservedAt
			record.State = "BASELINE"
			record.Position = b.Position
		}
		records = append(records, record)
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".commit")
		id, _, ok := logSegmentName(name)
		if !ok && strings.HasSuffix(entry.Name(), ".gap") {
			id = strings.TrimSuffix(entry.Name(), ".gap")
			ok = hex64.MatchString(id)
		}
		if !ok || owners[id] {
			continue
		}
		owners[id] = true
		records = append(records, LogBaselineRecord{ID: id, State: "MISSING_BASELINE"})
	}
	slices.SortFunc(records, func(a, b LogBaselineRecord) int { return b.ObservedAt.Compare(a.ObservedAt) })
	return records, ctx.Err()
}

func (s *LogStore) CaptureBaseline(ctx context.Context, db *sql.DB, blobs storage.BlobStore) (record LogBaselineRecord, resultErr error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return record, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return record, err
	}
	// A reserved temporary extension gives interrupted capture a rooted owner
	// even when a process terminates before it can construct the final bundle.
	temporary := hex.EncodeToString(random[:]) + ".base.tmp"
	created := false
	defer func() {
		if created {
			resultErr = errors.Join(resultErr, s.removeRegular(temporary))
		}
	}()
	b, _, err := createLogBaseline(ctx, db, blobs, func() (*os.File, error) {
		f, e := s.store.root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		created = e == nil
		return f, e
	}, func() { _ = s.removeRegular(temporary) })
	if err != nil {
		return record, err
	}
	if b.Source != s.store.Source || !validLogAge(b.ObservedAt, time.Now().UTC()) {
		return record, errors.New("captured log baseline source or clock mismatch")
	}
	archive, err := s.store.readRegular(temporary, maxArchiveBytes)
	if err != nil {
		return record, err
	}
	if _, err = OpenLogReplay(b, archive); err != nil {
		return record, err
	}
	raw, err := encodeLogBundle(b, archive)
	if err != nil {
		return record, err
	}
	id := digest(raw)
	if err = s.publish(ctx, id+".base", raw, maxLogBundleBytes); err != nil {
		return record, err
	}
	return LogBaselineRecord{ID: id, Schema: b.Schema, ObservedAt: b.ObservedAt, State: "BASELINE", Position: b.Position}, nil
}
