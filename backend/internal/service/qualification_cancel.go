package service

import "context"

// Cancellation bookkeeping uses stored child references and never dispatches.
// Unknown requests retain their hold; undispatched reservations are released.
func (s *Qualifications) cancelChildren(ctx context.Context, r QualificationRun) (string, error) {
	rows, err := s.IP.Workspace.DB.QueryContext(ctx, "SELECT s.child_id FROM qualification_steps s JOIN generation_tasks g ON g.id=s.child_id WHERE s.run_id=? AND s.child_kind='generation' AND g.state IN ('PREPARED','QUEUED','RUNNING')", r.ID)
	if err != nil {
		return "", err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if _, err = s.IP.Workspace.DB.ExecContext(ctx, "UPDATE generation_tasks SET paused=FALSE WHERE id=? AND state IN ('PREPARED','QUEUED','RUNNING')", id); err != nil {
			return "", err
		}
		if _, err = s.generationService().Recover(ctx, id); err != nil {
			return "", err
		}
	}
	return "CANCELLED", nil
}
