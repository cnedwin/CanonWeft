package integration

import (
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/backup"
	"novelstudio/internal/httpapi"
)

func TestProtectionStatusIsOwnerOnlyAndHonorsCurrentSession(t *testing.T) {
	owner, db := setup(t)
	view := func(now time.Time) backup.ProtectionView {
		return backup.ProtectionViewFromTick(backup.ProtectionTick{State: "CAPTURE_FAILED", CheckedAt: now}, now)
	}
	httpapi.RegisterProtection(owner.handler.(*echo.Echo), db, view)
	path := "/api/operations/protection"
	anonymous := &browser{t: t, handler: owner.handler}
	anonymous.request("GET", path, nil, 401)
	owner.request("POST", "/api/members", map[string]any{"username": "backup-reader", "password": "backup-reader-password-123", "role": "reader"}, 201)
	reader := &browser{t: t, handler: owner.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "backup-reader", "password": "backup-reader-password-123"}, 200)
	reader.request("GET", path, nil, 403)
	result := owner.request("GET", path, nil, 200)
	if result["checkpoint_state"] != "MISSING" || result["log_recovery"] != "NOT_QUALIFIED" {
		t.Fatalf("status overclaims recovery readiness: %+v", result)
	}
	if _, err := db.Exec("UPDATE users SET enabled=FALSE WHERE username='owner'"); err != nil {
		t.Fatal(err)
	}
	owner.request("GET", path, nil, 401)
}
