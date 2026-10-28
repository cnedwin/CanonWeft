package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/storage"
)

var protectionDatabaseName = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

func protectionDirectory(blobs, database, configured string) (string, error) {
	if !filepath.IsAbs(blobs) || !protectionDatabaseName.MatchString(database) {
		return "", errors.New("absolute blob directory and valid database required for automatic protection")
	}
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("configured backup directory must be absolute")
		}
		return filepath.Clean(configured), nil
	}
	return filepath.Join(filepath.Dir(blobs), "backups", database), nil
}

func startProtection(ctx context.Context, db *sql.DB, database string) (*backup.ProtectionRunner, func(), error) {
	blobs := os.Getenv("STUDIO_BLOB_ROOT")
	directory, err := protectionDirectory(blobs, database, os.Getenv("STUDIO_BACKUP_DIRECTORY"))
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return nil, nil, errors.New("automatic backup parent directory is unavailable")
	}
	startup, cancelStartup := context.WithTimeout(ctx, 20*time.Second)
	runner, err := backup.NewProtectionRunner(startup, db, storage.LocalBlobs{Root: blobs}, directory)
	cancelStartup()
	if err != nil {
		return nil, nil, errors.New("automatic backup repository unavailable; check source identity, directory permissions and other running instances")
	}
	running, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(running, func(result backup.ProtectionTick, err error) {
			if err != nil && running.Err() == nil {
				// Never print driver errors, paths, DSNs or content in diagnostics.
				log.Printf("Automatic backup needs attention: %s", result.State)
			} else if result.Captured {
				log.Printf("Automatic data checkpoint saved at %s", result.Record.SnapshotAt.UTC().Format(time.RFC3339))
			}
		})
	}()
	stop := func() {
		cancel()
		<-done
		if err := runner.Close(); err != nil {
			log.Print("Automatic backup repository close failed")
		}
	}
	return runner, stop, nil
}
