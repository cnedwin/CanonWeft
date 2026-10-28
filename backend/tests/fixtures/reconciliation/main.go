// Test-only fixture: creates one unknown accounting record without any model call.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dsn := os.Getenv("STUDIO_TEST_DSN")
	config, err := mysql.ParseDSN(dsn)
	if err != nil || config.DBName != "studio_test" || config.User != "studio_test" {
		return fmt.Errorf("isolated test database required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	var actorID string
	if err = db.QueryRowContext(ctx, "SELECT id FROM users WHERE username='owner' AND role='owner'").Scan(&actorID); err != nil {
		return err
	}
	workspace := &service.Workspace{DB: db}
	svc := &service.Budgets{Workspace: workspace}
	project, err := workspace.CreateProject(ctx, actorID, "浏览器对账样本 "+service.NewID()[:8], service.NewID())
	if err != nil {
		return err
	}
	budget, err := svc.Read(ctx, actorID)
	if err != nil {
		return err
	}
	budget.Limit = max(budget.Limit, budget.Spent+budget.Reserved+100)
	budget.PerCall = max(budget.PerCall, 100)
	budget.Enabled = true
	if _, err = svc.Configure(ctx, actorID, budget); err != nil {
		return err
	}
	defer func() {
		if b, e := svc.Read(ctx, actorID); e == nil {
			b.Enabled = false
			_, _ = svc.Configure(ctx, actorID, b)
		}
	}()
	call, err := svc.Reserve(ctx, domain.Actor{ID: actorID}, project.ID, service.NewID(), 80, budget.Currency, "browser-fixture")
	if err != nil {
		return err
	}
	if err = svc.Dispatch(ctx, domain.Actor{ID: actorID}, project.ID, call.ID); err != nil {
		return err
	}
	if err = svc.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": call.ID, "project": project.Name, "currency": budget.Currency})
}
