package backup

import (
	"context"
	"database/sql"
	"errors"
	"novelstudio/internal/authority"
	"os"
	"time"
)

type Revocation struct{ ID, ProjectID, ActorID, Reason string }
type AuthorityState struct {
	Source       Source
	CheckedAt    time.Time
	Projects     map[string]bool
	Skills       []Revocation
	Capabilities []Revocation
	LedgerID     string
	Protocol     int
}
type ProtectedAuthority struct {
	DB     *sql.DB
	Ledger authority.Ledger
}

func configuredLedger() authority.Ledger {
	schema := os.Getenv("STUDIO_AUTHORITY_SCHEMA")
	if schema == "" {
		schema = "studio_authority"
	}
	return authority.Ledger{Schema: schema}
}
func (a ProtectedAuthority) Current(ctx context.Context, want Source) (AuthorityState, error) {
	current, err := a.Ledger.Current(ctx, a.DB, authority.Source{ServerUUID: want.ServerUUID, Database: want.Database})
	if err != nil {
		return AuthorityState{}, err
	}
	state := AuthorityState{Source: want, CheckedAt: current.CheckedAt, Projects: current.Projects, LedgerID: current.LedgerID, Protocol: current.Protocol}
	for _, event := range current.Skills {
		state.Skills = append(state.Skills, Revocation{ID: event.ResourceID, ProjectID: event.ProjectID, ActorID: event.ActorID, Reason: event.Reason})
	}
	for _, event := range current.Capabilities {
		state.Capabilities = append(state.Capabilities, Revocation{ID: event.ResourceID, ProjectID: event.ProjectID, ActorID: event.ActorID, Reason: event.Reason})
	}
	return state, nil
}

// CurrentAuthority is trusted live infrastructure, never deserialized from the
// archive. Implementations must prove completeness/currentness or return error.
// ProtectedAuthority does not require the original business database. Arbitrary
// JSON exports are deliberately not accepted as authoritative evidence.
type CurrentAuthority interface {
	Current(context.Context, Source) (AuthorityState, error)
}
type LiveAuthority struct{ DB *sql.DB }

func (a LiveAuthority) Current(ctx context.Context, want Source) (AuthorityState, error) {
	if a.DB == nil {
		return AuthorityState{}, authority.ErrUnavailable
	}
	actual, err := sourceIdentity(ctx, a.DB)
	if err != nil || actual != want {
		return AuthorityState{}, authority.ErrUnavailable
	}
	return (ProtectedAuthority{DB: a.DB, Ledger: configuredLedger()}).Current(ctx, want)
}
func checkAuthority(a *Archive, state AuthorityState) error {
	if !hex32.MatchString(state.LedgerID) || state.Protocol != authority.Protocol {
		return errors.New("protected ledger provenance required")
	}
	if a.Manifest.Authority != nil && (a.Manifest.Authority.LedgerID != state.LedgerID || a.Manifest.Authority.Protocol != state.Protocol) {
		return errors.New("protected ledger provenance mismatch")
	}
	if state.Source != a.Manifest.Source || state.CheckedAt.IsZero() || state.CheckedAt.Before(a.Manifest.SnapshotAt) || time.Since(state.CheckedAt) > time.Minute || state.CheckedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("current authoritative revocation evidence required")
	}
	for _, s := range tableCatalog {
		if s.name == "projects" {
			for _, r := range a.Data[s.name] {
				if !state.Projects[s.value(r, "id")] {
					return errors.New("restore refused: snapshot contains a project missing from current authority")
				}
			}
		}
	}
	return nil
}
func replayRevocations(ctx context.Context, tx *sql.Tx, state AuthorityState, schema ...int) error {
	for _, entry := range []struct {
		table, idCol, parent string
		revocations          []Revocation
	}{
		{"skill_revocations", "package_id", "skill_packages", state.Skills},
		{"capability_revocations", "version_id", "capability_versions", state.Capabilities},
	} {
		for _, r := range entry.revocations {
			if !hex32.MatchString(r.ID) || !hex32.MatchString(r.ProjectID) || !hex32.MatchString(r.ActorID) {
				return errors.New("invalid authority identity")
			}
			// Newer versions absent from this snapshot need no row. Existing lineage
			// rows receive the current revocation, including ancestors of derived skills.
			conflict := "reason=VALUES(reason)"
			if len(schema) == 1 && hasRetentionSchema(schema[0]) {
				// Existing reasons are journal-bound historical content. Keep
				// them; the independent ledger remains the current authority.
				conflict = entry.idCol + "=" + entry.table + "." + entry.idCol
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO "+entry.table+"("+entry.idCol+",project_id,actor_id,reason) SELECT id,project_id,?,? FROM "+entry.parent+" WHERE id=? AND project_id=? ON DUPLICATE KEY UPDATE "+conflict, r.ActorID, r.Reason, r.ID, r.ProjectID)
			if err != nil {
				return err
			}
			if entry.parent == "skill_packages" {
				if _, err = tx.ExecContext(ctx, "UPDATE skill_packages SET state='REVOKED' WHERE id=? AND project_id=?", r.ID, r.ProjectID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
