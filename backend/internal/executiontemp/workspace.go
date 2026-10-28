// Package executiontemp owns disposable execution inputs, never author assets.
// An OS lock protects live users; a durable bounded marker survives process death.
package executiontemp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("temporary workspace requires maintenance")

// Leave one hour of scheduling/retry margin inside the 24-hour cleanup target.
const orphanGrace = 23 * time.Hour

type leaseRecord struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Workspace struct {
	root     *os.Root
	lease    *os.File
	base, id string
	once     sync.Once
	closeErr error
}

type Report struct {
	Removed int  `json:"removed"`
	Active  int  `json:"active"`
	Waiting int  `json:"waiting"`
	Blocked int  `json:"blocked"`
	More    bool `json:"more"`
}

func openRoot(base string) (*os.Root, string, error) {
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", ErrUnavailable
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	bound, err := root.Stat(".")
	if err != nil || !os.SameFile(info, bound) {
		root.Close()
		return nil, "", ErrUnavailable
	}
	return root, abs, nil
}

func Create(base string) (*Workspace, error) {
	if strings.TrimSpace(base) == "" {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, ErrUnavailable
	}
	root, abs, err := openRoot(base)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		root.Close()
		return nil, ErrUnavailable
	}
	id := hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(id+".lease", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		root.Close()
		return nil, ErrUnavailable
	}
	w := &Workspace{root: root, lease: file, base: abs, id: id}
	locked, err := lock(file)
	if err != nil || !locked {
		file.Close()
		root.Close()
		return nil, ErrUnavailable
	}
	raw, _ := json.Marshal(leaseRecord{Schema: 1, ID: id, CreatedAt: time.Now().UTC()})
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = root.Mkdir(id, 0700)
	}
	if err != nil {
		_ = w.Close()
		return nil, ErrUnavailable
	}
	return w, nil
}

func (w *Workspace) Path() string { return filepath.Join(w.base, w.id) }

// Caller closes all child processes/files first. Failed deletion keeps the
// ownership marker for the next sweep; no error includes private source data.
func (w *Workspace) Close() error {
	w.once.Do(func() {
		removeErr := w.root.RemoveAll(w.id)
		closeErr := w.lease.Close()
		if removeErr == nil && closeErr == nil {
			removeErr = w.root.Remove(w.id + ".lease")
		}
		rootErr := w.root.Close()
		if removeErr != nil || closeErr != nil || rootErr != nil {
			w.closeErr = ErrUnavailable
		}
	})
	return w.closeErr
}

func Sweep(ctx context.Context, base string) (Report, error) {
	return sweep(ctx, base, time.Now().UTC())
}

func sweep(ctx context.Context, base string, now time.Time) (Report, error) {
	report := Report{}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if strings.TrimSpace(base) == "" {
		return report, ErrUnavailable
	}
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return report, nil
	}
	root, _, err := openRoot(base)
	if err != nil {
		return report, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return report, ErrUnavailable
	}
	entries, readErr := directory.ReadDir(10001)
	directory.Close()
	if (readErr != nil && readErr != io.EOF) || len(entries) > 10000 {
		return report, ErrUnavailable
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".lease") {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				if _, err := root.Lstat(name + ".lease"); err != nil {
					report.Blocked++
				}
			}
			continue
		}
		id := strings.TrimSuffix(name, ".lease")
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != id || !entry.Type().IsRegular() {
			report.Blocked++
			continue
		}
		if report.Removed >= 128 {
			report.More = true
			break
		}
		switch sweepOne(root, name, id, now) {
		case "removed":
			report.Removed++
		case "active":
			report.Active++
		case "waiting":
			report.Waiting++
		default:
			report.Blocked++
		}
	}
	return report, nil
}

func sweepOne(root *os.Root, name, id string, now time.Time) string {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return "blocked"
	}
	file, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return "blocked"
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return "blocked"
	}
	locked, err := lock(file)
	if err != nil {
		return "blocked"
	}
	if !locked {
		return "active"
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(raw) > 1024 {
		return "blocked"
	}
	var record leaseRecord
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || record.Schema != 1 || record.ID != id || record.CreatedAt.IsZero() || record.CreatedAt.After(now) {
		return "blocked"
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "blocked"
	}
	if now.Before(record.CreatedAt.Add(orphanGrace)) {
		return "waiting"
	}
	// id is an exact locally generated hex component and Root confines all
	// recursive removal. Symlinks inside it cannot escape to author files.
	if info, err := root.Lstat(id); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "blocked"
	} else if err != nil && !os.IsNotExist(err) {
		return "blocked"
	}
	if err := root.RemoveAll(id); err != nil {
		return "blocked"
	}
	if err := file.Close(); err != nil {
		return "blocked"
	}
	if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
		return "blocked"
	}
	return "removed"
}
