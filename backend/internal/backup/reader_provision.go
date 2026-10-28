package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/readercredentials"
)

type ReaderProvisionOptions struct {
	Database        string
	AuthoritySchema string
	Address         string
	CredentialFile  string
	ReadyFile       string
}

var readerGeneratedPassword = regexp.MustCompile(`^aA7![A-Za-z0-9_-]{64}$`)

type ReaderProvisionReady struct {
	readercredentials.Identity
	Status           string    `json:"status"`
	SchemaVersion    int       `json:"schema_version"`
	TableFingerprint string    `json:"table_fingerprint"`
	CredentialFile   string    `json:"credential_file"`
	CredentialSHA256 string    `json:"credential_sha256"`
	VerifiedAt       time.Time `json:"verified_at"`
}

// No raw driver error, query text or DSN escapes this administrative boundary.
func readerSetupError(stage string, err error) error {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		return fmt.Errorf("log reader setup %s failed (MySQL %d)", stage, mysqlError.Number)
	}
	return fmt.Errorf("log reader setup %s failed", stage)
}

func readerAddressValid(address string) bool {
	host, port, err := net.SplitHostPort(address)
	number, parseErr := strconv.Atoi(port)
	return err == nil && parseErr == nil && host == "127.0.0.1" && number > 0 && number <= 65535
}

func readerConfig(identity readercredentials.Identity, address, password string) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = identity.User, password, "tcp", address, identity.Database
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	_ = cfg.Apply(mysql.Charset("utf8mb4", ""))
	return cfg
}

func validatedReaderConfig(secret readercredentials.Secret, address string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(secret.DSN())
	if err != nil || !readerGeneratedPassword.MatchString(cfg.Passwd) {
		return nil, errReaderScope
	}
	if cfg.FormatDSN() != readerConfig(secret.Identity, address, cfg.Passwd).FormatDSN() {
		return nil, errReaderScope
	}
	return cfg, nil
}

func pendingReader(ctx context.Context, secret readercredentials.Secret, address string, schema int) (*sql.DB, error) {
	cfg, err := validatedReaderConfig(secret, address)
	if err != nil {
		return nil, errReaderScope
	}
	// A pending USAGE-only account cannot select the source database yet.
	cfg.DBName = ""
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errReaderScope
	}
	db.SetMaxOpenConns(1)
	var uuid, account, mandatory, active string
	if err = db.QueryRowContext(ctx, "SELECT LOWER(@@server_uuid),CURRENT_USER(),@@GLOBAL.mandatory_roles,CURRENT_ROLE()").Scan(&uuid, &account, &mandatory, &active); err != nil || uuid != secret.ServerUUID || account != secret.User+"@127.0.0.1" || mandatory != "" || active != "NONE" {
		db.Close()
		return nil, errReaderScope
	}
	rows, err := db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		db.Close()
		return nil, errReaderScope
	}
	var grants []string
	for rows.Next() {
		var grant string
		if rows.Scan(&grant) != nil {
			rows.Close()
			db.Close()
			return nil, errReaderScope
		}
		grants = append(grants, grant)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(grants) == 0 || auditReaderGrants(grants, secret.User, readerGrantTables(secret.Database, secret.AuthoritySchema, schema), false) != nil {
		db.Close()
		return nil, errReaderScope
	}
	return db, nil
}

// ProvisionLogReader changes only the dedicated account's privileges. The
// source and protected authority must already have valid provenance. DCL is
// implicitly committed, so pending encrypted credentials are retained on error.
func ProvisionLogReader(ctx context.Context, admin, source *sql.DB, options ReaderProvisionOptions) (ReaderProvisionReady, error) {
	var ready ReaderProvisionReady
	if admin == nil || source == nil || !readerIdentifier.MatchString(options.Database) || !readerIdentifier.MatchString(options.AuthoritySchema) || options.Database == options.AuthoritySchema || !readerAddressValid(options.Address) || options.CredentialFile == "" || options.ReadyFile == "" || strings.EqualFold(filepath.Clean(options.CredentialFile), filepath.Clean(options.ReadyFile)) {
		return ready, readerSetupError("configuration", nil)
	}
	conn, err := admin.Conn(ctx)
	if err != nil {
		return ready, readerSetupError("connection", err)
	}
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var uuid string
	if err = conn.QueryRowContext(ctx, "SELECT LOWER(@@server_uuid)").Scan(&uuid); err != nil {
		return ready, readerSetupError("identity", err)
	}
	user, err := LogReaderAccountName(uuid, options.Database)
	if err != nil {
		return ready, readerSetupError("identity", err)
	}
	for _, lock := range []string{options.Database + ":schema", "log-reader:" + user} {
		var acquired int
		if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,10)", lock).Scan(&acquired); err != nil || acquired != 1 {
			return ready, readerSetupError("administrative lock", err)
		}
	}
	actual, err := sourceIdentity(ctx, source)
	if err != nil || strings.ToLower(actual.ServerUUID) != uuid || actual.Database != options.Database {
		return ready, readerSetupError("source identity", err)
	}
	schema, err := qualifiedLogSourceSchema(ctx, source)
	if err != nil {
		return ready, readerSetupError("source schema", err)
	}
	ledger := authority.Ledger{Schema: options.AuthoritySchema}
	if _, err = ledger.ProvenanceTx(ctx, source); err != nil {
		return ready, readerSetupError("protected provenance", err)
	}
	if err = requireLogSource(ctx, conn); err != nil {
		return ready, readerSetupError("source log configuration", err)
	}
	var hosts, local int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(Host='127.0.0.1'),0) FROM mysql.user WHERE User=?", user).Scan(&hosts, &local); err != nil || hosts > 1 || hosts != local {
		return ready, readerSetupError("account ownership", err)
	}
	identity := readercredentials.Identity{Format: 1, Database: options.Database, ServerUUID: uuid, User: user, Host: "127.0.0.1", AuthoritySchema: options.AuthoritySchema, GrantProfile: LogReaderGrantProfile}
	secret, loadErr := readercredentials.Load(options.CredentialFile)
	if loadErr != nil {
		if hosts != 0 {
			return ready, readerSetupError("existing account requires its protected credential", nil)
		}
		if _, statErr := os.Lstat(options.CredentialFile); !errors.Is(statErr, os.ErrNotExist) {
			return ready, readerSetupError("protected credential", nil)
		}
		random := make([]byte, 48)
		if _, err = rand.Read(random); err != nil {
			return ready, readerSetupError("credential creation", nil)
		}
		password := "aA7!" + base64.RawURLEncoding.EncodeToString(random)
		clear(random)
		secret = readercredentials.New(identity, readerConfig(identity, options.Address, password).FormatDSN())
		if _, err = readercredentials.Create(options.CredentialFile, secret); err != nil {
			return ready, readerSetupError("credential persistence", nil)
		}
	} else if secret.Identity != identity {
		return ready, readerSetupError("credential identity", nil)
	}
	readerConfig, err := validatedReaderConfig(secret, options.Address)
	if err != nil {
		return ready, readerSetupError("credential configuration", nil)
	}
	// READY must not survive any partial privilege update or failed re-audit.
	if err = readercredentials.RemoveReady(options.ReadyFile); err != nil {
		return ready, readerSetupError("ready metadata", nil)
	}
	account := "'" + user + "'@'127.0.0.1'"
	if hosts == 0 {
		// CREATE USER does not accept an authentication-string parameter marker.
		// Only our fixed generated alphabet is interpolated: no quote or slash
		// can occur, independently of the session's backslash escape mode.
		if _, err = conn.ExecContext(ctx, "CREATE USER "+account+" IDENTIFIED BY '"+readerConfig.Passwd+"'"); err != nil {
			return ready, readerSetupError("account creation", err)
		}
	}
	pending, err := pendingReader(ctx, secret, options.Address, schema)
	if err != nil {
		return ready, readerSetupError("account authentication and scope", nil)
	}
	pending.Close()
	if _, err = conn.ExecContext(ctx, "GRANT FLUSH_TABLES, REPLICATION CLIENT, REPLICATION SLAVE ON *.* TO "+account); err != nil {
		return ready, readerSetupError("global privilege grant", err)
	}
	for _, table := range readerGrantTables(options.Database, options.AuthoritySchema, schema) {
		if _, err = conn.ExecContext(ctx, "GRANT SELECT ON `"+table.schema+"`.`"+table.name+"` TO "+account); err != nil {
			return ready, readerSetupError("table privilege grant", err)
		}
	}
	reader, err := sql.Open("mysql", secret.DSN())
	if err != nil {
		return ready, readerSetupError("reader connection", nil)
	}
	defer reader.Close()
	if err = VerifyLogReaderScope(ctx, reader, options.AuthoritySchema); err != nil {
		return ready, readerSetupError("final scope verification", nil)
	}
	readerSchema, err := qualifiedLogSourceSchema(ctx, reader)
	if err != nil || readerSchema != schema {
		return ready, readerSetupError("reader schema verification", nil)
	}
	if _, err = ledger.ProvenanceTx(ctx, reader); err != nil {
		return ready, readerSetupError("reader provenance verification", nil)
	}
	hash, err := readercredentials.CipherSHA256(options.CredentialFile)
	if err != nil {
		return ready, readerSetupError("credential pairing", nil)
	}
	ready = ReaderProvisionReady{Identity: identity, Status: "READY", SchemaVersion: schema, TableFingerprint: readerTableFingerprint(options.Database, options.AuthoritySchema, schema), CredentialFile: filepath.Base(options.CredentialFile), CredentialSHA256: hash, VerifiedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(ready, "", "  ")
	if err != nil || readercredentials.PublishReady(options.ReadyFile, raw) != nil {
		return ReaderProvisionReady{}, readerSetupError("ready publication", nil)
	}
	return ready, nil
}
