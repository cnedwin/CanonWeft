package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func narrowGrantFixture(t *testing.T) (*sql.DB, *sql.DB, string, string) {
	t.Helper()
	dsn := os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("STUDIO_AUTHORITY_TEST_ADMIN_DSN required for isolated account tests")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.User == "" {
		t.Fatal("invalid isolated administrator configuration")
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(b)
	schema := "studio_authority_test_" + suffix
	user := "authguard_" + suffix[:22]
	password := suffix + suffix
	if _, err = admin.Exec("CREATE DATABASE `" + schema + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if localName.MatchString(schema) && len(strings.TrimPrefix(schema, "studio_authority_test_")) == 32 {
			if _, err := admin.Exec("DROP DATABASE `" + schema + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err = admin.Exec("CREATE USER '" + user + "'@'localhost' IDENTIFIED BY '" + password + "'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP USER '" + user + "'@'localhost'"); err != nil {
			t.Error(err)
		}
	})
	for _, statement := range []string{
		"CREATE TABLE `" + schema + "`.events(reason TEXT) ENGINE=InnoDB",
		"CREATE TABLE `" + schema + "`.sources(protocol INT) ENGINE=InnoDB",
		"INSERT INTO `" + schema + "`.events VALUES('original')",
		"GRANT SELECT,INSERT ON `" + schema + "`.events TO '" + user + "'@'localhost'",
		"GRANT SELECT ON `" + schema + "`.sources TO '" + user + "'@'localhost'",
	} {
		if _, err = admin.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	cfg.User = user
	cfg.Passwd = password
	app, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return admin, app, schema, user
}

func TestNarrowGrantsAcceptsOnlyNonDelegableAppendPrivileges(t *testing.T) {
	admin, app, schema, user := narrowGrantFixture(t)
	if err := verifyNarrowGrants(context.Background(), admin, schema, user); err != nil {
		t.Fatal("narrow account refused", err)
	}
	if _, err := app.Exec("INSERT INTO `" + schema + "`.events VALUES('append')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := app.QueryRow("SELECT COUNT(*) FROM `" + schema + "`.events").Scan(&count); err != nil || count != 2 {
		t.Fatal("append/read unavailable", count, err)
	}
	if _, err := app.Exec("UPDATE `" + schema + "`.events SET reason='changed'"); err == nil {
		t.Fatal("narrow account can rewrite")
	}
	// A literal unrelated database grant must not disable normal app provisioning.
	if _, err := admin.Exec("GRANT SELECT ON `unrelated_" + strings.TrimPrefix(schema, "studio_authority_test_") + "`.* TO '" + user + "'@'localhost'"); err != nil {
		t.Fatal(err)
	}
	if err := verifyNarrowGrants(context.Background(), admin, schema, user); err != nil {
		t.Fatal("unrelated scope refused", err)
	}
}

func TestNarrowGrantsRejectsColumnWildcardAndGrantOption(t *testing.T) {
	for _, kind := range []string{"column-update", "percent-schema", "underscore-schema", "escaped-schema", "table-grant-option"} {
		t.Run(kind, func(t *testing.T) {
			admin, app, schema, user := narrowGrantFixture(t)
			grant := ""
			switch kind {
			case "column-update":
				grant = "GRANT UPDATE(reason) ON `" + schema + "`.events"
			case "percent-schema":
				grant = "GRANT UPDATE ON `" + schema[:len(schema)-4] + "%`.*"
			case "underscore-schema":
				grant = "GRANT UPDATE ON `" + schema[:len(schema)-1] + "_`.*"
			case "escaped-schema":
				grant = "GRANT UPDATE ON `" + strings.ReplaceAll(schema, "_", `\_`) + "`.*"
			case "table-grant-option":
				grant = "GRANT SELECT,INSERT ON `" + schema + "`.events"
			}
			grant += " TO '" + user + "'@'localhost'"
			if kind == "table-grant-option" {
				grant += " WITH GRANT OPTION"
			}
			if _, err := admin.Exec(grant); err != nil {
				t.Fatal(err)
			}
			var partialRevokes int
			if err := admin.QueryRow("SELECT @@GLOBAL.partial_revokes").Scan(&partialRevokes); err != nil {
				t.Fatal(err)
			}
			if kind == "column-update" || (partialRevokes == 0 && kind != "table-grant-option") {
				if _, err := app.Exec("UPDATE `" + schema + "`.events SET reason='bypass demonstrated'"); err != nil {
					t.Fatal("fixture did not reproduce effective write authority", err)
				}
			}
			if err := verifyNarrowGrants(context.Background(), admin, schema, user); err == nil {
				t.Fatal("unsafe authority grant accepted", kind)
			}
		})
	}
}

func TestNarrowGrantsRejectsInheritedRoles(t *testing.T) {
	admin, _, schema, user := narrowGrantFixture(t)
	role := "authrole_" + strings.TrimPrefix(schema, "studio_authority_test_")[:22]
	if _, err := admin.Exec("CREATE ROLE '" + role + "'@'localhost'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP ROLE '" + role + "'@'localhost'"); err != nil {
			t.Error(err)
		}
	})
	if _, err := admin.Exec("GRANT UPDATE ON `" + schema + "`.events TO '" + role + "'@'localhost'"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("GRANT '" + role + "'@'localhost' TO '" + user + "'@'localhost'"); err != nil {
		t.Fatal(err)
	}
	if err := verifyNarrowGrants(context.Background(), admin, schema, user); err == nil {
		t.Fatal("inherited role accepted")
	}
}
