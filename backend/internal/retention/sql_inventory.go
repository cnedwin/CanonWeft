package retention

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type SourceSnapshot struct {
	CapturedAt time.Time
	Hash       string
	Tables     []string
	Index      *SourceIndex
}

func liveSourceTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil || version < 35 || version > 38 {
		return nil, ErrSourceOwnership
	}
	return KnownTablesForSchema(version), nil
}

// ScanSources reads only the compiled schema catalog in the caller's consistent
// transaction. It does not acquire deletion authority, create a cleanup plan,
// register an object age, or return source bytes to a user-facing API.
func ScanSources(ctx context.Context, tx *sql.Tx) (SourceSnapshot, error) {
	if tx == nil {
		return SourceSnapshot{}, ErrSourceOwnership
	}
	if err := verifySQLSourceCatalog(ctx, tx); err != nil {
		return SourceSnapshot{}, err
	}
	snapshot, err := scanSourceRows(ctx, tx, false)
	if err != nil {
		return SourceSnapshot{}, err
	}
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", MigrationName).Scan(&installed); err != nil {
		return SourceSnapshot{}, err
	}
	if installed == 0 {
		return snapshot, nil
	}
	for _, row := range snapshot.Index.Records() {
		if row.Table != "projects" {
			continue
		}
		project := row.Values["id"]
		versions, err := readReferenceVersionsMode(ctx, tx, project, snapshot.CapturedAt, false)
		if err != nil {
			return SourceSnapshot{}, err
		}
		if err := snapshot.Index.bindExpiryVersions(ctx, tx, project, versions, snapshot.CapturedAt, false); err != nil {
			return SourceSnapshot{}, err
		}
		if err := snapshot.Index.validateRetiredRequestCoverage(project); err != nil {
			return SourceSnapshot{}, err
		}
	}
	return snapshot, nil
}

// Registration uses current locking reads, even if the caller previously
// established a repeatable-read snapshot. The normal read-only preview does not.
func scanSourceRows(ctx context.Context, tx *sql.Tx, current bool) (SourceSnapshot, error) {
	var snapshot SourceSnapshot
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return snapshot, ErrSourceOwnership
	}
	var err error
	snapshot.CapturedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return SourceSnapshot{}, ErrSourceOwnership
	}
	var source []SourceRow
	budget := 0
	tables, err := liveSourceTables(ctx, tx)
	if err != nil {
		return SourceSnapshot{}, err
	}
	for _, table := range tables {
		spec, _ := OwnerSpec(table)
		columns := make([]string, len(spec.Columns))
		for i, column := range spec.Columns {
			columns[i] = column.Column
		}
		keys := make([]string, len(spec.PrimaryKey))
		for i, key := range spec.PrimaryKey {
			keys[i] = key.Name
		}
		// Identifiers originate solely from the static reviewed catalog. Neither
		// a saved plan nor a caller can provide SQL names or expressions here.
		query := "SELECT `" + strings.Join(columns, "`,`") + "` FROM `" + table + "` ORDER BY `" + strings.Join(keys, "`,`") + "` LIMIT 250001"
		if current {
			query += " FOR SHARE"
		}
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return SourceSnapshot{}, ErrSourceOwnership
		}
		for rows.Next() {
			if len(source) == 250000 {
				rows.Close()
				return SourceSnapshot{}, ErrSourceOwnership
			}
			cells := make([]sql.RawBytes, len(columns))
			pointers := make([]any, len(columns))
			for i := range pointers {
				pointers[i] = &cells[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return SourceSnapshot{}, ErrSourceOwnership
			}
			row := SourceRow{Table: table, Values: map[string]string{}, Nulls: map[string]bool{}}
			budget += 256
			for i, column := range columns {
				budget += len(column) + len(cells[i]) + 96
				if budget > 64<<20 {
					rows.Close()
					return SourceSnapshot{}, ErrSourceOwnership
				}
				row.Values[column] = string(cells[i])
				if cells[i] == nil {
					row.Nulls[column] = true
				}
			}
			source = append(source, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return SourceSnapshot{}, ErrSourceOwnership
		}
	}
	snapshot.Index, err = NewSourceIndex(source)
	if err != nil {
		return SourceSnapshot{}, err
	}
	snapshot.Tables = tables
	snapshot.Hash = stableHash(struct {
		Protocol int
		Rows     []SourceRow
	}{1, snapshot.Index.Records()})
	return snapshot, nil
}

func verifySQLSourceCatalog(ctx context.Context, tx *sql.Tx) error {
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", MigrationName).Scan(&installed); err != nil {
		return ErrSourceOwnership
	}
	if installed != 0 {
		// A complete, exactly identified registration schema can coexist with
		// the read-only business-source preview. Partial/experimental installs
		// remain errors, and metadata coverage does not permit physical cleanup.
		return verifyRegistrationCatalog(ctx, tx)
	}
	return verifySourceCatalog(ctx, tx, nil)
}

func verifySourceCatalog(ctx context.Context, tx *sql.Tx, metadata map[string][]string) error {
	tables, err := liveSourceTables(ctx, tx)
	if err != nil {
		return err
	}
	want := map[string]bool{"schema_version": true, "schema_migrations": true}
	for _, table := range tables {
		want[table] = true
	}
	for table := range metadata {
		want[table] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,TABLE_TYPE,COALESCE(ENGINE,'') FROM information_schema.tables WHERE table_schema=DATABASE()")
	if err != nil {
		return ErrSourceOwnership
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name, kind, engine string
		// Registration needs transactional source reads and journal writes. Do
		// not rely on a later SQL error or a specific server's GTID configuration
		// to prevent partial writes to nontransactional tables.
		if err := rows.Scan(&name, &kind, &engine); err != nil || !want[name] || seen[name] || kind != "BASE TABLE" || (metadata != nil && engine != "InnoDB") {
			rows.Close()
			return ErrSourceOwnership
		}
		seen[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(seen) != len(want) {
		return ErrSourceOwnership
	}
	rows, err = tx.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY TABLE_NAME,ORDINAL_POSITION")
	if err != nil {
		return ErrSourceOwnership
	}
	columns := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return ErrSourceOwnership
		}
		columns[table] = append(columns[table], column)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ErrSourceOwnership
	}
	for _, table := range tables {
		if err := ValidateColumnCoverage(table, columns[table]); err != nil {
			return ErrSourceOwnership
		}
	}
	for table, expected := range metadata {
		if len(columns[table]) != len(expected) {
			return ErrSourceOwnership
		}
		seen := map[string]bool{}
		for _, name := range expected {
			seen[name] = true
		}
		for _, name := range columns[table] {
			if !seen[name] {
				return ErrSourceOwnership
			}
			delete(seen, name)
		}
		if len(seen) != 0 {
			return ErrSourceOwnership
		}
	}
	return nil
}
