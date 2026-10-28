package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/storage"
)

var restoreName = regexp.MustCompile(`^studio_restore_[a-f0-9]{32}$`)

type RestoreOptions struct {
	ArchivePath string
	TargetDSN   string
	BlobRoot    string
	Authority   CurrentAuthority
}

func targetConfig(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid target DSN")
	}
	if !restoreName.MatchString(cfg.DBName) || cfg.MultiStatements || cfg.AllowAllFiles {
		return nil, errors.New("target must be a new isolated studio_restore_<32 lowercase hex> database")
	}
	cfg.ParseTime = false
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// DATETIME source bytes are inserted literally. New migration/revocation
	// timestamps must use UTC regardless of the caller's DSN/session default.
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg, nil
}

// Restore validates every byte and reference before creating any destination.
// It only creates a new database, never accepts an existing one, and never starts
// an API or worker. A failed restore leaves its disposable target for inspection.
func Restore(ctx context.Context, opt RestoreOptions) (Metrics, error) {
	started := time.Now()
	m := Metrics{RecoveryBoundary: recoveryBoundary}
	cfg, err := targetConfig(opt.TargetDSN)
	if err != nil {
		return m, err
	}
	if opt.Authority == nil {
		return m, errors.New("current revocation authority is mandatory")
	}
	if !filepath.IsAbs(opt.BlobRoot) {
		return m, errors.New("absolute new blob root required")
	}
	if _, err = os.Lstat(opt.BlobRoot); err == nil || !os.IsNotExist(err) {
		return m, errors.New("blob target already exists or inaccessible")
	}
	// The parent must exist and cannot be a link/junction redirected elsewhere.
	parent := filepath.Dir(opt.BlobRoot)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return m, err
	}
	if !strings.EqualFold(filepath.Clean(parent), filepath.Clean(resolved)) {
		return m, errors.New("linked blob target parent rejected")
	}
	f, err := os.Open(opt.ArchivePath)
	if err != nil {
		return m, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return m, err
	}
	a, err := ValidateArchive(f, stat.Size())
	if err != nil {
		return m, err
	}
	m.SnapshotAt = a.Manifest.SnapshotAt
	state, err := opt.Authority.Current(ctx, a.Manifest.Source)
	if err != nil {
		return m, errors.New("current revocation authority unavailable")
	}
	if err = checkAuthority(a, state); err != nil {
		return m, err
	}
	adminCfg := cfg.Clone()
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		return m, err
	}
	defer admin.Close()
	// No IF NOT EXISTS: even an empty pre-existing database is refused.
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+cfg.DBName+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		return m, errors.New("isolated database creation refused")
	}
	m.TargetDatabase = cfg.DBName
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return m, err
	}
	defer db.Close()
	if err = storage.RunMigrationsTo(ctx, db, supportedSchemaVersion); err != nil {
		return m, err
	}
	catalog := tableCatalog
	if hasRetentionSchema(a.Manifest.Schema) {
		if err = installRetentionRestoreSchema(ctx, db); err != nil {
			return m, err
		}
		if a.Manifest.Schema >= 37 {
			if err = storage.RunMigrationsTo(ctx, db, a.Manifest.Schema); err != nil {
				return m, err
			}
		}
		catalog, err = snapshotCatalog(a.Manifest.Schema)
		if err != nil {
			return m, err
		}
	}
	if err = os.Mkdir(opt.BlobRoot, 0700); err != nil {
		return m, err
	}
	marker := filepath.Join(opt.BlobRoot, "RESTORE_INCOMPLETE")
	if err = os.WriteFile(marker, []byte(recoveryBoundary), 0600); err != nil {
		return m, err
	}
	blobs := storage.LocalBlobs{Root: opt.BlobRoot}
	for path, raw := range a.Blobs {
		parts := strings.Split(path, "/")
		ref, err := blobs.PutObject(ctx, parts[1], bytes.NewReader(raw))
		if err != nil {
			return m, err
		}
		if ref.Hash != parts[2] {
			return m, errors.New("restored blob mismatch")
		}
		m.ObjectBytes += ref.Size
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	sanitizeArchiveData(a.Data, a.Manifest.Schema)
	if hasRetentionSchema(a.Manifest.Schema) {
		invalidateRestoredInventory(a.Data)
	}
	for _, s := range catalog {
		for _, r := range a.Data[s.name] {
			args := make([]any, len(r))
			for i, cell := range r {
				if cell != nil {
					args[i] = cell
				}
			}
			query := "INSERT INTO " + s.name + "(" + s.columns + ") VALUES(" + strings.TrimSuffix(strings.Repeat("?,", len(r)), ",") + ")"
			if _, err = tx.ExecContext(ctx, query, args...); err != nil {
				return m, fmt.Errorf("restore table %s: %w", s.name, err)
			}
			m.RowCount++
		}
	}
	// Read again after copying. Later revocations during a long copy are included.
	// Target accounts remain disabled, so this is not authorization to serve it.
	state, err = opt.Authority.Current(ctx, a.Manifest.Source)
	if err != nil {
		return m, errors.New("final revocation authority unavailable")
	}
	if err = checkAuthority(a, state); err != nil {
		return m, err
	}
	if err = replayRevocations(ctx, tx, state, a.Manifest.Schema); err != nil {
		return m, err
	}
	if hasRetentionSchema(a.Manifest.Schema) {
		if err = validateRestoredRetentionState(ctx, tx, a); err != nil {
			return m, err
		}
	}
	if err = tx.Commit(); err != nil {
		return m, err
	}
	m.AuthorityCheckedAt = state.CheckedAt
	m.ObjectCount = len(a.Blobs)
	m.ElapsedMS = time.Since(started).Milliseconds()
	if err = os.Rename(marker, filepath.Join(opt.BlobRoot, "RESTORED_ISOLATED_REBIND_REQUIRED")); err != nil {
		return m, err
	}
	return m, nil
}
