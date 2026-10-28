package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/binlog"
)

const maxLogBundleBytes = maxArchiveBytes + (2 << 20)

// LogStore owns a separate, source-bound repository. Each immutable payload has
// an independent commit receipt, so loss of a committed tail is detectable.
type LogStore struct {
	store          *ProtectionStore
	inventoryLimit int
}

type logSegmentReceipt struct {
	Format        int    `json:"format"`
	BaselineID    string `json:"baseline_id"`
	SegmentSHA256 string `json:"segment_sha256"`
}

func (s *LogStore) receipt(ctx context.Context, name string) error {
	id, hash, ok := logSegmentName(name)
	if !ok {
		return errors.New("invalid segment receipt name")
	}
	raw, err := json.Marshal(logSegmentReceipt{Format: 1, BaselineID: id, SegmentSHA256: hash})
	if err != nil {
		return err
	}
	return s.publish(ctx, name+".commit", raw, 4096)
}

func OpenLogStore(path string, source Source) (*LogStore, error) {
	p, err := OpenProtection(path, source)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			p.Close()
		}
	}()
	raw, err := p.readRegular("journal.json", 4096)
	if os.IsNotExist(err) {
		entries, e := p.entries()
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			switch entry.Name() {
			case "protection.json", ".writer.lock", "journal.json.tmp":
			default:
				return nil, errors.New("log journal requires a dedicated repository")
			}
		}
		if i, e := p.root.Lstat("journal.json.tmp"); e == nil {
			if !i.Mode().IsRegular() || i.Size() > 4096 {
				return nil, errors.New("invalid journal initialization residue")
			}
			if e = p.root.Remove("journal.json.tmp"); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		raw, err = json.Marshal(protectionIdentity{Format: 1, Source: source})
		if err == nil {
			err = p.writeNew("journal.json", bytes.NewReader(raw))
		}
	} else if err == nil {
		var identity protectionIdentity
		if strictLogJSON(raw, &identity) != nil || identity.Format != 1 || identity.Source != source {
			return nil, errors.New("log journal identity mismatch")
		}
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return &LogStore{store: p, inventoryLimit: 20000}, nil
}

func (s *LogStore) Close() error { return s.store.Close() }

func encodeLogBundle(b LogBaseline, archive []byte) ([]byte, error) {
	metadata, err := json.Marshal(b)
	if err != nil || len(metadata) > 1<<20 || int64(len(archive)) > maxArchiveBytes {
		return nil, errors.New("log baseline bundle capacity exceeded")
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, file := range []struct {
		name string
		body []byte
	}{{"baseline.json", metadata}, {"archive.zip", archive}} {
		h := &zip.FileHeader{Name: file.name, Method: zip.Store}
		h.SetMode(0600)
		e, err := w.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err = e.Write(file.body); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > maxLogBundleBytes {
		return nil, errors.New("log baseline bundle capacity exceeded")
	}
	return buf.Bytes(), nil
}

func decodeLogBundle(raw []byte) (LogBaseline, []byte, error) {
	var b LogBaseline
	if int64(len(raw)) > maxLogBundleBytes {
		return b, nil, errors.New("log baseline bundle capacity exceeded")
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(z.File) != 2 {
		return b, nil, errors.New("invalid log baseline bundle")
	}
	parts := map[string][]byte{}
	for _, file := range z.File {
		limit := int64(1 << 20)
		if file.Name == "archive.zip" {
			limit = maxArchiveBytes
		} else if file.Name != "baseline.json" {
			return b, nil, errors.New("unexpected log baseline file")
		}
		if _, exists := parts[file.Name]; exists || file.Method != zip.Store || !file.Mode().IsRegular() || file.UncompressedSize64 > uint64(limit) {
			return b, nil, errors.New("invalid log baseline member")
		}
		f, e := file.Open()
		if e != nil {
			return b, nil, e
		}
		body, e := io.ReadAll(io.LimitReader(f, limit+1))
		closeErr := f.Close()
		if e != nil || closeErr != nil || int64(len(body)) > limit {
			return b, nil, errors.New("invalid log baseline member bytes")
		}
		parts[file.Name] = body
	}
	if strictLogJSON(parts["baseline.json"], &b) != nil || b.Format != 1 || !hex64.MatchString(b.ArchiveSHA256) || digest(parts["archive.zip"]) != b.ArchiveSHA256 || b.ObservedAt.IsZero() {
		return b, nil, errors.New("invalid log baseline binding")
	}
	return b, parts["archive.zip"], nil
}

func validLogAge(at, now time.Time) bool {
	return !now.IsZero() && !at.IsZero() && at.After(now.Add(-ProtectionRetention)) && !at.After(now.Add(time.Minute))
}

func (s *LogStore) usable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.store.root == nil {
		return errors.New("log journal closed")
	}
	return nil
}

// publish is called only with fixed, hash-derived names under the writer lock.
// A temporary payload can be replaced after a crash; committed bytes cannot.
func (s *LogStore) publish(ctx context.Context, name string, raw []byte, limit int64) error {
	if err := s.usable(ctx); err != nil {
		return err
	}
	if existing, err := s.store.readRegular(name, limit); err == nil {
		if !bytes.Equal(existing, raw) {
			return errors.New("immutable journal file differs")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	// Payload admission reserves space for its receipt and a temporary file.
	// Receipt repair may use that reserve, including after a process crash.
	admission := s.inventoryLimit - 2
	if strings.HasSuffix(name, ".seg.commit") {
		admission = s.inventoryLimit
	}
	if entries, err := s.store.entries(); err != nil {
		return err
	} else if len(entries) >= admission {
		return errors.New("log journal inventory capacity exceeded")
	}
	temporary := name + ".tmp"
	if info, err := s.store.root.Lstat(temporary); err == nil {
		if !info.Mode().IsRegular() || info.Size() > limit {
			return errors.New("unsafe journal temporary")
		}
		if err = s.store.root.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	created, err := s.store.writePayload(temporary, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if !created {
		return errors.New("journal payload not created")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return s.store.root.Rename(temporary, name)
}

func (s *LogStore) CommitBaseline(ctx context.Context, b LogBaseline, archive []byte, now time.Time) (string, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return "", err
	}
	if b.Source != s.store.Source || !validLogAge(b.ObservedAt, now) {
		return "", errors.New("log baseline source or retention mismatch")
	}
	if _, err := OpenLogReplay(b, archive); err != nil {
		return "", err
	}
	raw, err := encodeLogBundle(b, archive)
	if err != nil {
		return "", err
	}
	id := digest(raw)
	if err = s.publish(ctx, id+".base", raw, maxLogBundleBytes); err != nil {
		return "", err
	}
	return id, nil
}

func logSegmentName(name string) (baseline, hash string, ok bool) {
	if len(name) != 133 || name[64] != '.' || !strings.HasSuffix(name, ".seg") {
		return "", "", false
	}
	baseline, hash = name[:64], name[65:129]
	return baseline, hash, hex64.MatchString(baseline) && hex64.MatchString(hash)
}
func logBaselineName(name string) (string, bool) {
	if len(name) != 69 || !strings.HasSuffix(name, ".base") {
		return "", false
	}
	return name[:64], hex64.MatchString(name[:64])
}

func (s *LogStore) baseline(id string) (LogBaseline, []byte, error) {
	if !hex64.MatchString(id) {
		return LogBaseline{}, nil, errors.New("invalid log baseline identifier")
	}
	raw, err := s.store.readRegular(id+".base", maxLogBundleBytes)
	if err != nil {
		return LogBaseline{}, nil, err
	}
	if digest(raw) != id {
		return LogBaseline{}, nil, errors.New("log baseline integrity failure")
	}
	b, archive, err := decodeLogBundle(raw)
	if err == nil && b.Source != s.store.Source {
		err = errors.New("log baseline source mismatch")
	}
	return b, archive, err
}

// load validates every published segment, including disconnected or forked
// files. It cannot silently choose a healthier-looking prefix after damage.
func (s *LogStore) load(ctx context.Context, id string, now time.Time) (*LogReplay, []byte, []string, error) {
	if err := s.usable(ctx); err != nil {
		return nil, nil, nil, err
	}
	b, archive, err := s.baseline(id)
	if err != nil {
		return nil, nil, nil, err
	}
	if !validLogAge(b.ObservedAt, now) {
		return nil, nil, nil, errors.New("log baseline outside recovery retention")
	}
	r, err := OpenLogReplay(b, archive)
	if err != nil {
		return nil, nil, nil, err
	}
	entries, err := s.store.entries()
	if err != nil {
		return nil, nil, nil, err
	}
	next := map[string]string{}
	payloads := map[string]bool{}
	var receipts []string
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if strings.HasSuffix(entry.Name(), ".commit") {
			name := strings.TrimSuffix(entry.Name(), ".commit")
			owner, hash, ok := logSegmentName(name)
			if !ok || owner != id {
				continue
			}
			raw, e := s.store.readRegular(entry.Name(), 4096)
			if e != nil {
				return nil, nil, nil, e
			}
			var receipt logSegmentReceipt
			if strictLogJSON(raw, &receipt) != nil || receipt.Format != 1 || receipt.BaselineID != owner || receipt.SegmentSHA256 != hash {
				return nil, nil, nil, errors.New("invalid log commit receipt")
			}
			receipts = append(receipts, name)
			continue
		}
		owner, hash, ok := logSegmentName(entry.Name())
		if !ok || owner != id {
			continue
		}
		raw, e := s.store.readRegular(entry.Name(), maxDataBytes)
		if e != nil {
			return nil, nil, nil, e
		}
		if digest(raw) != hash {
			return nil, nil, nil, errors.New("log segment integrity failure")
		}
		var segment LogSegment
		if strictLogJSON(raw, &segment) != nil || !hex64.MatchString(segment.PreviousSHA256) {
			return nil, nil, nil, errors.New("invalid durable segment")
		}
		if _, exists := next[segment.PreviousSHA256]; exists {
			return nil, nil, nil, errors.New("forked log journal")
		}
		next[segment.PreviousSHA256] = entry.Name()
		payloads[entry.Name()] = true
	}
	for _, name := range receipts {
		if !payloads[name] {
			return nil, nil, nil, errors.New("committed log segment is missing")
		}
	}
	var order []string
	for len(order) < len(next) {
		if err = ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		name, exists := next[r.Point().Hash]
		if !exists {
			return nil, nil, nil, errors.New("disconnected log journal")
		}
		_, hash, _ := logSegmentName(name)
		raw, e := s.store.readRegular(name, maxDataBytes)
		if e != nil {
			return nil, nil, nil, e
		}
		if e = r.ApplySegmentContext(ctx, raw, hash); e != nil {
			return nil, nil, nil, e
		}
		if r.Point().ObservedAt.After(now.Add(time.Minute)) {
			return nil, nil, nil, errors.New("log segment observation is in the future")
		}
		order = append(order, name)
	}
	// A crash after payload publication but before receipt publication leaves a
	// complete orphan. Only a fully validated, connected chain may repair it.
	for _, name := range order {
		if err = s.receipt(ctx, name); err != nil {
			return nil, nil, nil, err
		}
	}
	return r, archive, order, nil
}

func (s *LogStore) Load(ctx context.Context, id string, now time.Time) (*LogReplay, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	r, _, _, err := s.load(ctx, id, now)
	return r, err
}

func (s *LogStore) Append(ctx context.Context, id string, raw []byte, now time.Time) (LogPoint, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	r, _, order, err := s.load(ctx, id, now)
	if err != nil {
		return LogPoint{}, err
	}
	hash := digest(raw)
	name := id + "." + hash + ".seg"
	if slices.Contains(order, name) {
		return r.Point(), nil
	}
	if err = r.ApplySegmentContext(ctx, raw, hash); err != nil {
		return LogPoint{}, err
	}
	if r.Point().ObservedAt.After(now.Add(time.Minute)) {
		return LogPoint{}, errors.New("log segment observation is in the future")
	}
	if err = s.publish(ctx, name, raw, maxDataBytes); err != nil {
		return LogPoint{}, err
	}
	if err = s.receipt(ctx, name); err != nil {
		return LogPoint{}, err
	}
	return r.Point(), nil
}

func (s *LogStore) Archive(ctx context.Context, id string, position binlog.Position, now time.Time) ([]byte, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	r, archive, order, err := s.load(ctx, id, now)
	if err != nil {
		return nil, err
	}
	if position == r.Point().Position {
		return r.ArchiveBytes()
	}
	r, err = OpenLogReplay(r.baseline, archive)
	if err != nil {
		return nil, err
	}
	if position == r.Point().Position {
		return r.ArchiveBytes()
	}
	for _, name := range order {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		_, hash, _ := logSegmentName(name)
		raw, e := s.store.readRegular(name, maxDataBytes)
		if e != nil {
			return nil, e
		}
		var segment LogSegment
		if strictLogJSON(raw, &segment) != nil {
			return nil, errors.New("invalid durable segment")
		}
		if cmp, e := binlog.ComparePositions(position, segment.Through); e != nil {
			return nil, e
		} else if cmp <= 0 {
			return r.ArchiveAt(raw, hash, position)
		}
		if e = r.ApplySegment(raw, hash); e != nil {
			return nil, e
		}
	}
	return nil, errors.New("selected recovery point is not available")
}

func (s *LogStore) Prune(ctx context.Context, now time.Time) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("retention time required")
	}
	entries, err := s.store.entries()
	if err != nil {
		return err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	var failures error
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		id, ok := logBaselineName(entry.Name())
		if !ok {
			continue
		}
		b, _, e := s.baseline(id)
		if e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		if b.ObservedAt.After(now.Add(-ProtectionRetention)) {
			continue
		}
		// Keep the baseline until every owned segment is removed. If Windows
		// sharing rules interrupt deletion, the next sweep has the same owner.
		failed := false
		for _, child := range entries {
			owner, _, valid := logSegmentName(child.Name())
			if !valid || owner != id {
				continue
			}
			if e = ctx.Err(); e != nil {
				return errors.Join(failures, e)
			}
			if e = s.removeRegular(child.Name()); e != nil {
				failures = errors.Join(failures, e)
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		for _, child := range entries {
			if !strings.HasSuffix(child.Name(), ".commit") {
				continue
			}
			owner, _, valid := logSegmentName(strings.TrimSuffix(child.Name(), ".commit"))
			if !valid || owner != id {
				continue
			}
			if e = ctx.Err(); e != nil {
				return errors.Join(failures, e)
			}
			if e = s.removeRegular(child.Name()); e != nil {
				failures = errors.Join(failures, e)
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		if e = ctx.Err(); e != nil {
			return errors.Join(failures, e)
		}
		if e = s.removeRegular(id + ".gap"); e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		if e = ctx.Err(); e != nil {
			return errors.Join(failures, e)
		}
		if e = s.removeRegular(id + ".rollover"); e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		if e = ctx.Err(); e != nil {
			return errors.Join(failures, e)
		}
		if e = s.removeRegular(entry.Name()); e != nil {
			failures = errors.Join(failures, e)
		}
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".tmp") {
			continue
		}
		original := strings.TrimSuffix(name, ".tmp")
		_, baseline := logBaselineName(original)
		_, _, segment := logSegmentName(strings.TrimSuffix(original, ".commit"))
		rollover := strings.HasSuffix(original, ".rollover") && hex64.MatchString(strings.TrimSuffix(original, ".rollover"))
		gap := strings.HasSuffix(original, ".gap") && hex64.MatchString(strings.TrimSuffix(original, ".gap"))
		if !baseline && !segment && !rollover && !gap {
			continue
		}
		i, e := s.store.root.Lstat(name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		if !i.Mode().IsRegular() {
			failures = errors.Join(failures, errors.New("unsafe journal temporary"))
			continue
		}
		if !i.ModTime().After(now.Add(-24 * time.Hour)) {
			if e = ctx.Err(); e != nil {
				return errors.Join(failures, e)
			}
			if e = s.removeRegular(name); e != nil {
				failures = errors.Join(failures, e)
			}
		}
	}
	return errors.Join(failures, ctx.Err())
}
func (s *LogStore) removeRegular(name string) error {
	i, err := s.store.root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() {
		return errors.New("unsafe journal file")
	}
	return s.store.root.Remove(name)
}
