package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/hostsignals"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("STUDIO_DSN")
	if dsn == "" {
		return errors.New("STUDIO_DSN is required; use Start-Platform.ps1")
	}
	address := os.Getenv("STUDIO_ADDR")
	if address == "" {
		address = "127.0.0.1:18765"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("local server must bind an explicit loopback address")
	}
	cfg, err := applicationDatabaseConfig(dsn)
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = storage.RunMigrations(ctx, db)
	cancel()
	if err != nil {
		return err
	}
	var runtime *worker.Runtime
	// Do not start workers or present a ready application without its independent
	// revocation authority. Provisioning is a separate trusted local operation.
	check, checkCancel := context.WithTimeout(context.Background(), 20*time.Second)
	var source authority.Source
	err = db.QueryRowContext(check, "SELECT @@server_uuid,DATABASE()").Scan(&source.ServerUUID, &source.Database)
	if err == nil {
		schema := os.Getenv("STUDIO_AUTHORITY_SCHEMA")
		if schema == "" {
			schema = "studio_authority"
		}
		_, err = (authority.Ledger{Schema: schema}).Current(check, db, source)
	}
	checkCancel()
	if err != nil {
		return errors.New("protected authority is not ready; stop source services and run scripts/initialize-authority.ps1 for this database")
	}
	if temporalAddress := os.Getenv("STUDIO_TEMPORAL"); temporalAddress != "" {
		startup, startupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		tasks := &service.Tasks{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
		runtime, err = worker.StartRuntime(startup, tasks, temporalAddress, os.Getenv("STUDIO_TEMPORAL_NAMESPACE"))
		startupCancel()
		if err != nil {
			return err
		}
		defer runtime.Close()
	}
	stopped, stop := hostsignals.NotifyContext(context.Background())
	defer stop()
	protection, stopProtection, err := startProtection(stopped, db, source.Database)
	if err != nil {
		return err
	}
	defer stopProtection()
	logProtection, stopLogProtection, err := startLogProtection(stopped, source.Database, source.ServerUUID)
	if err != nil {
		return err
	}
	defer stopLogProtection()
	e := httpapi.New(db, "http://"+address, runtime)
	httpapi.RegisterProtection(e, db, func(now time.Time) backup.ProtectionView {
		view := protection.View(now)
		if logProtection != nil {
			logs := logProtection.View(now)
			view.LogCoverage = &logs
			view.LogRecovery = logs.State
		}
		return view
	})
	web := os.Getenv("STUDIO_WEB_DIR")
	if web != "" {
		e.StaticFS("/assets/", os.DirFS(filepath.Join(web, "assets")))
		e.GET("/*", func(c *echo.Context) error {
			if strings.HasPrefix(c.Request().URL.Path, "/api/") {
				return c.JSON(404, map[string]string{"error": "接口不存在"})
			}
			return c.FileFS("index.html", os.DirFS(web))
		})
	}
	server := &http.Server{Addr: address, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { log.Printf("Studio available at http://%s", address); done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-stopped.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
	return nil
}
