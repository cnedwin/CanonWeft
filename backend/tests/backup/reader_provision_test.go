package backup_test

import (
	"context"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/service"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
	"novelstudio/internal/readercredentials"
)

func TestDedicatedLogReaderProvisionIsIdempotentAndReadOnly(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	var uuid string
	if err := f.admin.QueryRow("SELECT LOWER(@@server_uuid)").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	user, err := backup.LogReaderAccountName(uuid, f.config.DBName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.admin.Exec("DROP USER IF EXISTS '" + user + "'@'127.0.0.1'") })
	options := backup.ReaderProvisionOptions{Database: f.config.DBName, AuthoritySchema: f.ledger.Schema, Address: f.config.Addr, CredentialFile: filepath.Join(f.root, "reader.dpapi"), ReadyFile: filepath.Join(f.root, "reader.ready.json")}
	ready, err := backup.ProvisionLogReader(ctx, f.admin, f.db, options)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := readercredentials.Load(options.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("mysql", secret.DSN())
	if err != nil {
		t.Fatal("reader open failed")
	}
	defer reader.Close()
	if err = backup.VerifyLogReaderScope(ctx, reader, options.AuthoritySchema); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := backup.CreateLogBaseline(ctx, reader, f.blobs, filepath.Join(f.root, "reader-baseline.zip"))
	if err != nil {
		t.Fatal("dedicated reader baseline failed", err)
	}
	execute(t, f.db, "UPDATE projects SET name='reader-log-change' WHERE id=?", f.project)
	var through binlog.Position
	var included, excluded, gtids string
	if err = f.db.QueryRow("SHOW BINARY LOG STATUS").Scan(&through.File, &through.Offset, &included, &excluded, &gtids); err != nil {
		t.Fatal(err)
	}
	stream, err := binlog.NewReader(secret.DSN(), uuid)
	if err != nil {
		t.Fatal("dedicated protocol reader invalid")
	}
	var tables []binlog.TableSchema
	for _, table := range baseline.Tables {
		tables = append(tables, table.Layout)
	}
	changes := 0
	if err = stream.ReadRange(ctx, f.config.DBName, tables, baseline.Position, through, func(tx binlog.Transaction) error { changes += len(tx.Changes); return nil }); err != nil || changes != 1 {
		t.Fatal("dedicated account could not read actual log interval", err)
	}
	for _, query := range []string{"UPDATE projects SET name='forbidden'", "DELETE FROM users", "CREATE TABLE forbidden(id INT)", "SELECT * FROM mysql.user", "UPDATE `" + f.ledger.Schema + "`.sources SET protocol=99", "INSERT INTO `" + f.ledger.Schema + "`.events(ledger_id) VALUES('forbidden')", "GRANT SELECT ON *.* TO CURRENT_USER"} {
		if _, err = reader.ExecContext(ctx, query); err == nil {
			t.Fatal("reader obtained forbidden authority", query)
		}
	}
	again, err := backup.ProvisionLogReader(ctx, f.admin, f.db, options)
	if err != nil || again.CredentialSHA256 != ready.CredentialSHA256 {
		t.Fatal("repeat changed credential", err)
	}
	if _, err = os.Stat(options.ReadyFile); err != nil {
		t.Fatal("verified setup did not publish READY")
	}
}

func TestDedicatedLogReaderResumesAfterCommittedAccountAndFailedGrant(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	var uuid string
	if err := f.admin.QueryRow("SELECT LOWER(@@server_uuid)").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	user, err := backup.LogReaderAccountName(uuid, f.config.DBName)
	if err != nil {
		t.Fatal(err)
	}
	limitedUser := "setup_" + service.NewID()[:20]
	limitedAccount := "'" + limitedUser + "'@'127.0.0.1'"
	t.Cleanup(func() {
		_, _ = f.admin.Exec("DROP USER IF EXISTS '" + user + "'@'127.0.0.1'")
		_, _ = f.admin.Exec("DROP USER IF EXISTS " + limitedAccount)
	})
	execute(t, f.admin, "CREATE USER "+limitedAccount+" IDENTIFIED BY 'SetupTestPassword7!'")
	execute(t, f.admin, "GRANT CREATE USER ON *.* TO "+limitedAccount)
	execute(t, f.admin, "GRANT SELECT ON mysql.user TO "+limitedAccount)
	execute(t, f.admin, "GRANT SELECT ON `"+f.config.DBName+"`.* TO "+limitedAccount)
	execute(t, f.admin, "GRANT SELECT ON `"+f.ledger.Schema+"`.sources TO "+limitedAccount)
	cfg := f.config.Clone()
	cfg.DBName, cfg.User, cfg.Passwd = "", limitedUser, "SetupTestPassword7!"
	limited, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("limited admin fixture invalid")
	}
	defer limited.Close()
	options := backup.ReaderProvisionOptions{Database: f.config.DBName, AuthoritySchema: f.ledger.Schema, Address: f.config.Addr, CredentialFile: filepath.Join(f.root, "reader.dpapi"), ReadyFile: filepath.Join(f.root, "reader.ready.json")}
	if _, err = backup.ProvisionLogReader(ctx, limited, f.db, options); err == nil || !strings.Contains(err.Error(), "global privilege grant") {
		t.Fatal("fixture did not fail after CREATE USER committed", err)
	}
	var accounts int
	if err = f.admin.QueryRow("SELECT COUNT(*) FROM mysql.user WHERE User=? AND Host='127.0.0.1'", user).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatal("CREATE USER did not survive failure", err)
	}
	if _, err = os.Stat(options.ReadyFile); !os.IsNotExist(err) {
		t.Fatal("partial setup published READY")
	}
	hash, err := readercredentials.CipherSHA256(options.CredentialFile)
	if err != nil {
		t.Fatal("committed account lost encrypted password")
	}
	ready, err := backup.ProvisionLogReader(ctx, f.admin, f.db, options)
	if err != nil || ready.CredentialSHA256 != hash {
		t.Fatal("retry reset password instead of reusing pending credential", err)
	}
	secret, err := readercredentials.Load(options.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	wrongConfig, err := mysql.ParseDSN(secret.DSN())
	if err != nil {
		t.Fatal("protected DSN invalid")
	}
	wrongConfig.Passwd = "aA7!" + strings.Repeat("a", 64)
	wrong := filepath.Join(f.root, "wrong.dpapi")
	if _, err = readercredentials.Create(wrong, readercredentials.New(secret.Identity, wrongConfig.FormatDSN())); err != nil {
		t.Fatal(err)
	}
	options.CredentialFile, options.ReadyFile = wrong, filepath.Join(f.root, "wrong.ready.json")
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err == nil {
		t.Fatal("mismatched password took over existing account")
	}
	valid, err := sql.Open("mysql", secret.DSN())
	if err != nil {
		t.Fatal("original connection invalid")
	}
	defer valid.Close()
	if err = valid.PingContext(ctx); err != nil {
		t.Fatal("refusal changed existing password", err)
	}
}

func TestDedicatedLogReaderRefusesUnownedOrExcessPrivileges(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	var uuid string
	if err := f.admin.QueryRow("SELECT LOWER(@@server_uuid)").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	user, err := backup.LogReaderAccountName(uuid, f.config.DBName)
	if err != nil {
		t.Fatal(err)
	}
	account := "'" + user + "'@'127.0.0.1'"
	t.Cleanup(func() { _, _ = f.admin.Exec("DROP USER IF EXISTS " + account) })
	execute(t, f.admin, "CREATE USER "+account+" IDENTIFIED BY 'UnownedPassword7!'")
	options := backup.ReaderProvisionOptions{Database: f.config.DBName, AuthoritySchema: f.ledger.Schema, Address: f.config.Addr, CredentialFile: filepath.Join(f.root, "reader.dpapi"), ReadyFile: filepath.Join(f.root, "reader.ready.json")}
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err == nil {
		t.Fatal("unowned account taken over")
	}
	if _, err = os.Stat(options.CredentialFile); !os.IsNotExist(err) {
		t.Fatal("refusal created credential for unowned account")
	}
	execute(t, f.admin, "DROP USER "+account)
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err != nil {
		t.Fatal(err)
	}
	execute(t, f.admin, "GRANT INSERT ON `"+f.config.DBName+"`.`projects` TO "+account)
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err == nil {
		t.Fatal("extra write privilege accepted")
	}
	if _, err = os.Stat(options.ReadyFile); !os.IsNotExist(err) {
		t.Fatal("unsafe account retained READY")
	}
	secret, err := readercredentials.Load(options.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("mysql", secret.DSN())
	if err != nil {
		t.Fatal("reader connection invalid")
	}
	defer reader.Close()
	if err = backup.VerifyLogReaderScope(ctx, reader, options.AuthoritySchema); err == nil {
		t.Fatal("runtime audit accepted excess privilege")
	}
	if strings.Contains(err.Error(), "UnownedPassword") || strings.Contains(err.Error(), secret.DSN()) {
		t.Fatal("audit error exposed secret")
	}
	execute(t, f.admin, "REVOKE INSERT ON `"+f.config.DBName+"`.`projects` FROM "+account)
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err != nil {
		t.Fatal("explicit admin remediation could not resume", err)
	}
	role := "'reader_role_" + service.NewID()[:20] + "'@'%'"
	execute(t, f.admin, "CREATE ROLE "+role)
	t.Cleanup(func() { _, _ = f.admin.Exec("DROP ROLE IF EXISTS " + role) })
	execute(t, f.admin, "GRANT "+role+" TO "+account)
	if err = backup.VerifyLogReaderScope(ctx, reader, options.AuthoritySchema); err == nil {
		t.Fatal("inactive role assignment accepted")
	}
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err == nil {
		t.Fatal("setup accepted role assignment")
	}
	execute(t, f.admin, "REVOKE "+role+" FROM "+account)
	if _, err = backup.ProvisionLogReader(ctx, f.admin, f.db, options); err != nil {
		t.Fatal("role remediation could not resume", err)
	}
}

// Explicit local setup check only; ordinary random-schema tests never require
// or mutate a pre-existing configured database.
func TestConfiguredDedicatedLogReaderScope(t *testing.T) {
	dsn := os.Getenv("STUDIO_LOG_READER_TEST_DSN")
	if dsn == "" {
		t.Skip("explicit configured reader check not requested")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.DBName != "studio_test" {
		t.Fatal("configured reader check requires studio_test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("configured reader connection unavailable")
	}
	defer db.Close()
	if err = backup.VerifyLogReaderScope(context.Background(), db, os.Getenv("STUDIO_AUTHORITY_SCHEMA")); err != nil {
		t.Fatal(err)
	}
	var source string
	if err = db.QueryRow("SELECT DATABASE()").Scan(&source); err != nil || source != "studio_test" {
		t.Fatal("configured reader source mismatch")
	}
}
