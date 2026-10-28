package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

// A controllable database query makes successful-run/watchdog shutdown overlap
// deterministically, without a sleeping guest or a network database.
type toolWatchDriver struct{}
type toolWatchConn struct{}
type toolWatchTx struct{}

var watchQueryEntered chan struct{}
var registerWatchDriver sync.Once

func (toolWatchDriver) Open(string) (driver.Conn, error) { return toolWatchConn{}, nil }
func (toolWatchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (toolWatchConn) Close() error              { return nil }
func (toolWatchConn) Begin() (driver.Tx, error) { return toolWatchTx{}, nil }
func (toolWatchConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return toolWatchTx{}, nil
}
func (toolWatchConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	close(watchQueryEntered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (toolWatchTx) Commit() error   { return nil }
func (toolWatchTx) Rollback() error { return nil }

func TestToolTrialWatchNormalShutdownDoesNotBecomeTransientFailure(t *testing.T) {
	registerWatchDriver.Do(func() { sql.Register("tool-watch-cancellation", toolWatchDriver{}) })
	watchQueryEntered = make(chan struct{})
	db, err := sql.Open("tool-watch-cancellation", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &ToolTrials{IP: &IP{Workspace: &Workspace{DB: db}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go svc.watch(ctx, ToolTrial{}, cancel, done)
	select {
	case <-watchQueryEntered:
	case <-time.After(4 * time.Second):
		t.Fatal("watch query not reached")
	}
	cancel() // runner has completed; stop the monitor during its database query
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("normal shutdown requested execution retry", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not stop")
	}
}
