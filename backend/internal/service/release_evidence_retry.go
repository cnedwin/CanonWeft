package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/domain"
)

// Also a normal conflict for callers outside release commands (for example an
// explicit prospective-index job), never an unclassified database/HTTP 500.
var errReleaseSourceBusy = fmt.Errorf("来源作者正在进行其他操作，请稍后重试: %w", domain.ErrConflict)

// Every attempt owns and closes its transactions before returning. Never wait
// for a different actor while holding the project: their ordinary request takes
// actor then project. NOWAIT + a full retry lets that request finish first.
func retryReleaseSourceAuthority(ctx context.Context, attempt func() (domain.Release, error)) (domain.Release, error) {
	return retrySourceAuthority(ctx, attempt)
}

func retrySourceAuthority[T any](ctx context.Context, attempt func() (T, error)) (T, error) {
	for retry := 0; ; retry++ {
		result, err := attempt()
		if !errors.Is(err, errReleaseSourceBusy) || retry == 7 {
			return result, err
		}
		timer := time.NewTimer(time.Duration(10<<retry) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			var empty T
			return empty, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *IP) releaseSourceActorTx(ctx context.Context, tx *sql.Tx, id string) (domain.Actor, error) {
	var locked string
	err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE NOWAIT", id).Scan(&locked)
	if err != nil {
		var databaseErr *mysql.MySQLError
		if errors.As(err, &databaseErr) && databaseErr.Number == 3572 {
			return domain.Actor{}, errReleaseSourceBusy
		}
		if err == sql.ErrNoRows {
			return domain.Actor{}, domain.ErrForbidden
		}
		return domain.Actor{}, err
	}
	// Keep the authoritative current enabled/role/workspace checks. This row is
	// already locked, so actorTx cannot reintroduce an actor/project lock wait.
	return s.Workspace.actorTx(ctx, tx, id)
}

// The qualified-use validator also checks the original qualification actor,
// who can be different from both publisher and generation author. Acquire that
// current actor lock without waiting before entering the unchanged validator.
func (s *IP) lockReleaseQualificationActorTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.Capability == nil || g.Frozen.Capability.Mode != "QUALIFIED" {
		return nil
	}
	record, err := (&Qualifications{IP: s}).recordTx(ctx, tx, g.ProjectID, g.Frozen.Capability.QualificationID)
	if err != nil {
		return err
	}
	run, err := qualificationRunTx(ctx, tx, g.ProjectID, record.RunID)
	if err != nil {
		return err
	}
	_, err = s.releaseSourceActorTx(ctx, tx, run.ActorID)
	return err
}
