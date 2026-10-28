package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const ProtectionRetention = 35 * 24 * time.Hour
const ProtectionMaxAge = 15 * time.Minute

// ProtectionStore owns a dedicated directory. Its operating-system lock is
// automatically released if the process dies. Files never contain credentials.
type ProtectionStore struct {
	Path   string
	Source Source
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
}

type ProtectionRecord struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	SnapshotAt time.Time `json:"snapshot_at"`
	ImportedAt time.Time `json:"imported_at"`
	SHA256     string    `json:"sha256"`
	Bytes      int64     `json:"bytes"`
	Schema     int       `json:"schema"`
}

type ProtectionStatus struct {
	State   string             `json:"state"`
	Records []ProtectionRecord `json:"records"`
	// This status describes data-only checkpoints, not binary-log continuity,
	// current authority reconciliation, or independent-media disaster recovery.
	Boundary string `json:"boundary"`
}

type protectionIdentity struct {
	Format int    `json:"format"`
	Source Source `json:"source"`
}

func OpenProtection(path string, source Source) (*ProtectionStore, error) {
	if !filepath.IsAbs(path) || !sourceName.MatchString(source.Database) || !uuidPattern.MatchString(source.ServerUUID) {
		return nil, errors.New("absolute protection directory and valid source required")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) == path {
		return nil, errors.New("protection directory cannot be a filesystem root")
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("protection directory must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), path) {
		return nil, errors.New("protection directory must not traverse links")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	s := &ProtectionStore{Path: path, Source: source, root: root}
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	// Existing unowned directories are never repurposed for automatic deletion.
	if _, err = root.Lstat("protection.json"); os.IsNotExist(err) {
		entries, readErr := s.entries()
		if readErr != nil {
			return nil, errors.New("protection directory is not empty or owned")
		}
		for _, entry := range entries {
			info, statErr := root.Lstat(entry.Name())
			if statErr != nil || !info.Mode().IsRegular() {
				return nil, errors.New("unsafe protection initialization residue")
			}
			// These are the only artifacts possible before identity publication.
			// They contain no backup payload; all other nonempty directories fail.
			if entry.Name() == ".writer.lock" && info.Size() == 0 {
				continue
			}
			if entry.Name() == "protection.json.tmp" && info.Size() <= 4096 {
				continue
			}
			return nil, errors.New("protection directory is not empty or owned")
		}
	} else if err != nil {
		return nil, err
	}
	if info, err := root.Lstat(".writer.lock"); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("unsafe protection lock")
	}
	s.lock, err = root.OpenFile(".writer.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockProtection(s.lock); err != nil {
		return nil, errors.New("protection repository already in use")
	}
	raw, err := s.readRegular("protection.json", 4096)
	if os.IsNotExist(err) {
		// Another initialization may have completed while acquiring the lock;
		// only this locked, still-unpublished repository can clear its old temp.
		if removeErr := root.Remove("protection.json.tmp"); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, removeErr
		}
		raw, err = json.Marshal(protectionIdentity{Format: 1, Source: source})
		if err == nil {
			err = s.writeNew("protection.json", strings.NewReader(string(raw)))
		}
	} else if err == nil {
		var identity protectionIdentity
		if strictJSON(raw, &identity) != nil || identity.Format != 1 || identity.Source != source {
			return nil, errors.New("protection source identity mismatch")
		}
	}
	if err != nil {
		return nil, err
	}
	success = true
	return s, nil
}

func (s *ProtectionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	return err
}

func (s *ProtectionStore) entries() ([]os.DirEntry, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(20001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 20000 {
		return nil, errors.New("protection inventory capacity exceeded")
	}
	return entries, nil
}

func (s *ProtectionStore) readRegular(name string, limit int64) ([]byte, error) {
	i, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Size() > limit {
		return nil, errors.New("unsafe protection file")
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, errors.New("protection file capacity exceeded")
	}
	return raw, err
}

func (s *ProtectionStore) writeNew(name string, src io.Reader) error {
	if _, err := s.root.Lstat(name); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary := name + ".tmp"
	created, err := s.writePayload(temporary, src)
	if created {
		defer s.root.Remove(temporary)
	}
	if err != nil {
		return err
	}
	// All cooperating writers hold the repository's OS lock. Publishing only
	// after Sync means a process crash cannot expose a half-written intent.
	return s.root.Rename(temporary, name)
}

func (s *ProtectionStore) writePayload(name string, src io.Reader) (bool, error) {
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(f, src)
	syncErr := f.Sync()
	closeErr := f.Close()
	return true, errors.Join(copyErr, syncErr, closeErr)
}

func (s *ProtectionStore) discardOwnedPayload(payload, intent string) error {
	if err := s.root.Remove(payload); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.root.Remove(intent); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *ProtectionStore) Import(ctx context.Context, input string, now time.Time, kind string) (ProtectionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(input)
	if err != nil {
		return ProtectionRecord{}, err
	}
	defer f.Close()
	return s.importFile(ctx, f, now, kind)
}

// Caller holds mu; the input handle remains bound to the inspected source.
func (s *ProtectionStore) importFile(ctx context.Context, f *os.File, now time.Time, kind string) (result ProtectionRecord, resultErr error) {
	var record ProtectionRecord
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if s.root == nil || (kind != "baseline" && kind != "checkpoint") || now.IsZero() {
		return record, errors.New("invalid protection import")
	}
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return record, errors.New("invalid snapshot source file")
	}
	a, err := ValidateArchive(f, stat.Size())
	if err != nil {
		return record, err
	}
	if a.Manifest.Source != s.Source {
		return record, errors.New("snapshot source identity mismatch")
	}
	if a.Manifest.SnapshotAt.After(now.Add(time.Minute)) {
		return record, errors.New("snapshot is in the future")
	}
	if !a.Manifest.SnapshotAt.After(now.Add(-ProtectionRetention)) {
		return record, errors.New("snapshot already expired")
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return record, err
	}
	record = ProtectionRecord{ID: hex.EncodeToString(idBytes), Kind: kind, SnapshotAt: a.Manifest.SnapshotAt, ImportedAt: now.UTC(), Bytes: stat.Size(), Schema: a.Manifest.Schema}
	archiveName, pendingName, recordName := record.ID+".zip", record.ID+".pending", record.ID+".json"
	committed := false
	archiveCreated, pendingCreated := false, false
	defer func() {
		if !committed {
			if archiveCreated {
				resultErr = errors.Join(resultErr, s.discardOwnedPayload(archiveName, pendingName))
			} else if pendingCreated {
				resultErr = errors.Join(resultErr, s.root.Remove(pendingName))
			}
		}
	}()
	hasher := sha256.New()
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ProtectionRecord{}, err
	}
	if _, err = io.Copy(hasher, io.LimitReader(f, maxArchiveBytes+1)); err != nil {
		return ProtectionRecord{}, err
	}
	record.SHA256 = hex.EncodeToString(hasher.Sum(nil))
	raw, err := json.Marshal(record)
	if err != nil {
		return ProtectionRecord{}, err
	}
	if err = s.writeNew(pendingName, strings.NewReader(string(raw))); err != nil {
		return ProtectionRecord{}, err
	}
	pendingCreated = true
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ProtectionRecord{}, err
	}
	archiveCreated, err = s.writePayload(archiveName, io.LimitReader(f, maxArchiveBytes+1))
	if err != nil {
		return ProtectionRecord{}, err
	}
	// Revalidate the durable copy, not only the source which may have changed.
	if err = s.verifyRecord(ctx, record); err != nil {
		return ProtectionRecord{}, err
	}
	if err = ctx.Err(); err != nil {
		return ProtectionRecord{}, err
	}
	if err = s.root.Rename(pendingName, recordName); err != nil {
		return ProtectionRecord{}, err
	}
	committed = true
	return record, nil
}

func (s *ProtectionStore) records() ([]ProtectionRecord, error) {
	entries, err := s.entries()
	if err != nil {
		return nil, err
	}
	result := []ProtectionRecord{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !hex32.MatchString(strings.TrimSuffix(name, ".json")) {
			continue
		}
		raw, err := s.readRegular(name, 4096)
		if err != nil {
			return nil, err
		}
		var record ProtectionRecord
		if strictJSON(raw, &record) != nil || record.ID+".json" != name || !hex64.MatchString(record.SHA256) || record.Bytes <= 0 || record.Bytes > maxArchiveBytes || record.SnapshotAt.IsZero() || record.ImportedAt.IsZero() || (record.Kind != "baseline" && record.Kind != "checkpoint") {
			return nil, errors.New("invalid protection record")
		}
		result = append(result, record)
	}
	slices.SortFunc(result, func(a, b ProtectionRecord) int {
		if c := b.SnapshotAt.Compare(a.SnapshotAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

func (s *ProtectionStore) verifyRecord(ctx context.Context, record ProtectionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := s.readRegular(record.ID+".zip", maxArchiveBytes)
	if err != nil {
		return err
	}
	if int64(len(raw)) != record.Bytes || digest(raw) != record.SHA256 {
		return errors.New("protection archive integrity mismatch")
	}
	a, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	if a.Manifest.Source != s.Source || a.Manifest.Schema != record.Schema || !a.Manifest.SnapshotAt.Equal(record.SnapshotAt) {
		return errors.New("protection manifest mismatch")
	}
	return ctx.Err()
}

func (s *ProtectionStore) Verify(ctx context.Context, id string) (ProtectionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hex32.MatchString(id) || s.root == nil {
		return ProtectionRecord{}, errors.New("invalid protection checkpoint")
	}
	records, err := s.records()
	if err != nil {
		return ProtectionRecord{}, err
	}
	for _, record := range records {
		if record.ID == id {
			return record, s.verifyRecord(ctx, record)
		}
	}
	return ProtectionRecord{}, os.ErrNotExist
}

func (s *ProtectionStore) Status(ctx context.Context, now time.Time) (ProtectionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := ProtectionStatus{State: "MISSING", Records: []ProtectionRecord{}, Boundary: "Data-only checkpoints; binary-log continuity, current authority, recovery activation and independent-media recovery are not qualified by this status."}
	if s.root == nil || now.IsZero() {
		return status, errors.New("protection repository unavailable")
	}
	records, err := s.records()
	if err != nil {
		return status, err
	}
	status.Records = records
	if len(records) == 0 {
		return status, nil
	}
	if err = s.verifyRecord(ctx, records[0]); err != nil {
		status.State = "CORRUPT"
		return status, err
	}
	age := now.Sub(records[0].SnapshotAt)
	status.State = "CURRENT"
	if age < -time.Minute {
		status.State = "CLOCK_ERROR"
	} else if age > ProtectionMaxAge {
		status.State = "STALE"
	}
	return status, nil
}

func (s *ProtectionStore) Prune(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || now.IsZero() {
		return 0, errors.New("protection repository unavailable")
	}
	records, err := s.records()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, record := range records {
		if err = ctx.Err(); err != nil {
			return deleted, err
		}
		if record.SnapshotAt.After(now.Add(-ProtectionRetention)) {
			continue
		}
		// Only exact inventory IDs are removed, never arbitrary/recursive paths.
		// Missing archive is an interrupted prior prune, so finish its metadata.
		if err = s.root.Remove(record.ID + ".zip"); err != nil && !os.IsNotExist(err) {
			return deleted, fmt.Errorf("expired checkpoint removal failed: %w", err)
		}
		if err = s.root.Remove(record.ID + ".json"); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// CleanupInterrupted removes only archives with a durable uncommitted intent.
// Unrecognized files and committed checkpoints are never inferred to be garbage.
func (s *ProtectionStore) CleanupInterrupted(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || now.IsZero() {
		return 0, errors.New("protection repository unavailable")
	}
	entries, err := s.entries()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return deleted, err
		}
		reservedTemp := false
		for _, suffix := range []string{".pending.tmp", ".capture.intent.tmp", ".health.tmp", ".health.tmp.tmp"} {
			if strings.HasSuffix(entry.Name(), suffix) && hex32.MatchString(strings.TrimSuffix(entry.Name(), suffix)) {
				reservedTemp = true
				break
			}
		}
		if reservedTemp {
			expired, err := s.expiredIntent(entry.Name(), time.Time{}, now)
			if err != nil {
				return deleted, err
			}
			if expired {
				if err := s.root.Remove(entry.Name()); err != nil {
					return deleted, err
				}
				deleted++
			}
			continue
		}
		if strings.HasSuffix(entry.Name(), ".capture.intent") {
			id := strings.TrimSuffix(entry.Name(), ".capture.intent")
			if !hex32.MatchString(id) {
				continue
			}
			raw, err := s.readRegular(entry.Name(), 4096)
			if err != nil {
				return deleted, err
			}
			var intent captureIntent
			started := time.Time{}
			if strictJSON(raw, &intent) == nil && intent.Format == 1 && intent.ID == id && !intent.StartedAt.IsZero() {
				if intent.Source != s.Source {
					return deleted, errors.New("interrupted capture source mismatch")
				}
				started = intent.StartedAt
			}
			expired, err := s.expiredIntent(entry.Name(), started, now)
			if err != nil {
				return deleted, err
			}
			if !expired {
				continue
			}
			if err = s.discardOwnedPayload(id+".capture", entry.Name()); err != nil {
				return deleted, err
			}
			deleted++
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".pending")
		if id == entry.Name() || !hex32.MatchString(id) {
			continue
		}
		raw, err := s.readRegular(entry.Name(), 4096)
		if err != nil {
			return deleted, err
		}
		var record ProtectionRecord
		started := time.Time{}
		if strictJSON(raw, &record) == nil && record.ID == id && !record.ImportedAt.IsZero() && hex64.MatchString(record.SHA256) {
			started = record.ImportedAt
		}
		expired, err := s.expiredIntent(entry.Name(), started, now)
		if err != nil {
			return deleted, err
		}
		if !expired {
			continue
		}
		if _, err = s.root.Lstat(id + ".json"); err == nil {
			return deleted, errors.New("checkpoint has conflicting commit and intent")
		} else if !os.IsNotExist(err) {
			return deleted, err
		}
		if err = s.discardOwnedPayload(id+".zip", entry.Name()); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// A truncated legacy intent has no trustworthy embedded time. Its reserved
// filename still owns only the matching payload, and filesystem age provides
// a bounded grace period without parsing the malformed JSON on every tick.
func (s *ProtectionStore) expiredIntent(name string, started, now time.Time) (bool, error) {
	info, err := s.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return false, errors.New("unsafe interrupted protection metadata")
	}
	if started.IsZero() {
		started = info.ModTime()
	}
	return !started.After(now.Add(-24 * time.Hour)), nil
}
