// Package authority owns the protected append-only deletion/revocation ledger.
// It is outside every business archive, but shares the MySQL physical failure domain.
package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type Source struct{ ServerUUID, Database string }
type Kind string

const (
	ProjectCreated    Kind = "project_created"
	ProjectDeleted    Kind = "project_deleted"
	SkillRevoked      Kind = "skill_revoked"
	CapabilityRevoked Kind = "capability_revoked"
)

type Event struct {
	Kind                                   Kind
	ProjectID, ResourceID, ActorID, Reason string
}
type State struct {
	Source               Source
	LedgerID             string
	Protocol             int
	CheckedAt            time.Time
	Projects             map[string]bool
	Skills, Capabilities []Event
}
type Ledger struct{ Schema string }

const Protocol = 1

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
var id32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var serverID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var ErrUnavailable = errors.New("protected authority unavailable or invalid")

func (l Ledger) table(name string) (string, error) {
	if !identifier.MatchString(l.Schema) {
		return "", ErrUnavailable
	}
	return "`" + l.Schema + "`." + name, nil
}

type SnapshotReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Identity(ctx context.Context, tx SnapshotReader) (Source, error) {
	var source Source
	err := tx.QueryRowContext(ctx, "SELECT @@server_uuid,DATABASE()").Scan(&source.ServerUUID, &source.Database)
	if err != nil {
		return source, err
	}
	if !serverID.MatchString(source.ServerUUID) || !identifier.MatchString(source.Database) {
		return source, ErrUnavailable
	}
	return source, nil
}

func (l Ledger) provenance(ctx context.Context, tx SnapshotReader, source Source) (string, error) {
	table, err := l.table("sources")
	if err != nil {
		return "", err
	}
	var ledgerID string
	var protocol int
	var baselineText string
	err = tx.QueryRowContext(ctx, "SELECT ledger_id,protocol,CAST(baseline_committed_at AS CHAR) FROM "+table+" WHERE server_uuid=? AND source_database=?", source.ServerUUID, source.Database).Scan(&ledgerID, &protocol, &baselineText)
	if err != nil {
		return "", fmt.Errorf("%w: source record: %v", ErrUnavailable, err)
	}
	baseline, parseErr := time.Parse("2006-01-02 15:04:05.999999", baselineText)
	if parseErr != nil || protocol != Protocol || !id32.MatchString(ledgerID) || baseline.IsZero() || baseline.After(time.Now().Add(time.Minute)) {
		return "", ErrUnavailable
	}
	return ledgerID, nil
}

// ProvenanceTx binds a newly created archive to this trusted baseline instance.
// It uses the caller's consistent snapshot and derives the business identity.
func (l Ledger) ProvenanceTx(ctx context.Context, tx SnapshotReader) (State, error) {
	source, err := Identity(ctx, tx)
	if err != nil {
		return State{}, err
	}
	id, err := l.provenance(ctx, tx, source)
	return State{Source: source, LedgerID: id, Protocol: Protocol}, err
}

func validEvent(e Event) bool {
	if !id32.MatchString(e.ProjectID) || !id32.MatchString(e.ResourceID) || (e.ActorID != "" && !id32.MatchString(e.ActorID)) || len(e.Reason) > 4000 {
		return false
	}
	switch e.Kind {
	case ProjectCreated, ProjectDeleted:
		return e.ProjectID == e.ResourceID
	case SkillRevoked, CapabilityRevoked:
		return true
	default:
		return false
	}
}

// AppendTx derives the source from the same business transaction. It never opens
// another connection or commits the caller's transaction.
func (l Ledger) AppendTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if !validEvent(event) || event.ActorID == "" {
		return ErrUnavailable
	}
	source, err := Identity(ctx, tx)
	if err != nil {
		return err
	}
	ledgerID, err := l.provenance(ctx, tx, source)
	if err != nil {
		return err
	}
	table, err := l.table("events")
	if err != nil {
		return err
	}
	if event.Kind != ProjectCreated {
		active, err := l.active(ctx, tx, ledgerID, event.ProjectID)
		if err != nil {
			return err
		}
		if !active {
			// A repeated deletion is safe; every other write stays closed.
			if event.Kind != ProjectDeleted {
				return ErrUnavailable
			}
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ledger_id=? AND kind=? AND project_id=?", ledgerID, ProjectDeleted, event.ProjectID).Scan(&count); err != nil || count != 1 {
				return ErrUnavailable
			}
		}
	}
	hasClock, err := l.eventClockSupported(ctx, tx)
	if err != nil {
		return err
	}
	columns, values := "ledger_id,kind,project_id,resource_id,actor_id,reason,created_at", "?,?,?,?,?,?,UTC_TIMESTAMP(6)"
	if hasClock {
		columns += ",clock_basis"
		values += ",'UTC_V1'"
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO "+table+"("+columns+") VALUES("+values+")", ledgerID, event.Kind, event.ProjectID, event.ResourceID, event.ActorID, event.Reason)
	var duplicate *mysql.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		// Immutable duplicates preserve the original evidence. Scope and kind are
		// part of the unique key, so retries cannot rewrite another event.
		return nil
	}
	return err
}

func (l Ledger) active(ctx context.Context, tx *sql.Tx, ledgerID, project string) (bool, error) {
	table, err := l.table("events")
	if err != nil {
		return false, err
	}
	var created, deleted int
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(kind='project_created'),0),COALESCE(SUM(kind='project_deleted'),0) FROM "+table+" WHERE ledger_id=? AND project_id=? AND kind IN ('project_created','project_deleted')", ledgerID, project).Scan(&created, &deleted)
	return created == 1 && deleted == 0, err
}

// ProjectActiveTx also rejects projects restored behind the protected tombstone.
func (l Ledger) ProjectActiveTx(ctx context.Context, tx *sql.Tx, project string) (bool, error) {
	source, err := Identity(ctx, tx)
	if err != nil {
		return false, err
	}
	id, err := l.provenance(ctx, tx, source)
	if err != nil {
		return false, err
	}
	return l.active(ctx, tx, id, project)
}

// DeletionRecordedTx reconciles a lost deletion response against immutable
// authority, instead of trusting a possibly restored business receipt alone.
func (l Ledger) DeletionRecordedTx(ctx context.Context, tx *sql.Tx, project string) (bool, error) {
	source, err := Identity(ctx, tx)
	if err != nil {
		return false, err
	}
	ledgerID, err := l.provenance(ctx, tx, source)
	if err != nil {
		return false, err
	}
	table, err := l.table("events")
	if err != nil {
		return false, err
	}
	var count int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ledger_id=? AND kind='project_deleted' AND project_id=? AND resource_id=?", ledgerID, project, project).Scan(&count)
	return count == 1, err
}

// Current needs only a live connection to the protected schema's server. The
// original business database may be missing or inaccessible.
func (l Ledger) Current(ctx context.Context, db *sql.DB, want Source) (State, error) {
	state := State{Source: want, Projects: map[string]bool{}, Protocol: Protocol}
	if db == nil || !serverID.MatchString(want.ServerUUID) || !identifier.MatchString(want.Database) {
		return state, ErrUnavailable
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	var actual string
	if err = tx.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&actual); err != nil {
		return state, err
	}
	if actual != want.ServerUUID {
		return state, ErrUnavailable
	}
	state.LedgerID, err = l.provenance(ctx, tx, want)
	if err != nil {
		return state, err
	}
	table, err := l.table("events")
	if err != nil {
		return state, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT kind,project_id,resource_id,actor_id,reason FROM "+table+" WHERE ledger_id=? ORDER BY sequence_id", state.LedgerID)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var event Event
		if err = rows.Scan(&event.Kind, &event.ProjectID, &event.ResourceID, &event.ActorID, &event.Reason); err != nil {
			rows.Close()
			return state, err
		}
		if !validEvent(event) {
			rows.Close()
			return state, ErrUnavailable
		}
		_, known := state.Projects[event.ProjectID]
		if event.Kind != ProjectCreated && !known {
			rows.Close()
			return state, ErrUnavailable
		}
		switch event.Kind {
		case ProjectCreated:
			if known {
				rows.Close()
				return state, ErrUnavailable
			}
			state.Projects[event.ProjectID] = true
		case ProjectDeleted:
			state.Projects[event.ProjectID] = false
		case SkillRevoked:
			state.Skills = append(state.Skills, event)
		case CapabilityRevoked:
			state.Capabilities = append(state.Capabilities, event)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	state.CheckedAt = time.Now().UTC()
	return state, nil
}

// RevokedTx checks a bounded set of exact resource IDs, including a caller's
// already validated ancestor chain. It does not trust restored revocation rows.
func (l Ledger) RevokedTx(ctx context.Context, tx *sql.Tx, project string, kind Kind, ids []string) (bool, error) {
	if kind != SkillRevoked && kind != CapabilityRevoked || len(ids) == 0 || len(ids) > 256 {
		return false, ErrUnavailable
	}
	for _, id := range ids {
		if !id32.MatchString(id) {
			return false, ErrUnavailable
		}
	}
	source, err := Identity(ctx, tx)
	if err != nil {
		return false, err
	}
	ledgerID, err := l.provenance(ctx, tx, source)
	if err != nil {
		return false, err
	}
	table, err := l.table("events")
	if err != nil {
		return false, err
	}
	args := []any{ledgerID, project, kind}
	for _, id := range ids {
		args = append(args, id)
	}
	var count int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ledger_id=? AND project_id=? AND kind=? AND resource_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...).Scan(&count)
	return count > 0, err
}
