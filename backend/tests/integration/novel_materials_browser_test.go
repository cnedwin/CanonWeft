package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"novelstudio/internal/service"
)

// Drives the real ingest activity against the isolated browser database.
// Scheduling is local to this test; Temporal restart behavior has its own suite.
func startNovelBrowserIngest(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rows, err := db.QueryContext(ctx, "SELECT id FROM ingest_jobs WHERE state IN ('QUEUED','RUNNING') AND paused=0 AND cancel_requested=0")
				if err != nil {
					if ctx.Err() == nil {
						t.Errorf("browser ingest list: %v", err)
					}
					return
				}
				var ids []string
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						break
					}
					ids = append(ids, id)
				}
				rowErr := rows.Err()
				rows.Close()
				if err != nil || rowErr != nil {
					t.Errorf("browser ingest rows: %v %v", err, rowErr)
					return
				}
				for _, id := range ids {
					if _, err = jobs.Advance(ctx, id); err != nil && ctx.Err() == nil {
						t.Errorf("browser ingest advance: %v", err)
						return
					}
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}
