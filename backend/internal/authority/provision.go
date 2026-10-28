package authority

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// Provision is trusted local administration, run with API and workers stopped.
// DDL is resumable; the complete baseline and provenance marker commit together.
// Existing provenance is never replaced, and the application cannot call this
// successfully with its SELECT/INSERT-only ledger grants.
func Provision(ctx context.Context, admin *sql.DB, schema, business string) (Ledger, error) {
	ledger := Ledger{Schema: schema}
	if !identifier.MatchString(schema) || !identifier.MatchString(business) || schema == business {
		return ledger, ErrUnavailable
	}
	conn, err := admin.Conn(ctx)
	if err != nil {
		return ledger, err
	}
	defer conn.Close()
	var locked int
	lockName := business + ":schema"
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,10)", lockName).Scan(&locked); err != nil {
		return ledger, err
	}
	if locked != 1 {
		return ledger, ErrUnavailable
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
	if _, err = conn.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+schema+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		return ledger, err
	}
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS `" + schema + "`.sources (server_uuid CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,source_database VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,ledger_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,protocol INT NOT NULL,baseline_committed_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),PRIMARY KEY(server_uuid,source_database),UNIQUE KEY(ledger_id)) ENGINE=InnoDB",
		"CREATE TABLE IF NOT EXISTS `" + schema + "`.events (sequence_id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,ledger_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,project_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,actor_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,reason TEXT NOT NULL,created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),UNIQUE KEY event_identity(ledger_id,kind,project_id,resource_id),KEY project_events(ledger_id,project_id,kind),CONSTRAINT event_source FOREIGN KEY(ledger_id) REFERENCES `" + schema + "`.sources(ledger_id)) ENGINE=InnoDB",
	} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return ledger, err
		}
	}
	// Adding proof metadata never rewrites old times. Old applications and the
	// initial baseline omit the column and remain explicitly LEGACY.
	hasClock, err := ledger.eventClockSupported(ctx, conn)
	if err != nil {
		return ledger, err
	}
	if !hasClock {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE `"+schema+"`.events ADD COLUMN clock_basis ENUM('LEGACY','UTC_V1') NOT NULL DEFAULT 'LEGACY'"); err != nil {
			return ledger, err
		}
	}
	// A mixed/nontransactional business engine would destroy the atomicity claim.
	for _, entry := range []struct{ schema, table string }{{business, "projects"}, {business, "skill_revocations"}, {business, "capability_revocations"}, {schema, "sources"}, {schema, "events"}} {
		var engine string
		if err = conn.QueryRowContext(ctx, "SELECT engine FROM information_schema.tables WHERE table_schema=? AND table_name=?", entry.schema, entry.table).Scan(&engine); err != nil {
			return ledger, err
		}
		if engine != "InnoDB" {
			return ledger, fmt.Errorf("%w: InnoDB required", ErrUnavailable)
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ledger, err
	}
	defer tx.Rollback()
	source := Source{Database: business}
	if err = tx.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&source.ServerUUID); err != nil {
		return ledger, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+schema+"`.sources WHERE server_uuid=? AND source_database=?", source.ServerUUID, business).Scan(&count); err != nil {
		return ledger, err
	}
	if count != 0 {
		if _, err = ledger.provenance(ctx, tx, source); err != nil {
			return ledger, err
		}
		return ledger, tx.Commit()
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return ledger, err
	}
	id := hex.EncodeToString(random)
	if _, err = tx.ExecContext(ctx, "INSERT INTO `"+schema+"`.sources(server_uuid,source_database,ledger_id,protocol,baseline_committed_at) VALUES(?,?,?,?,UTC_TIMESTAMP(6))", source.ServerUUID, business, id, Protocol); err != nil {
		return ledger, err
	}
	events := "`" + schema + "`.events"
	prefix := "INSERT INTO " + events + "(ledger_id,kind,project_id,resource_id,actor_id,reason) "
	if _, err = tx.ExecContext(ctx, prefix+"SELECT ?,'project_created',id,id,'','trusted initial baseline' FROM `"+business+"`.projects ORDER BY id", id); err != nil {
		return ledger, err
	}
	// Baseline supports the known schema24 before schema25 is provisioned.
	var hasDeleted int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=? AND table_name='projects' AND column_name='deleted_at'", business).Scan(&hasDeleted); err != nil {
		return ledger, err
	}
	if hasDeleted == 1 {
		if _, err = tx.ExecContext(ctx, prefix+"SELECT ?,'project_deleted',id,id,'','trusted initial deletion baseline' FROM `"+business+"`.projects WHERE deleted_at IS NOT NULL ORDER BY id", id); err != nil {
			return ledger, err
		}
	}
	for _, entry := range []struct {
		table, id string
		kind      Kind
	}{{"skill_revocations", "package_id", SkillRevoked}, {"capability_revocations", "version_id", CapabilityRevoked}} {
		if _, err = tx.ExecContext(ctx, prefix+"SELECT ?,?,project_id,"+entry.id+",actor_id,reason FROM `"+business+"`."+entry.table+" ORDER BY "+entry.id, id, entry.kind); err != nil {
			return ledger, err
		}
	}
	return ledger, tx.Commit()
}
