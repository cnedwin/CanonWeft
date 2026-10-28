package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A preview is private, mutable working text. It is never an artifact revision,
// validated model result, receipt, or evidence that can be adopted/published.
type GenerationPreview struct {
	Text        string `json:"text"`
	Sequence    int64  `json:"sequence"`
	Hash        string `json:"hash"`
	Provisional bool   `json:"provisional"`
}

type previewBuffer struct {
	mu       sync.Mutex
	text     strings.Builder
	sequence int64
}

func (b *previewBuffer) append(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !utf8.ValidString(text) || b.text.Len()+len(text) > 4*1024*1024 {
		return domain.ErrValidation
	}
	if text != "" {
		b.text.WriteString(text)
		b.sequence++
	}
	return nil
}

func (b *previewBuffer) snapshot() GenerationPreview {
	b.mu.Lock()
	defer b.mu.Unlock()
	return GenerationPreview{Text: b.text.String(), Sequence: b.sequence, Provisional: true}
}

func (s *Generations) validateEvidence(ctx context.Context, tx *sql.Tx, g Generation) error {
	if err := s.validateRecipeChildTx(ctx, tx, g, false); err != nil {
		return err
	}
	if err := s.validateSemanticReviewTx(ctx, tx, g); err != nil {
		return err
	}
	if err := (&Capabilities{IP: s.IP}).validateConfiguredTx(ctx, tx, g); err != nil {
		return err
	}
	if err := (&Capabilities{IP: s.IP}).validateTrialTx(ctx, tx, g.ProjectID, g.Frozen.Capability); err != nil {
		return err
	}
	b, err := branchTx(ctx, tx, g.ProjectID, g.BranchID)
	if err != nil {
		return err
	}
	if b.Head != g.Frozen.BaseHead {
		return domain.ErrConflict
	}
	if g.Frozen.ChangePlanID != "" {
		if _, err = currentChangePlanTx(ctx, tx, g.ProjectID, g.BranchID, g.Frozen.ChangePlanID); err != nil {
			return err
		}
	}
	if err = requireMemoryContextTx(ctx, tx, g.ProjectID, g.BranchID, g.Frozen.Role, g.Frozen.ContextID); err != nil {
		return err
	}
	if g.Frozen.ContextID != "" {
		_, err = (&Knowledge{IP: s.IP}).readContextTx(ctx, tx, g.ActorID, g.ProjectID, g.BranchID, g.Frozen.ContextID)
	}
	return err
}

func (s *Generations) readPreview(ctx context.Context, tx *sql.Tx, g Generation) (*GenerationPreview, error) {
	var p GenerationPreview
	err := tx.QueryRowContext(ctx, "SELECT content,sequence,content_hash FROM generation_previews WHERE generation_id=?", g.ID).Scan(&p.Text, &p.Sequence, &p.Hash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expired := rejectExpiredRuntime([]byte(p.Text), "generation_previews.content"); expired != nil {
		if _, err := runtimeTransientTx(ctx, tx, g.ProjectID, "generation_previews", "content", g.ID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err = s.validateEvidence(ctx, tx, g); err != nil {
		return nil, err
	}
	if p.Sequence < 1 || len(p.Text) > 4*1024*1024 || !utf8.ValidString(p.Text) || digest(p.Text) != p.Hash {
		return nil, domain.ErrNotReady
	}
	p.Provisional = true
	return &p, nil
}

// The monitor performs a fresh authority transaction even without new text, so
// revocation or cancellation interrupts a provider stalled between SSE frames.
func (s *Generations) persistPreview(ctx context.Context, g Generation, p GenerationPreview, changed, final bool) error {
	tx, _, _, err := s.IP.begin(ctx, domain.Actor{ID: g.ActorID}, g.ProjectID, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := generationTx(ctx, tx, g.ProjectID, g.ID)
	if err != nil {
		return err
	}
	if current.State != "RUNNING" || current.Cancelled || current.Paused {
		return domain.ErrConflict
	}
	if err = s.validateFrozen(ctx, tx, current); err != nil {
		return err
	}
	budget, err := budgetTx(ctx, tx, g.WorkspaceID)
	if err != nil {
		return err
	}
	if !budget.Enabled {
		return ErrBudgetClosed
	}
	if changed && p.Sequence > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO generation_previews(generation_id,content,sequence,content_hash) VALUES(?,?,?,?)
 ON DUPLICATE KEY UPDATE content=IF(VALUES(sequence)>sequence,VALUES(content),content),content_hash=IF(VALUES(sequence)>sequence,VALUES(content_hash),content_hash),sequence=GREATEST(sequence,VALUES(sequence))`, g.ID, p.Text, p.Sequence, digest(p.Text))
		if err != nil {
			return err
		}
	}
	if (changed || final) && p.Sequence > 0 {
		if err = retention.CheckpointGenerationPreview(ctx, tx, protectedLedger(), g.ProjectID, g.ID, final); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Generations) generateStream(ctx context.Context, g Generation, key []byte, request models.Request, authorize func(context.Context) error) (models.Result, error) {
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	buffer := &previewBuffer{}
	var stopMonitor context.CancelFunc
	var monitorDone chan error
	var lastPersisted int64
	// Start after durable dispatch, before opening the provider connection.
	guardedAuthorize := func(dispatchCtx context.Context) error {
		if err := authorize(dispatchCtx); err != nil {
			return err
		}
		var monitorCtx context.Context
		monitorCtx, stopMonitor = context.WithCancel(streamCtx)
		monitorDone = make(chan error, 1)
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-monitorCtx.Done():
					monitorDone <- nil
					return
				case <-ticker.C:
					p := buffer.snapshot()
					checkCtx, cancel := context.WithTimeout(monitorCtx, 3*time.Second)
					err := s.persistPreview(checkCtx, g, p, p.Sequence > lastPersisted, false)
					cancel()
					if monitorCtx.Err() != nil {
						monitorDone <- nil
						return
					}
					if err != nil {
						stopStream()
						monitorDone <- err
						return
					}
					lastPersisted = p.Sequence
				}
			}
		}()
		return nil
	}
	output, callErr := s.Adapter.GenerateStream(streamCtx, g.Frozen.Config, key, request, guardedAuthorize, func(d models.Delta) error { return buffer.append(d.Text) })
	if stopMonitor == nil {
		return output, callErr
	}
	stopMonitor()
	monitorErr := <-monitorDone // no writer remains when final flush begins
	if monitorErr == nil && streamCtx.Err() == nil {
		p := buffer.snapshot()
		flushCtx, cancel := context.WithTimeout(streamCtx, 3*time.Second)
		monitorErr = s.persistPreview(flushCtx, g, p, p.Sequence > lastPersisted, true)
		cancel()
	}
	if monitorErr != nil && callErr == nil {
		// A complete receipt still accounts for known fees, but cannot grant
		// adoption after the current permission/version check stopped the run.
		callErr = models.ErrInvalidOutput
	}
	return output, callErr
}
