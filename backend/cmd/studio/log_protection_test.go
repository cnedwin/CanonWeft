package main

import "testing"

func TestLogProtectionConnectionIsLocalReadAccountAndExactDatabase(t *testing.T) {
	for _, raw := range []string{
		"root:do-not-print@tcp(127.0.0.1:13306)/studio_test",
		"backup:do-not-print@tcp(remote.invalid:3306)/studio_test",
		"backup:do-not-print@tcp(127.0.0.1:13306)/studio",
		"backup:do-not-print@tcp(127.0.0.1:13306)/studio_test?multiStatements=true",
		"backup:do-not-print@tcp(127.0.0.1:13306)/studio_test?allowAllFiles=true",
	} {
		if _, err := logProtectionConnection(raw, "studio_test"); err == nil {
			t.Fatal("unsafe collector connection accepted")
		}
	}
	cfg, err := logProtectionConnection("backup:do-not-print@tcp(127.0.0.1:13306)/studio_test?parseTime=true", "studio_test")
	if err != nil || cfg.DBName != "studio_test" || !cfg.ParseTime || cfg.MultiStatements || cfg.AllowAllFiles {
		t.Fatal("valid local collector unavailable", err)
	}
}
