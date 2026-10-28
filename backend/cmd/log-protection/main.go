package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/backup"
)

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("log-protection", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "initialized source database")
	authority := flags.String("schema", "studio_authority", "existing protected provenance schema")
	credential := flags.String("credential-file", "", "dedicated CurrentUser DPAPI file")
	ready := flags.String("ready-file", "", "public setup metadata")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("invalid log reader setup arguments")
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_LOG_ADMIN_DSN"))
	if err != nil || cfg.User == "" || cfg.Net != "tcp" {
		return errors.New("local log reader administrator connection is unavailable")
	}
	cfg.DBName, cfg.MultiStatements, cfg.AllowAllFiles = "", false, false
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 15*time.Second, 15*time.Second
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return errors.New("local administrator connection is invalid")
	}
	defer admin.Close()
	sourceConfig := cfg.Clone()
	sourceConfig.DBName = *source
	db, err := sql.Open("mysql", sourceConfig.FormatDSN())
	if err != nil {
		return errors.New("local source connection is invalid")
	}
	defer db.Close()
	result, err := backup.ProvisionLogReader(ctx, admin, db, backup.ReaderProvisionOptions{Database: *source, AuthoritySchema: *authority, Address: cfg.Addr, CredentialFile: *credential, ReadyFile: *ready})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// run returns only errors from our sanitized setup boundary.
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
