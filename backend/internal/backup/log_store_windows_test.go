//go:build windows

package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestLogStoreWindowsLockedExpiredSegmentRetainsCleanupOwner(t *testing.T) {
	r, b, baseline, _ := journalFixture(t)
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	other := b
	var second string
	for i := 0; i < 200; i++ {
		other.Position.Offset++
		bundle, e := encodeLogBundle(other, baseline)
		if e != nil {
			t.Fatal(e)
		}
		if digest(bundle) > id {
			second, err = s.CommitBaseline(ctx, other, baseline, now)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if second == "" {
		t.Fatal("could not arrange lock before independent owner")
	}
	raw, err := r.PrepareSegment(ctx, nil, b.Position, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, id, raw, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, id+"."+digest(raw)+".seg")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != 0 {
			windows.CloseHandle(h)
		}
	}()
	expired := b.ObservedAt.Add(ProtectionRetention)
	if err = s.Prune(ctx, expired); err == nil {
		t.Fatal("actual Windows denial not observed")
	}
	if _, err = os.Stat(filepath.Join(root, second+".base")); !os.IsNotExist(err) {
		t.Fatal("locked owner blocked unrelated expiry", err)
	}
	if _, err = os.Stat(filepath.Join(root, id+".base")); err != nil {
		t.Fatal("failed cleanup lost baseline ownership", err)
	}
	if _, err = s.Load(ctx, id, expired); err == nil {
		t.Fatal("locked expired point remained usable")
	}
	if err = windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = 0
	if err = s.Prune(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("segment survived cleanup retry", err)
	}
	if _, err = os.Stat(filepath.Join(root, id+".base")); !os.IsNotExist(err) {
		t.Fatal("baseline survived cleanup retry", err)
	}
}
