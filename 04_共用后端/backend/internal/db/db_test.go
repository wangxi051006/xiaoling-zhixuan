package db

import (
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestNormalizeUTCConfig(t *testing.T) {
	for _, dsn := range []string{
		"worker:placeholder@tcp(127.0.0.1:3306)/campus_test",
		"worker:placeholder@tcp(127.0.0.1:3306)/campus_test?loc=Local&time_zone=%27%2B08%3A00%27&charset=utf8mb4&multiStatements=true&timeout=7s",
	} {
		normalized, err := normalizeUTCConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := mysql.ParseDSN(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Loc != time.UTC || !cfg.ParseTime || cfg.Params["time_zone"] != "'+00:00'" {
			t.Fatal("driver and session did not both normalize to UTC")
		}
		original, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.User != original.User || cfg.Passwd != original.Passwd || cfg.DBName != original.DBName || cfg.Net != original.Net || cfg.Addr != original.Addr || cfg.MultiStatements != original.MultiStatements || cfg.Timeout != original.Timeout || cfg.Params["charset"] != original.Params["charset"] {
			t.Fatal("unrelated connection options changed")
		}
	}
}
