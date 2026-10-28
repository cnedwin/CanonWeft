package integration

import (
	"context"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"strings"
	"testing"
)

// Explicit opt-in for local shared-checkout development: these HTTP tests must
// not clear another test worker's studio_test history. No production entry uses
// this administrator connection or creates these disposable schemas.
func disposableHTTPDatabase(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.User != "root" || cfg.Net != "tcp" || (cfg.Addr != "127.0.0.1:13306" && cfg.Addr != "127.0.0.1:13316" && cfg.Addr != "127.0.0.1:13317") {
		t.Fatal("explicit local disposable HTTP database configuration required")
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open disposable administrator connection")
	}
	t.Cleanup(func() { admin.Close() })
	business, ledger := "studio_http_test_"+service.NewID(), "studio_http_authority_"+service.NewID()
	if _, err = admin.Exec("CREATE DATABASE `" + business + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{business, ledger} {
			if !strings.HasPrefix(name, "studio_http_test_") && !strings.HasPrefix(name, "studio_http_authority_") {
				t.Fatal("unsafe HTTP fixture cleanup")
			}
			if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	cfg.DBName = business
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open disposable HTTP database")
	}
	t.Cleanup(func() { db.Close() })
	if err = storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", ledger)
	if _, err = authority.Provision(context.Background(), admin, ledger, business); err != nil {
		t.Fatal(err)
	}
	return db
}
