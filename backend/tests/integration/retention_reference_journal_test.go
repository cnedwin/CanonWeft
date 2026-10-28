package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionReferenceJournalEditRetryAndReversion(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "原始引用正文", "reference-journal-edit")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	ctx := context.Background()
	first, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || first.NewReferences == 0 || first.UpdatedReferences != 0 {
		t.Fatal("material references were not persisted", first, err)
	}
	var refID, born, original string
	var revision, inventory int64
	if err := db.QueryRow("SELECT id,CAST(first_verified_at AS CHAR),payload_hash,revision FROM retention_references WHERE project_id=? AND owner_table='projects' AND field_key='name'", project).Scan(&refID, &born, &original, &revision); err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&inventory); err != nil {
		t.Fatal(err)
	}
	repeat, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || repeat.NewReferences != 0 || repeat.UpdatedReferences != 0 {
		t.Fatal("unchanged replay created a version", repeat, err)
	}
	var replayRevision int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&replayRevision); err != nil || replayRevision != inventory {
		t.Fatal("unchanged replay invalidated the inventory", replayRevision, inventory, err)
	}
	for i, name := range []string{"编辑后的引用正文", "原始引用正文"} {
		if _, err := db.Exec("UPDATE projects SET name=? WHERE id=?", name, project); err != nil {
			t.Fatal(err)
		}
		result, err := w.RegisterRetentionSources(ctx, actor, project)
		if err != nil || result.UpdatedReferences != 1 || result.NewReferences != 0 {
			t.Fatal("source edit was not versioned", result, err)
		}
		var age, payload string
		if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR),payload_hash,revision FROM retention_references WHERE id=?", refID).Scan(&age, &payload, &revision); err != nil || age != born || revision != int64(i+2) {
			t.Fatal("edit reset age or lost its revision", age, revision, err)
		}
		if i == 1 && payload != original {
			t.Fatal("reverting content did not preserve original digest")
		}
	}
	var versions int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE reference_id=?", refID).Scan(&versions); err != nil || versions != 3 {
		t.Fatal("immutable versions missing", versions, err)
	}
	var deadline sql.NullString
	if err := db.QueryRow("SELECT CAST(deadline AS CHAR) FROM retention_references WHERE id=?", refID).Scan(&deadline); err != nil || deadline.Valid {
		t.Fatal("active author text received an expiry", deadline, err)
	}
}

func TestRetentionReferenceJournalAdoptionAndDeletion(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "引用采用与删除", "reference-adoption")
	bid := branch(b, project, "正史")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	payload, err := json.Marshal(domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	digest, candidate := hex.EncodeToString(hash[:]), service.NewID()
	if _, err := db.Exec("INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,status,created_by) VALUES(?,?,'',?,?,'PREPARED',?)", candidate, bid, string(payload), digest, actor); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var id, first string
	if err := db.QueryRow("SELECT id,CAST(first_verified_at AS CHAR) FROM retention_references WHERE project_id=? AND owner_table='ip_revisions' AND field_key='payload'", project).Scan(&id, &first); err != nil {
		t.Fatal(err)
	}
	// The fixture models an author's adoption, not an AI-authored confirmation.
	if _, err := db.Exec("INSERT INTO adoptions(candidate_id,actor_id,content_hash) VALUES(?,?,?)", candidate, actor, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE ip_revisions SET status='READY' WHERE id=?", candidate); err != nil {
		t.Fatal(err)
	}
	adopted, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || adopted.UpdatedReferences != 2 {
		t.Fatal("adoption was not versioned", adopted, err)
	}
	var revision int64
	var root bool
	var deadline sql.NullString
	if err := db.QueryRow("SELECT revision,content_root,CAST(deadline AS CHAR) FROM retention_references WHERE id=?", id).Scan(&revision, &root, &deadline); err != nil || revision != 2 || !root || deadline.Valid {
		t.Fatal("adopted content still expires", revision, root, deadline, err)
	}
	var oldRaw string
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=1", id).Scan(&oldRaw); err != nil {
		t.Fatal(err)
	}
	var old retention.ReferenceVersion
	if err := json.Unmarshal([]byte(oldRaw), &old); err != nil || old.Reference.ContentRoot || old.Deadline.IsZero() {
		t.Fatal("adoption rewrote previous policy", old, err)
	}
	if err := w.DeleteProject(ctx, actor, project, "引用采用与删除", 1, "delete-journal-project"); err != nil {
		t.Fatal(err)
	}
	deleted, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || deleted.UpdatedReferences == 0 || deleted.DeletionReady {
		t.Fatal(deleted, err)
	}
	var currentAge, currentRaw string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR),revision FROM retention_references WHERE id=?", id).Scan(&currentAge, &revision); err != nil || currentAge != first || revision != 3 {
		t.Fatal("deletion reset age", currentAge, revision, err)
	}
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=3", id).Scan(&currentRaw); err != nil {
		t.Fatal(err)
	}
	var current retention.ReferenceVersion
	if err := json.Unmarshal([]byte(currentRaw), &current); err != nil || current.Lifecycle.DeleteSequence == 0 || !current.Deadline.Equal(current.Lifecycle.DeletedAt.Add(7*24*time.Hour)) {
		t.Fatal("protected deletion cap missing", current, err)
	}
}

func TestRetentionReferenceJournalRejectsBrokenProof(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"missing", "DELETE FROM retention_reference_versions WHERE reference_id=?"},
		{"snapshot", "UPDATE retention_reference_versions SET snapshot='{}' WHERE reference_id=?"},
		{"current", "UPDATE retention_references SET payload_hash=REPEAT('0',64) WHERE id=?"},
		{"expired", "UPDATE retention_references SET state='EXPIRED',expired_at=UTC_TIMESTAMP(6) WHERE id=?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, db := setup(t)
			project := createProject(b, "引用证据", "reference-corrupt-"+tc.name)
			actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
			installSourceRegistrationFixture(t, db)
			w, ctx := &service.Workspace{DB: db}, context.Background()
			if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := db.QueryRow("SELECT id FROM retention_references WHERE project_id=? AND owner_table='projects' AND field_key='name'", project).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.statement, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE projects SET name='不应入库的新版本' WHERE id=?", project); err != nil {
				t.Fatal(err)
			}
			if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
				t.Fatal("broken proof accepted", err)
			}
			var values, versions int
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values WHERE source_id=?", id).Scan(&values); err != nil || values != 1 {
				t.Fatal("failed proof leaked observation", values, err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE reference_id=? AND revision>1", id).Scan(&versions); err != nil || versions != 0 {
				t.Fatal("failed proof recreated history", versions, err)
			}
		})
	}
}

func TestRetentionReferenceJournalHistoryFailureRollsBack(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "原子引用记录", "reference-atomic")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	if _, err := db.Exec("CREATE TRIGGER reject_reference_version BEFORE INSERT ON retention_reference_versions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected history failure'"); err != nil {
		t.Fatal(err)
	}
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("failed history accepted", err)
	}
	for _, table := range []string{"project_lifecycle", "retention_source_slots", "retention_source_values", "retention_references", "retention_reference_versions"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial journal committed", table, count, err)
		}
	}
	if _, err := db.Exec("DROP TRIGGER reject_reference_version"); err != nil {
		t.Fatal(err)
	}
	result, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || result.NewReferences == 0 {
		t.Fatal("safe retry failed", result, err)
	}
}

func TestRetentionReferenceJournalMissingSourceBlocksWithoutErasing(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "消失来源", "reference-disappearing")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	initial, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM operations WHERE actor_id=?", actor); err != nil {
		t.Fatal(err)
	}
	missing, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || missing.UnresolvedReferences == 0 || missing.DeletionReady {
		t.Fatal("missing source ignored", missing, err)
	}
	var state string
	var revision, next int64
	if err := db.QueryRow("SELECT state,inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&state, &revision); err != nil || state != "BLOCKED" {
		t.Fatal(state, err)
	}
	replay, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || replay.UnresolvedReferences != missing.UnresolvedReferences {
		t.Fatal(replay, err)
	}
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&next); err != nil || next != revision {
		t.Fatal("unresolved replay churned revision", next, revision, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=?", project).Scan(&count); err != nil || count != initial.NewReferences {
		t.Fatal("missing evidence erased", count, initial, err)
	}
}
