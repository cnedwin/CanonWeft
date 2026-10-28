package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRecoveryPreparedSurvivesActualWriterCrash(t *testing.T) {
	base := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryPreparedCrashHelper$")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = append(os.Environ(), "STUDIO_RECOVERY_CRASH_TEST_ROOT="+base)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "READY\n" {
		t.Fatal("writer never reached prepared stage", line, err)
	}
	journal, err := os.ReadFile(filepath.Join(base, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref BlobRef
	if err := json.Unmarshal(journal, &ref); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	store, err := NewLocalRetentionStore(filepath.Join(base, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := strings.Repeat("b", 32)
	objects, err := store.ScanRecoveryObjects(ctx, project, []BlobRef{ref})
	if err != nil || len(objects) != 1 || objects[0] != ref {
		t.Fatal("crashed writer was not journal-bound", objects, err)
	}
	if err := store.DeleteRecoveryPrepared(ctx, project, ref); err != nil {
		t.Fatal(err)
	}
	if objects, err := store.ScanObjects(ctx, project); err != nil || len(objects) != 0 {
		t.Fatal("crash residue", objects, err)
	}
}

type recoveryPreparedPause struct {
	context.Context
	path string
}

func (c recoveryPreparedPause) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		fmt.Println("READY")
		<-c.Done()
	}
	return c.Context.Err()
}

func TestRecoveryPreparedCrashHelper(t *testing.T) {
	base := os.Getenv("STUDIO_RECOVERY_CRASH_TEST_ROOT")
	if base == "" {
		return
	}
	project, body := strings.Repeat("b", 32), "real recovery writer interrupted before immutable link"
	root := filepath.Join(base, "objects")
	scope, err := NewRecoveryObjects(context.Background(), LocalBlobs{Root: root}, project)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	_, err = scope.Journal(func(_ context.Context, ref BlobRef) error {
		data, _ := json.Marshal(ref)
		file, err := os.OpenFile(filepath.Join(base, "journal.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			return err
		}
		return file.Sync()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = scope.PutObject(recoveryPreparedPause{ctx, filepath.Join(root, project, "prepared-recovery-"+contentHash([]byte(body)))}, project, strings.NewReader(body))
	t.Fatal("helper was not killed", err)
}

func TestRecoveryPreparedOccupiedFileRetriesWithoutLosingEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalRetentionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := strings.Repeat("b", 32)
	ref := BlobRef{Hash: contentHash([]byte("complete")), Size: 8}
	if err := os.Mkdir(filepath.Join(root, project), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, project, "prepared-recovery-"+ref.Hash)
	if err := os.WriteFile(path, []byte("par"), 0600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(path)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}()
	if store.DeleteRecoveryPrepared(context.Background(), project, ref) == nil {
		t.Fatal("occupied file counted as deleted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed delete lost evidence", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = 0
	if err := store.DeleteRecoveryPrepared(context.Background(), project, ref); err != nil {
		t.Fatal("retry failed", err)
	}
}
