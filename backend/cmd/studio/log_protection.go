package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
)

func logProtectionConnection(raw, database string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(raw)
	if err != nil || cfg.User == "" || cfg.User == "root" || cfg.Net != "tcp" || cfg.DBName != database || cfg.MultiStatements || cfg.AllowAllFiles {
		return nil, errors.New("local scoped log collector connection required")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("local scoped log collector connection required")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg, nil
}

func startLogProtection(ctx context.Context, database, serverUUID string) (*backup.LogRunner, func(), error) {
	raw := os.Getenv("STUDIO_LOG_DSN")
	if database == "studio_test" {
		raw = os.Getenv("STUDIO_TEST_LOG_DSN")
	}
	if raw == "" {
		return nil, func() {}, nil
	}
	cfg, err := logProtectionConnection(raw, database)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, nil, errors.New("log collector database unavailable")
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var sourceDatabase, sourceUUID string
	if err = db.QueryRowContext(startup, "SELECT DATABASE(),@@server_uuid").Scan(&sourceDatabase, &sourceUUID); err != nil || sourceDatabase != database || sourceUUID != serverUUID {
		return nil, nil, errors.New("log collector source does not match this workspace")
	}
	authoritySchema := os.Getenv("STUDIO_AUTHORITY_SCHEMA")
	if authoritySchema == "" {
		authoritySchema = "studio_authority"
	}
	if err = backup.VerifyLogReaderScope(startup, db, authoritySchema); err != nil {
		return nil, nil, errors.New("log collector permissions do not match the required read-only profile")
	}
	blobs := os.Getenv("STUDIO_BLOB_ROOT")
	snapshotPath, err := protectionDirectory(blobs, database, os.Getenv("STUDIO_BACKUP_DIRECTORY"))
	if err != nil {
		return nil, nil, err
	}
	path := os.Getenv("STUDIO_LOG_DIRECTORY")
	if path == "" {
		path = filepath.Join(filepath.Dir(snapshotPath), database+"-logs")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) == filepath.Clean(snapshotPath) {
		return nil, nil, errors.New("separate absolute log journal directory required")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, errors.New("log journal parent directory unavailable")
	}
	runner, err := backup.NewLogRunner(startup, db, storage.LocalBlobs{Root: blobs}, cfg.FormatDSN(), path)
	if err != nil {
		return nil, nil, errors.New("log collector unavailable; verify its scoped account and repository")
	}
	running, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(running, func(view backup.LogRecoveryView, err error) {
			if err != nil && running.Err() == nil {
				log.Print(logProtectionFailure(view, err))
			}
		})
	}()
	ok = true
	return runner, func() { stop(); <-done; _ = runner.Close(); _ = db.Close() }, nil
}

func logProtectionFailure(view backup.LogRecoveryView, err error) string {
	if details, ok := binlog.ReadFailureInfo(err); ok {
		return fmt.Sprintf("Automatic log protection needs attention: %s code=%s stage=%s position=%s:%d event=%d", view.State, details.Code, details.Stage, details.Position.File, details.Position.Offset, details.EventType)
	}
	// Never format arbitrary errors: parser/driver errors can contain payloads.
	return fmt.Sprintf("Automatic log protection needs attention: %s code=CAPTURE_UNCLASSIFIED", view.State)
}
