package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"io"
	"novelstudio/internal/authority"
	"novelstudio/internal/storage"
	"os"
	"regexp"
	"time"
)

type options struct {
	source, schema, user, adminEnv string
	stopped                        bool
}

var localName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("authority", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.source, "source", "", "business database")
	fs.StringVar(&o.schema, "schema", "studio_authority", "protected authority database")
	fs.StringVar(&o.user, "app-user", "", "local application MySQL account")
	fs.StringVar(&o.adminEnv, "admin-env", "STUDIO_AUTHORITY_ADMIN_DSN", "environment variable containing admin DSN")
	fs.BoolVar(&o.stopped, "services-stopped", false, "confirm source API and workers stopped")
	if err := fs.Parse(args); err != nil {
		return o, errors.New("invalid provisioning arguments")
	}
	if !o.stopped || fs.NArg() != 0 || !localName.MatchString(o.source) || !localName.MatchString(o.schema) || !localName.MatchString(o.user) || o.user == "root" || o.source == o.schema {
		return o, errors.New("stopped services, distinct safe database names and a non-root local application account are required")
	}
	return o, nil
}
func validateArgs(args []string) error { _, err := parseArgs(args); return err }

func verifyNarrowGrants(ctx context.Context, db *sql.DB, schema, user string) error {
	grantee := "'" + user + "'@'localhost'"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"SELECT COUNT(*) FROM information_schema.user_privileges WHERE GRANTEE=? AND (PRIVILEGE_TYPE<>'USAGE' OR IS_GRANTABLE='YES')", []any{grantee}},
		// Dynamic global privileges (for example ROLE_ADMIN) are stored separately
		// from the static privileges exposed by USER_PRIVILEGES.
		{"SELECT COUNT(*) FROM mysql.global_grants WHERE USER=? AND HOST='localhost'", []any{user}},
		// Database GRANT scopes use LIKE wildcards and escaped literal names.
		// Conservatively reject potential overlap even when partial_revokes makes
		// wildcards literal: ambiguity must not turn into rewrite permission after
		// a server-mode change. Explicit ESCAPE also handles NO_BACKSLASH_ESCAPES.
		{"SELECT COUNT(*) FROM information_schema.schema_privileges WHERE GRANTEE=? AND (TABLE_SCHEMA=? OR ? LIKE TABLE_SCHEMA ESCAPE ?)", []any{grantee, schema, schema, "\\"}},
		{"SELECT COUNT(*) FROM information_schema.table_privileges WHERE GRANTEE=? AND TABLE_SCHEMA=? AND (IS_GRANTABLE='YES' OR NOT ((TABLE_NAME='sources' AND PRIVILEGE_TYPE='SELECT') OR (TABLE_NAME='events' AND PRIVILEGE_TYPE IN ('SELECT','INSERT'))))", []any{grantee, schema}},
		{"SELECT COUNT(*) FROM information_schema.column_privileges WHERE GRANTEE=? AND TABLE_SCHEMA=?", []any{grantee, schema}},
		{"SELECT COUNT(*) FROM mysql.role_edges WHERE TO_USER=? AND TO_HOST='localhost'", []any{user}},
		// Mandatory roles are implicitly granted and absent from role_edges.
		{"SELECT LENGTH(TRIM(@@GLOBAL.mandatory_roles))", nil},
	} {
		var count int
		if err := db.QueryRowContext(ctx, q.sql, q.args...).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("application account has broader authority privileges or inherited roles; provisioning refused")
		}
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	o, err := parseArgs(args)
	if err != nil {
		return err
	}
	cfg, err := mysql.ParseDSN(os.Getenv(o.adminEnv))
	if err != nil || cfg.User == "" {
		return errors.New("administrator connection environment is missing or invalid")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return errors.New("administrator connection unavailable")
	}
	defer admin.Close()
	if err = verifyNarrowGrants(ctx, admin, o.schema, o.user); err != nil {
		return err
	}
	sourceCfg := cfg.Clone()
	sourceCfg.DBName = o.source
	source, err := sql.Open("mysql", sourceCfg.FormatDSN())
	if err != nil {
		return errors.New("source connection unavailable")
	}
	defer source.Close()
	if err = storage.RunMigrations(ctx, source); err != nil {
		return err
	}
	ledger, err := authority.Provision(ctx, admin, o.schema, o.source)
	if err != nil {
		return err
	}
	// All interpolated identifiers passed the restricted ASCII grammar above.
	for _, grant := range []string{
		"GRANT SELECT ON `" + o.schema + "`.sources TO '" + o.user + "'@'localhost'",
		"GRANT SELECT, INSERT ON `" + o.schema + "`.events TO '" + o.user + "'@'localhost'",
	} {
		if _, err = admin.ExecContext(ctx, grant); err != nil {
			return err
		}
	}
	if err = verifyNarrowGrants(ctx, admin, o.schema, o.user); err != nil {
		return err
	}
	var serverUUID string
	if err = admin.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&serverUUID); err != nil {
		return err
	}
	state, err := ledger.Current(ctx, admin, authority.Source{ServerUUID: serverUUID, Database: o.source})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"source": o.source, "schema": o.schema, "ledger_id": state.LedgerID, "protocol": state.Protocol, "application_schema": storage.SchemaVersion})
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		var dbErr *mysql.MySQLError
		if errors.As(err, &dbErr) {
			fmt.Fprintf(os.Stderr, "authority provisioning failed (MySQL code %d)\n", dbErr.Number)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
