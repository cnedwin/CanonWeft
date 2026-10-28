package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProtectionHealthKeepsFailureSeparateFromLastRecoveryPoint(t *testing.T) {
	now := time.Now().UTC()
	p, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Import(context.Background(), p, now, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	result := ProtectionTick{CheckedAt: now.Add(time.Minute), State: "CAPTURE_FAILED", Record: record}
	if err := store.WriteHealth(result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Path, "health.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved ProtectionTick
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Record.SnapshotAt != record.SnapshotAt || saved.State != "CAPTURE_FAILED" {
		t.Fatalf("failed attempt erased or advanced recovery point: %+v %v", saved, err)
	}
	view := ProtectionViewFromTick(saved, now.Add(16*time.Minute))
	if view.CheckpointState != "STALE" || view.AttemptState != "CAPTURE_FAILED" {
		t.Fatalf("stale checkpoint hidden behind recent attempt: %+v", view)
	}
	view = ProtectionViewFromTick(ProtectionTick{State: "HEALTH_WRITE_FAILED", CheckpointState: "CORRUPT", Record: record}, now)
	if view.CheckpointState != "CORRUPT" {
		t.Fatal("health reporting error must not hide corrupt recovery point")
	}
}
