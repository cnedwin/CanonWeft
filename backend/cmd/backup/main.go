package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/storage"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("use create, verify or restore")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	archive := fs.String("archive", "", "archive path (new file for create)")
	sourceEnv := fs.String("source-env", "STUDIO_DSN", "environment variable containing source DSN")
	authorityEnv := fs.String("authority-env", "STUDIO_AUTHORITY_DSN", "optional independent protected authority connection environment")
	targetEnv := fs.String("target-env", "STUDIO_RESTORE_DSN", "environment variable containing isolated target DSN")
	blobRoot := fs.String("blobs", os.Getenv("STUDIO_BLOB_ROOT"), "source blob root for create; NEW absolute target root for restore")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *archive == "" || fs.NArg() != 0 {
		return errors.New("archive path required; positional arguments not accepted")
	}
	if args[0] == "verify" {
		f, err := os.Open(*archive)
		if err != nil {
			return err
		}
		defer f.Close()
		s, err := f.Stat()
		if err != nil {
			return err
		}
		a, err := backup.ValidateArchive(f, s.Size())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"verified": true, "schema": a.Manifest.Schema, "snapshot_at": a.Manifest.SnapshotAt, "objects": len(a.Blobs), "authority_checked": false})
	}
	if args[0] != "create" && args[0] != "restore" {
		return errors.New("unknown command")
	}
	sourceDSN := os.Getenv(*sourceEnv)
	if args[0] == "restore" && os.Getenv(*authorityEnv) != "" {
		sourceDSN = os.Getenv(*authorityEnv)
	}
	if sourceDSN == "" {
		return errors.New("source or protected authority connection environment is empty")
	}
	cfg, err := mysql.ParseDSN(sourceDSN)
	if err != nil {
		return errors.New("invalid source DSN")
	}
	cfg.ParseTime = false
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	// Restoring must still work when the source business database is gone.
	// This connection reads only the separately provisioned protected ledger.
	if args[0] == "restore" {
		cfg.DBName = ""
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return errors.New("source connection configuration failed")
	}
	defer db.Close()
	var metrics backup.Metrics
	if args[0] == "create" {
		metrics, err = backup.Create(ctx, db, storage.LocalBlobs{Root: *blobRoot}, *archive)
	} else {
		schema := os.Getenv("STUDIO_AUTHORITY_SCHEMA")
		if schema == "" {
			schema = "studio_authority"
		}
		metrics, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: *archive, TargetDSN: os.Getenv(*targetEnv), BlobRoot: *blobRoot, Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: schema}}})
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(metrics)
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) {
			fmt.Fprintf(os.Stderr, "database operation failed (MySQL code %d)\n", mysqlErr.Number)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
