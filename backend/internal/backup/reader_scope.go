package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
)

const LogReaderGrantProfile = "log-reader-v1"

var readerIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var readerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var errReaderScope = errors.New("log reader identity or privilege profile is invalid")

type readerTable struct{ schema, name string }

func readerGrantTables(database, authoritySchema string, schema ...int) []readerTable {
	catalog, err := logSchemaCatalog(schema...)
	if err != nil {
		return nil
	}
	tables := make([]readerTable, 0, len(catalog)+1)
	for _, spec := range catalog {
		tables = append(tables, readerTable{database, spec.name})
	}
	return append(tables, readerTable{authoritySchema, "sources"})
}

// LogReaderAccountName binds the local account to a particular server and source.
func LogReaderAccountName(serverUUID, database string) (string, error) {
	if !readerUUID.MatchString(serverUUID) || !readerIdentifier.MatchString(database) {
		return "", errReaderScope
	}
	sum := sha256.Sum256([]byte(serverUUID + "\x00" + database))
	return "backup_" + hex.EncodeToString(sum[:])[:20], nil
}

func readerTableFingerprint(database, authority string, schema ...int) string {
	catalog, err := logSchemaCatalog(schema...)
	if err != nil {
		return ""
	}
	var entries []string
	for _, spec := range catalog {
		entries = append(entries, database+"."+spec.name+":"+spec.columns)
	}
	entries = append(entries, authority+".sources:SELECT")
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(LogReaderGrantProfile + "\n" + strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

// auditReaderGrants accepts only MySQL's explicit self-grant representation.
// Unknown syntax, role edges, column grants and grant options fail closed.
func auditReaderGrants(grants []string, user string, tables []readerTable, complete bool) error {
	if len(tables) == 0 {
		return errReaderScope
	}
	allowed := map[string]bool{}
	for _, table := range tables {
		allowed["`"+table.schema+"`.`"+table.name+"`"] = true
	}
	global := map[string]bool{"FLUSH_TABLES": false, "REPLICATION CLIENT": false, "REPLICATION SLAVE": false}
	seen := map[string]bool{}
	suffix := " TO `" + user + "`@`127.0.0.1`"
	for _, grant := range grants {
		if !strings.HasPrefix(grant, "GRANT ") || !strings.HasSuffix(grant, suffix) {
			return errReaderScope
		}
		body := strings.TrimSuffix(strings.TrimPrefix(grant, "GRANT "), suffix)
		parts := strings.Split(body, " ON ")
		if len(parts) != 2 {
			return errReaderScope
		}
		if parts[1] == "*.*" {
			for _, privilege := range strings.Split(parts[0], ", ") {
				if privilege == "USAGE" && parts[0] == "USAGE" {
					continue
				}
				if _, ok := global[privilege]; !ok {
					return errReaderScope
				}
				global[privilege] = true
			}
		} else {
			if parts[0] != "SELECT" || !allowed[parts[1]] {
				return errReaderScope
			}
			seen[parts[1]] = true
		}
	}
	if complete {
		if len(seen) != len(allowed) {
			return errReaderScope
		}
		for _, present := range global {
			if !present {
				return errReaderScope
			}
		}
	}
	return nil
}

func verifyReaderScope(ctx context.Context, db *sql.DB, authority string, complete bool) error {
	if db == nil || !readerIdentifier.MatchString(authority) {
		return errReaderScope
	}
	var uuid, database, account, mandatoryRoles, activeRoles string
	if err := db.QueryRowContext(ctx, "SELECT LOWER(@@server_uuid),DATABASE(),CURRENT_USER(),@@GLOBAL.mandatory_roles,CURRENT_ROLE()").Scan(&uuid, &database, &account, &mandatoryRoles, &activeRoles); err != nil {
		return errReaderScope
	}
	user, err := LogReaderAccountName(uuid, database)
	if err != nil || account != user+"@127.0.0.1" || mandatoryRoles != "" || activeRoles != "NONE" || authority == database {
		return errReaderScope
	}
	schema, err := qualifiedLogSourceSchema(ctx, db)
	if err != nil {
		return errReaderScope
	}
	rows, err := db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return errReaderScope
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grant string
		if rows.Scan(&grant) != nil {
			return errReaderScope
		}
		grants = append(grants, grant)
	}
	if rows.Err() != nil || len(grants) == 0 {
		return errReaderScope
	}
	return auditReaderGrants(grants, user, readerGrantTables(database, authority, schema), complete)
}

// Only current source schemas may be provisioned. Historical replay support
// does not grant authority to connect a live source with an obsolete catalog.
func qualifiedLogSourceSchema(ctx context.Context, q queryer) (int, error) {
	var version int
	var checksum string
	var initialized bool
	if err := q.QueryRowContext(ctx, "SELECT version,checksum,history_initialized FROM schema_version WHERE id=1").Scan(&version, &checksum, &initialized); err != nil {
		return 0, errReaderScope
	}
	history, err := readHistory(ctx, q, version)
	if err != nil || !initialized || validateMigrationHistory(version, history) != nil {
		return 0, errReaderScope
	}
	if len(history) == 0 || checksum != history[len(history)-1].SHA256 {
		return 0, errReaderScope
	}
	if err := checkTables(ctx, q, version); err != nil {
		return 0, errReaderScope
	}
	return version, nil
}

// VerifyLogReaderScope audits the current authenticated account, including
// inactive role assignments. It never grants privileges or changes source data.
// Generic low-level log runners remain reusable; production startup calls this.
func VerifyLogReaderScope(ctx context.Context, db *sql.DB, authoritySchema string) error {
	return verifyReaderScope(ctx, db, authoritySchema, true)
}
