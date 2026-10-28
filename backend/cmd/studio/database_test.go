package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplicationDatabaseUsesUTCOnEveryConnection(t *testing.T) {
	cfg, err := applicationDatabaseConfig("test:secret@tcp(127.0.0.1:13306)/test?loc=Local&time_zone=%27%2B08%3A00%27&parseTime=false")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.Params["time_zone"] != "'+00:00'" {
		t.Fatal("application connection must use UTC for SQL and decoding")
	}
	_, err = applicationDatabaseConfig("sensitive-password@invalid")
	if err == nil || strings.Contains(err.Error(), "sensitive-password") {
		t.Fatal("invalid connection must fail without secret")
	}
}

func TestApplicationDatabaseUTCPoolMySQL(t *testing.T) {
	dsn := os.Getenv("STUDIO_TEST_DSN")
	if dsn == "" {
		t.Skip("STUDIO_TEST_DSN required")
	}
	cfg, err := applicationDatabaseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open test database")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var connections []*sql.Conn
	defer func() {
		for _, conn := range connections {
			conn.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		var zone string
		var offset int
		var now time.Time
		if err := conn.QueryRowContext(ctx, "SELECT @@session.time_zone,TIMESTAMPDIFF(SECOND,UTC_TIMESTAMP(),NOW()),CURRENT_TIMESTAMP(6)").Scan(&zone, &offset, &now); err != nil {
			t.Fatal(err)
		}
		if zone != "+00:00" || offset != 0 || time.Since(now).Abs() > time.Minute {
			t.Fatalf("pooled connection %d has inconsistent time semantics", i)
		}
	}
}
