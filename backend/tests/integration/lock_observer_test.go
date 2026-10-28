package integration

import (
	"database/sql"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// Performance-schema observation is a fixture privilege. The service and the
// transactions whose lock order is tested keep the normal application account.
func publicationLockObserver(t *testing.T, application *sql.DB) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN")
	if dsn == "" {
		dsn = os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil || config.User != "root" || config.Net != "tcp" || config.Addr != "127.0.0.1:13306" {
		t.Fatal("lock observation requires the explicit local test administrator connection")
	}
	var schema string
	if err = application.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	config.DBName = ""
	observer, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal("cannot open the test lock observer")
	}
	t.Cleanup(func() { observer.Close() })
	return observer, schema
}
