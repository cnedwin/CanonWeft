package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
	"strings"
	"time"
)

type LogPoint struct {
	Position     binlog.Position `json:"position"`
	ObservedAt   time.Time       `json:"observed_at"`
	LastCommitAt time.Time       `json:"last_commit_at,omitempty"`
	Hash         string          `json:"hash"`
}
type LogSegment struct {
	Format         int                  `json:"format"`
	BaselineSHA256 string               `json:"baseline_sha256"`
	PreviousSHA256 string               `json:"previous_sha256"`
	Source         Source               `json:"source"`
	Schema         int                  `json:"schema"`
	From           binlog.Position      `json:"from"`
	Through        binlog.Position      `json:"through"`
	ObservedAt     time.Time            `json:"observed_at"`
	Transactions   []binlog.Transaction `json:"transactions"`
	Objects        map[string][]byte    `json:"objects"`
}
type LogReplay struct {
	baseline LogBaseline
	archive  *Archive
	point    LogPoint
	capacity logCapacity
}

// Fixed production budgets; unit tests exercise the same boundary with smaller
// values. No capacity value is loaded from untrusted archive/segment metadata.
type logCapacity struct {
	data, archive, manifest     int64
	segmentObjects, segmentJSON int64
	entries                     int
}

// Only per-segment encoding/object budgets can be relieved by splitting a
// capture batch. Retained history and recovery-state limits are permanent.
var errLogSegmentCapacity = errors.New("log segment capacity exceeded")

func OpenLogReplay(b LogBaseline, raw []byte) (*LogReplay, error) {
	metadata, err := json.Marshal(b)
	_, schemaErr := logSchemaVersion(b.Schema)
	if err != nil || schemaErr != nil || len(metadata) > 1<<20 || b.Format != 1 || !hex64.MatchString(b.ArchiveSHA256) || digest(raw) != b.ArchiveSHA256 {
		return nil, errors.New("invalid log baseline binding")
	}
	var frozen LogBaseline
	if err := strictJSON(metadata, &frozen); err != nil {
		return nil, errors.New("invalid log baseline metadata")
	}
	if _, err := logContracts(frozen.Tables, frozen.Schema); err != nil {
		return nil, err
	}
	if _, err := binlog.ComparePositions(frozen.Position, frozen.Position); err != nil {
		return nil, err
	}
	a, err := validateArchiveContents(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	if a.Manifest.Schema != frozen.Schema || a.Manifest.Source != frozen.Source || !a.Manifest.SnapshotAt.Equal(frozen.ObservedAt) {
		return nil, errors.New("log baseline and archive provenance differ")
	}
	r := &LogReplay{baseline: frozen, archive: a, point: LogPoint{Position: frozen.Position, ObservedAt: frozen.ObservedAt, Hash: frozen.ArchiveSHA256}, capacity: logCapacity{data: maxDataBytes, archive: maxArchiveBytes, manifest: 1 << 20, entries: maxEntries, segmentObjects: maxArchiveBytes / 2, segmentJSON: maxDataBytes}}
	if _, err := r.materializationFiles(a.Data, a.Blobs, frozen.ObservedAt); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *LogReplay) Point() LogPoint { return r.point }

// PrepareSegment is called only after a source Reader has validated the whole
// observed range. It never advances the replay point or writes raw source rows.
func (r *LogReplay) PrepareSegment(ctx context.Context, blobs storage.BlobStore, end binlog.Position, observed time.Time, transactions []binlog.Transaction) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	segment := LogSegment{Format: 1, BaselineSHA256: r.baseline.ArchiveSHA256, PreviousSHA256: r.point.Hash, Source: r.baseline.Source, Schema: r.baseline.Schema, From: r.point.Position, Through: end, ObservedAt: observed, Objects: map[string][]byte{}}
	if len(transactions) > 1000 {
		return nil, errors.New("log segment transaction limit")
	}
	// Count the entire input before cloning even its first row image.
	var inputBytes int64
	var inputRows int
	for _, tx := range transactions {
		inputRows += len(tx.Changes)
		if inputRows > 100000 {
			return nil, errors.New("log segment row capacity exceeded")
		}
		for _, change := range tx.Changes {
			for _, row := range []binlog.Row{change.Before, change.After} {
				for _, cell := range row {
					inputBytes += int64(len(cell)) + 32
					if inputBytes > 64<<20 {
						return nil, errors.New("log segment byte capacity exceeded")
					}
				}
			}
		}
	}
	for _, tx := range transactions {
		clean, err := prepareLogTransaction(r.baseline.Tables, tx, r.baseline.Schema)
		if err != nil {
			return nil, err
		}
		segment.Transactions = append(segment.Transactions, clean)
	}
	_, refs, err := r.segmentRows(segment, nil)
	if err != nil {
		return nil, err
	}
	var total int64
	for path := range refs {
		if _, exists := r.archive.Blobs[path]; exists {
			continue
		}
		if blobs == nil {
			return nil, errors.New("log recovery object store unavailable")
		}
		parts := strings.Split(path, "/")
		body, err := blobs.ReadObject(ctx, parts[1], parts[2])
		if err != nil || int64(len(body)) > storage.MaxBlobBytes || digest(body) != parts[2] {
			return nil, errors.New("log recovery object missing or corrupt")
		}
		total += int64(len(body))
		if total > r.capacity.segmentObjects {
			return nil, fmt.Errorf("%w: object bytes", errLogSegmentCapacity)
		}
		segment.Objects[path] = body
	}
	raw, err := json.Marshal(segment)
	if err != nil {
		return nil, errors.New("log segment encoding failed")
	}
	if int64(len(raw)) > r.capacity.segmentJSON {
		return nil, fmt.Errorf("%w: encoded bytes", errLogSegmentCapacity)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Objects are now known. Every complete intermediate point must fit the
	// same export contract before these bytes can be durably published.
	if _, _, _, err := r.decodeSegment(raw, digest(raw)); err != nil {
		return nil, err
	}
	return raw, nil
}

func (r *LogReplay) segmentRows(segment LogSegment, check func(Data, time.Time) error) (Data, map[string]bool, error) {
	if segment.Format != 1 || segment.BaselineSHA256 != r.baseline.ArchiveSHA256 || segment.PreviousSHA256 != r.point.Hash || segment.Source != r.baseline.Source || segment.Schema != r.baseline.Schema || segment.From != r.point.Position || segment.ObservedAt.IsZero() || segment.ObservedAt.Before(r.point.ObservedAt) {
		return nil, nil, errors.New("log segment provenance or chain mismatch")
	}
	if order, err := binlog.ComparePositions(segment.From, segment.Through); err != nil || order > 0 {
		return nil, nil, errors.New("log segment range mismatch")
	}
	if len(segment.Transactions) > 1000 {
		return nil, nil, errors.New("log segment transaction limit")
	}
	state := r.archive.Data
	if err := r.checkLogDataCapacity(state); err != nil {
		return nil, nil, err
	}
	if check != nil {
		if err := check(state, r.point.ObservedAt); err != nil {
			return nil, nil, err
		}
	}
	refs, err := r.stateReferences(state, r.point.ObservedAt)
	if err != nil {
		return nil, nil, err
	}
	cursor := segment.From
	pointTime := r.point.ObservedAt
	var totalRows int
	var totalBytes int64
	for _, tx := range segment.Transactions {
		totalRows += len(tx.Changes)
		for _, change := range tx.Changes {
			for _, row := range []binlog.Row{change.Before, change.After} {
				for _, cell := range row {
					totalBytes += int64(len(cell)) + 32
				}
			}
		}
		if totalRows > 100000 || totalBytes > 64<<20 {
			return nil, nil, errors.New("log segment row capacity exceeded")
		}
		if order, err := binlog.ComparePositions(tx.End, segment.Through); err != nil || order > 0 {
			return nil, nil, errors.New("log segment overruns observed boundary")
		}
		pointTime = r.recoveryTime(pointTime, tx.CommittedAt, segment.ObservedAt)
		state, err = r.applyRows(state, cursor, tx.End, []binlog.Transaction{tx}, pointTime)
		if err != nil {
			return nil, nil, err
		}
		if err := r.checkLogDataCapacity(state); err != nil {
			return nil, nil, err
		}
		if check != nil {
			if err := check(state, pointTime); err != nil {
				return nil, nil, err
			}
		}
		pointRefs, err := r.stateReferences(state, pointTime)
		if err != nil {
			return nil, nil, err
		}
		for path := range pointRefs {
			refs[path] = true
		}
		cursor = tx.End
	}
	// Metadata-only tail coverage is supplied by the actual source Reader;
	// committed changes must still be contiguous and cannot overrun Through.
	if check != nil {
		if err := check(state, segment.ObservedAt); err != nil {
			return nil, nil, err
		}
	}
	return state, refs, nil
}

func (r *LogReplay) decodeSegment(raw []byte, expectedHash string) (LogSegment, Data, map[string][]byte, error) {
	var segment LogSegment
	if len(raw) == 0 || int64(len(raw)) > maxDataBytes || !hex64.MatchString(expectedHash) || digest(raw) != expectedHash {
		return segment, nil, nil, errors.New("log segment integrity failure")
	}
	if err := strictLogJSON(raw, &segment); err != nil {
		return segment, nil, nil, errors.New("invalid log segment encoding")
	}
	_, refs, err := r.segmentRows(segment, nil)
	if err != nil {
		return segment, nil, nil, err
	}
	if len(r.archive.Blobs)+len(segment.Objects)+2 > r.capacity.entries {
		return segment, nil, nil, errors.New("log recovery object history entry capacity exceeded")
	}
	objects := make(map[string][]byte, len(r.archive.Blobs)+len(segment.Objects))
	var total int64
	for path, body := range r.archive.Blobs {
		objects[path] = body
		total += int64(len(body))
	}
	missing := 0
	for path := range refs {
		if _, exists := objects[path]; exists {
			continue
		}
		missing++
		body, exists := segment.Objects[path]
		parts := strings.Split(path, "/")
		if !exists || int64(len(body)) > storage.MaxBlobBytes || digest(body) != parts[2] {
			return segment, nil, nil, errors.New("log recovery object missing or corrupt")
		}
		total += int64(len(body))
		if total > maxArchiveBytes {
			return segment, nil, nil, errors.New("log recovery object history capacity exceeded")
		}
		objects[path] = body
	}
	if missing != len(segment.Objects) {
		return segment, nil, nil, errors.New("unexpected or duplicated log recovery object")
	}
	data, _, err := r.segmentRows(segment, func(data Data, at time.Time) error { _, err := r.materializationFiles(data, objects, at); return err })
	if err != nil {
		return segment, nil, nil, err
	}
	return segment, data, objects, nil
}

func (r *LogReplay) ApplySegment(raw []byte, expectedHash string) error {
	segment, data, objects, err := r.decodeSegment(raw, expectedHash)
	if err != nil {
		return err
	}
	lastCommit := r.point.LastCommitAt
	if len(segment.Transactions) > 0 {
		lastCommit = segment.Transactions[len(segment.Transactions)-1].CommittedAt
	}
	// Only replace state after all row, object and chain checks succeed.
	r.archive.Data = data
	r.archive.Blobs = objects
	r.point = LogPoint{Position: segment.Through, ObservedAt: segment.ObservedAt, LastCommitAt: lastCommit, Hash: expectedHash}
	return nil
}

// ArchiveAt validates the entire segment first, then materializes exactly a
// complete chosen boundary. Later objects/changes are not copied to the archive.
func (r *LogReplay) ArchiveAt(raw []byte, expectedHash string, position binlog.Position) ([]byte, error) {
	segment, _, objects, err := r.decodeSegment(raw, expectedHash)
	if err != nil {
		return nil, err
	}
	selected := position == r.point.Position
	through := r.point.Position
	at := r.point.ObservedAt
	var prefix []binlog.Transaction
	for _, tx := range segment.Transactions {
		if selected {
			break
		}
		prefix = append(prefix, tx)
		through = tx.End
		at = r.recoveryTime(at, tx.CommittedAt, segment.ObservedAt)
		selected = tx.End == position
	}
	if position == segment.Through {
		selected = true
		through = segment.Through
		at = segment.ObservedAt
	}
	if !selected {
		return nil, errors.New("selected point is not a complete recovery boundary")
	}
	data, err := r.applyRows(r.archive.Data, r.point.Position, through, prefix, at)
	if err != nil {
		return nil, err
	}
	return r.materializedArchive(data, objects, at)
}

// Row positions order commits; the event header only has second precision and
// is not allowed to backdate a later recovery point before an observed one.
func logPointTime(previous, committed, observed time.Time) time.Time {
	if committed.After(observed) {
		committed = observed
	}
	if committed.After(previous) {
		return committed
	}
	return previous
}

func (r *LogReplay) ArchiveBytes() ([]byte, error) {
	return r.materializedArchive(r.archive.Data, r.archive.Blobs, r.point.ObservedAt)
}

func (r *LogReplay) materializedArchive(data Data, objects map[string][]byte, at time.Time) ([]byte, error) {
	return r.writeMaterializedArchive(data, objects, at)
}

// Reject duplicate object keys as well as unknown fields. A caller cannot hide
// extra content behind JSON's usual last-key-wins decoding behavior.
func strictLogJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("log JSON nesting limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate log JSON key")
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid log JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing log JSON")
	}
	return strictJSON(raw, out)
}
