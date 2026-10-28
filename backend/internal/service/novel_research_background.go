package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"novelstudio/internal/domain"
)

var errResearchSuperseded = errors.New("research checkpoint superseded")
var errResearchBusy = errors.New("research checkpoint leased")

type researchBackgroundPin struct {
	ActorID    string `json:"actor_id"`
	BookID     string `json:"book_id"`
	NotebookID string `json:"notebook_id"`
	Revision   int64  `json:"revision"`
	Hash       string `json:"hash"`
}

func researchRunPin(run ResearchExecution) researchBackgroundPin {
	return researchBackgroundPin{run.Document.ActorID, run.Document.BookID, run.Document.NotebookID, run.Revision, capabilityDigest(run.Document)}
}

// Each event grants only one exact checkpoint. The next event and the result
// commit together. Editing private JSON alone never grants background execution.
func (s *NovelResearchExecutions) queueResearchStepTx(ctx context.Context, tx *sql.Tx, book NovelBook, run *ResearchExecution) error {
	run.BackgroundQueued = false
	if !run.Document.Background || run.Document.Paused || run.Document.Cancelled || run.Document.Error != "" || (run.Document.Stage != "search" && run.Document.Stage != "collect") {
		return nil
	}
	id := digest("research.step:" + book.ProjectID + ":" + run.ResourceID + ":" + capabilityDigest(researchRunPin(*run)))[:32]
	raw, err := json.Marshal(researchRunPin(*run))
	if err != nil {
		return err
	}
	// outbox keeps one row per kind/resource. Rotate its opaque grant ID with
	// the checkpoint, just like recipe retry. A stale dispatcher acknowledgement
	// for the previous ID then cannot consume the next checkpoint's event.
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'research.step',?,?) ON DUPLICATE KEY UPDATE id=VALUES(id),payload=VALUES(payload),created_at=UTC_TIMESTAMP(6),processed_at=NULL", id, book.ProjectID, run.ResourceID, raw); err != nil {
		return err
	}
	run.BackgroundQueued = true
	return nil
}

func (s *NovelResearchExecutions) StartBackground(ctx context.Context, actor, bookID, notebook, id string, revision int64) (ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchExecution{}, err
	}
	defer tx.Rollback()
	run, rd, err := s.readTx(ctx, tx, a, book, notebook, id)
	if err != nil {
		return run, err
	}
	if run.Revision != revision || run.Document.Cancelled || (run.Document.Attempt != "" && run.Document.LeaseUntil > time.Now().Unix()) {
		return run, domain.ErrConflict
	}
	if run.BackgroundQueued && !run.Document.Paused && run.Document.Error == "" {
		return run, nil
	}
	run.Document.Background = true
	run.Document.Paused = false
	run.Document.Error = ""
	run.Document.Attempt = ""
	run.Document.LeaseUntil = 0
	run, err = s.saveTx(ctx, tx, a, book, run, rd)
	if err != nil {
		return run, err
	}
	if err = s.queueResearchStepTx(ctx, tx, book, &run); err != nil {
		return run, err
	}
	return run, tx.Commit()
}

func (s *NovelResearchExecutions) checkResearchEventTx(ctx context.Context, tx *sql.Tx, eventID string, book NovelBook, run ResearchExecution) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT payload FROM outbox WHERE id=? AND project_id=? AND kind='research.step' AND resource_id=? FOR UPDATE", eventID, book.ProjectID, run.ResourceID).Scan(&raw)
	if err == sql.ErrNoRows {
		return errResearchSuperseded
	}
	if err != nil {
		return err
	}
	var pin researchBackgroundPin
	if json.Unmarshal(raw, &pin) != nil || pin != researchRunPin(run) || !run.Document.Background {
		return errResearchSuperseded
	}
	return nil
}

func (s *NovelResearchExecutions) advanceResearchClaimTx(ctx context.Context, tx *sql.Tx, eventID string, run ResearchExecution) error {
	raw, err := json.Marshal(researchRunPin(run))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE outbox SET payload=? WHERE id=? AND kind='research.step'", raw, eventID)
	return err
}

// Temporal receives only the opaque outbox ID. Backup event history is retained
// with delivery disabled; stale events and removed permissions cannot fetch.
func (s *NovelResearchExecutions) AdvanceBackground(ctx context.Context, eventID string) (string, error) {
	if !lowerHex32(eventID) {
		return "CANCELLED", nil
	}
	var raw []byte
	var id, project string
	err := s.Research.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT project_id,resource_id,payload FROM outbox WHERE id=? AND kind='research.step'", eventID).Scan(&project, &id, &raw)
	if err == sql.ErrNoRows {
		return "CANCELLED", nil
	}
	if err != nil {
		return "", err
	}
	var pin researchBackgroundPin
	if json.Unmarshal(raw, &pin) != nil || !lowerHex32(pin.ActorID) || !lowerHex32(pin.BookID) || !lowerHex32(pin.NotebookID) || !lowerHex32(id) || !lowerHex32(project) || pin.Revision < 1 || len(pin.Hash) != 64 {
		return "CANCELLED", nil
	}
	run, err := s.step(ctx, pin.ActorID, pin.BookID, pin.NotebookID, id, pin.Revision, eventID)
	if errors.Is(err, errResearchSuperseded) {
		return "SUPERSEDED", nil
	}
	if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrMaterialExpired) {
		return "CANCELLED", nil
	}
	if err != nil {
		return "", err
	}
	if run.Document.Cancelled {
		return "CANCELLED", nil
	}
	if run.Document.Paused {
		return "PAUSED", nil
	}
	if run.Document.Error != "" {
		return "WAITING_RETRY", nil
	}
	if run.BackgroundQueued {
		return "QUEUED", nil
	}
	if run.Document.Stage == "complete" {
		return "COMPLETED", nil
	}
	return "WAITING_SELECTION", nil
}
