package backup

import (
	"errors"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/readercredentials"
	"strings"
	"testing"
)

func TestLogReaderGrantProfile(t *testing.T) {
	identity := "`backup_01234567890123456789`@`127.0.0.1`"
	allowed := readerGrantTables("studio_test", "studio_authority")
	grants := []string{"GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO " + identity, "GRANT FLUSH_TABLES ON *.* TO " + identity}
	for _, table := range allowed {
		grants = append(grants, "GRANT SELECT ON `"+table.schema+"`.`"+table.name+"` TO "+identity)
	}
	if err := auditReaderGrants(grants, "backup_01234567890123456789", allowed, true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"GRANT SELECT ON *.* TO " + identity,
		"GRANT SELECT ON `studio_test`.* TO " + identity,
		"GRANT RELOAD ON *.* TO " + identity,
		"GRANT SELECT, INSERT ON `studio_authority`.`sources` TO " + identity,
		"GRANT SELECT (`id`) ON `studio_test`.`users` TO " + identity,
		"GRANT SELECT ON `studio_test`.`users` TO " + identity + " WITH GRANT OPTION",
		"GRANT `reader_role`@`%` TO " + identity,
		"GRANT PROXY ON ``@`` TO " + identity,
		"GRANT SELECT ON `mysql`.`user` TO " + identity,
		"GRANT SELECT ON `studio_test`.`users` TO `other`@`127.0.0.1`",
	} {
		t.Run(strings.Split(bad, " TO ")[0], func(t *testing.T) {
			if err := auditReaderGrants(append(append([]string{}, grants...), bad), "backup_01234567890123456789", allowed, true); err == nil {
				t.Fatal("unsafe grant accepted")
			}
		})
	}
	if err := auditReaderGrants(grants[:1], "backup_01234567890123456789", allowed, true); err == nil {
		t.Fatal("incomplete profile accepted")
	}
	if err := auditReaderGrants([]string{"GRANT USAGE ON *.* TO " + identity}, "backup_01234567890123456789", allowed, false); err != nil {
		t.Fatal("safe pending account rejected", err)
	}
}

func TestReaderGeneratedDSNCanonicalRoundtrip(t *testing.T) {
	identity := readercredentials.Identity{User: "backup_01234567890123456789", Database: "studio_test"}
	config := readerConfig(identity, "127.0.0.1:13306", "aA7!"+strings.Repeat("a", 64))
	secret := readercredentials.New(identity, config.FormatDSN())
	if _, err := validatedReaderConfig(secret, "127.0.0.1:13306"); err != nil {
		t.Fatal("our generated protected DSN was rejected", err)
	}
}

func TestReaderSetupErrorsNeverEchoDriverMessage(t *testing.T) {
	for _, cause := range []error{&mysql.MySQLError{Number: 1064, Message: "near IDENTIFIED BY 'private-secret'"}, errors.New("connection private-secret invalid")} {
		if strings.Contains(readerSetupError("account", cause).Error(), "private-secret") {
			t.Fatal("setup exposed driver error")
		}
	}
}

func TestReaderAccountIdentityAndFixedCatalog(t *testing.T) {
	uuid := "12345678-1234-1234-1234-123456789abc"
	a, err := LogReaderAccountName(uuid, "studio_test")
	if err != nil || len(a) > 32 {
		t.Fatal("invalid account name", err)
	}
	b, _ := LogReaderAccountName(uuid, "studio_other")
	if a == b {
		t.Fatal("source accounts collided")
	}
	if _, err = LogReaderAccountName(uuid, "studio`bad"); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
	if readerTableFingerprint("studio_test", "studio_authority") == readerTableFingerprint("studio_test", "other_authority") {
		t.Fatal("authority not bound to catalog")
	}
}

func TestReaderCatalogUpgradeKeepsExactScope(t *testing.T) {
	old := readerGrantTables("studio_test", "studio_authority")
	current := readerGrantTables("studio_test", "studio_authority", retentionSchemaVersion)
	if len(current)-len(old) != len(retentionTableCatalog()) {
		t.Fatal("retention table grant catalog is incomplete")
	}
	oldFingerprint := readerTableFingerprint("studio_test", "studio_authority")
	if oldFingerprint != readerTableFingerprint("studio_test", "studio_authority", supportedSchemaVersion) || oldFingerprint == readerTableFingerprint("studio_test", "studio_authority", retentionSchemaVersion) {
		t.Fatal("schema-specific catalog fingerprint did not preserve old scope")
	}
	for _, versions := range [][]int{{999}, {35, 36}} {
		tables := readerGrantTables("studio_test", "studio_authority", versions...)
		if len(tables) != 0 || readerTableFingerprint("studio_test", "studio_authority", versions...) != "" || auditReaderGrants(nil, "reader", tables, false) == nil {
			t.Fatal("unknown or ambiguous grant catalog did not fail closed")
		}
	}
}
