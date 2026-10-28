package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/proto"
	platformworker "novelstudio/internal/worker"
)

type maintenanceRestartRecord struct {
	ID, RunID, HistoryHash string
	Events                 int64
}

// The driver stops and restarts the real Temporal server between these phases.
// It uses the isolated namespace from TestRetentionConfirmedJobRunsAfterRestart,
// where real production activities have already executed and scheduled timers.
func TestMaintenanceServerRestart(t *testing.T) {
	phase := os.Getenv("STUDIO_MAINTENANCE_PROBE_PHASE")
	if phase == "" {
		t.Skip("two-process service restart probe")
	}
	namespace, path := os.Getenv("STUDIO_MAINTENANCE_PROBE_NAMESPACE"), os.Getenv("STUDIO_MAINTENANCE_PROBE_FILE")
	if !strings.HasPrefix(namespace, "cleanup-") || len(namespace) != 40 || path == "" || (phase != "start" && phase != "finish") {
		t.Fatal("invalid isolated probe configuration")
	}
	if _, err := hex.DecodeString(namespace[8:]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: os.Getenv("STUDIO_TEMPORAL"), Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	records := []maintenanceRestartRecord{{ID: "periodic-material-maintenance"}, {ID: "recovery-object-maintenance"}, {ID: "execution-temporary-maintenance"}}
	if phase == "finish" {
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &records) != nil || len(records) != 3 {
			t.Fatal("probe evidence missing", err)
		}
	}
	for index := range records {
		record := &records[index]
		description, err := c.DescribeWorkflowExecution(ctx, record.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		info := description.WorkflowExecutionInfo
		if info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			t.Fatal("maintenance no longer waiting", record.ID, info.Status)
		}
		runID := info.Execution.RunId
		if phase == "finish" && record.RunID != runID {
			t.Fatal("restart replaced persisted run", record.ID)
		}
		hash, count, timer := sha256.New(), int64(0), false
		history := c.GetWorkflowHistory(ctx, record.ID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for history.HasNext() {
			event, err := history.Next()
			if err != nil {
				t.Fatal(err)
			}
			if phase == "finish" && event.EventId > record.Events {
				break
			}
			data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = hash.Write(data)
			count = event.EventId
			if event.EventType == enumspb.EVENT_TYPE_TIMER_STARTED {
				timer = true
			}
		}
		if !timer {
			t.Fatal("maintenance has no durable timer", record.ID)
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		if phase == "finish" && (record.Events != count || record.HistoryHash != digest) {
			t.Fatal("persisted maintenance history changed", record.ID)
		}
		*record = maintenanceRestartRecord{ID: record.ID, RunID: runID, HistoryHash: digest, Events: count}
	}
	if phase == "start" {
		raw, _ := json.Marshal(records)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	// Query a fresh worker: state must be rebuilt from production history,
	// without executing a second scan or registering replacement activities.
	w := temporalworker.New(c, platformworker.ReviewQueue, temporalworker.Options{})
	w.RegisterWorkflow(platformworker.PeriodicCleanupWorkflow)
	w.RegisterWorkflow(platformworker.RecoveryCleanupWorkflow)
	w.RegisterWorkflow(platformworker.TemporaryCleanupWorkflow)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	value, err := c.QueryWorkflow(ctx, records[0].ID, records[0].RunID, "status")
	if err != nil {
		t.Fatal("state could not replay after restart", err)
	}
	var state struct{ Last struct{ State string } }
	if err := value.Get(&state); err != nil || state.Last.State != "PAUSED" {
		t.Fatal("disabled automatic cleanup changed after restart", state, err)
	}
	for _, record := range records {
		if err := c.TerminateWorkflow(ctx, record.ID, record.RunID, "isolated maintenance restart probe complete"); err != nil {
			t.Fatal(err)
		}
	}
}
