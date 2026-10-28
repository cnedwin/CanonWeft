package main

import (
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

func applicationDatabaseConfig(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	// loc only controls Go decoding. A session setting is also necessary for
	// CURRENT_TIMESTAMP defaults and every connection created by the pool.
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg, nil
}
