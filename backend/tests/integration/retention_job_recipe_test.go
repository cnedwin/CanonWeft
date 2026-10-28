package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionJobExpiresRecipeMaterial(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "prepared"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			transport := &generationTransport{}
			_, recipes, actor, project, input := recipeFixture(t, transport)
			ctx := context.Background()
			db := recipes.Generations.IP.Workspace.DB
			run, err := recipes.Prepare(ctx, actor, project, input)
			if err != nil {
				t.Fatal(err)
			}
			if completed {
				run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if _, err = recipes.Advance(ctx, run.ID); err != nil {
						t.Fatal(err)
					}
					run, err = recipes.Read(ctx, actor, project, run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if state, err := recipes.Generations.Execute(ctx, run.Steps[i].GenerationID); err != nil || state != "SUCCEEDED" {
						t.Fatal(state, err)
					}
					if _, err = recipes.Advance(ctx, run.ID); err != nil {
						t.Fatal(err)
					}
					run, err = recipes.Read(ctx, actor, project, run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision, run.Steps[0].OutputHash); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			calls := transport.calls.Load()
			installCompleteRetentionFixture(t, db)
			var raw string
			if err = db.QueryRow("SELECT frozen FROM recipe_runs WHERE id=?", run.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			key, _ := retention.RowOwnerKey("recipe_runs", map[string]string{"id": run.ID})
			id, _ := retention.ReferenceID(project, "recipe_runs", key, "frozen")
			hash := sha256.Sum256([]byte(raw))
			if _, err = db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'recipe_runs',?,'frozen','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, key); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
				t.Fatal(err)
			}
			for _, step := range run.Steps {
				key, _ := retention.RowOwnerKey("recipe_steps", map[string]string{"id": step.ID})
				ref, _ := retention.ReferenceID(project, "recipe_steps", key, "step_key")
				hash := sha256.Sum256([]byte(step.Key))
				if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'recipe_steps',?,'step_key','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, key); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
					t.Fatal(err)
				}
			}
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &service.RetentionScanner{Workspace: recipes.Generations.IP.Workspace, Blobs: store}
			job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
			if err != nil || job.Expiring < 3 {
				t.Fatal("recipe sources not planned", job, err)
			}
			if !completed {
				assertRecipeExpiryBindings(t, db, project, job.ID, run, false)
			}
			if _, err = scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
				t.Fatal(err)
			}
			done := false
			checkedPartialOutput := false
			for i := 0; i < 60; i++ {
				state, err := scanner.AdvanceJob(ctx, job.ID)
				if err != nil {
					tx, beginErr := db.BeginTx(ctx, nil)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					_, registrationErr := retention.RegisterSourceObservations(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, store.ReadObject)
					tx.Rollback()
					var cursor string
					db.QueryRow("SELECT CONCAT(r.owner_table,'.',r.field_key) FROM retention_jobs j LEFT JOIN retention_references r ON r.id=j.cursor_key WHERE j.id=?", job.ID).Scan(&cursor)
					t.Fatal("recipe cleanup failed", state, err, "after", cursor, "registration", registrationErr)
				}
				if state == "BLOCKED" {
					var reason string
					db.QueryRow("SELECT reason_code FROM retention_jobs WHERE id=?", job.ID).Scan(&reason)
					t.Fatal("recipe cleanup blocked", reason)
				}
				if completed {
					for _, step := range run.Steps {
						stepKey, _ := retention.RowOwnerKey("recipe_steps", map[string]string{"id": step.ID})
						outputID, _ := retention.ReferenceID(project, "recipe_steps", stepKey, "output_hash")
						var outputState, frozen string
						if err := db.QueryRow("SELECT r.state,s.frozen FROM retention_references r JOIN recipe_steps s ON s.id=? WHERE r.id=?", step.ID, outputID).Scan(&outputState, &frozen); err != nil {
							t.Fatal(err)
						}
						if outputState != "EXPIRED" {
							continue
						}
						if _, err := retention.ParseSourceProjection([]byte(frozen), "recipe_steps.frozen"); err == nil {
							continue
						}
						checkedPartialOutput = true
						history, err := recipes.Read(ctx, actor, project, run.ID)
						if err != nil {
							t.Fatal("partial expiry history unreadable", err)
						}
						for _, actual := range history.Steps {
							if actual.ID == step.ID && (actual.Retention == nil || len(actual.Output) != 0 || actual.OutputText != "" || actual.InputText != "") {
								t.Fatal("partially expired step still exposes executable material")
							}
						}
					}
				}
				if state == "COMPLETE" {
					done = true
					break
				}
			}
			if !done {
				t.Fatal("recipe cleanup never completed")
			}
			if completed && !checkedPartialOutput {
				t.Fatal("partial output expiry was not exercised")
			}
			assertRecipeExpiryBindings(t, db, project, job.ID, run, true)
			history, err := recipes.Read(ctx, actor, project, run.ID)
			if err != nil || history.Retention == nil || history.Retention.State != "EXPIRED" {
				t.Fatal("expired recipe history unavailable", err)
			}
			if _, err = recipes.Start(ctx, actor, project, run.ID, run.Revision); err == nil {
				t.Fatal("expired recipe can start")
			}
			for _, step := range run.Steps {
				var raw string
				var key string
				if err := db.QueryRow("SELECT step_key FROM recipe_steps WHERE id=?", step.ID).Scan(&key); err != nil {
					t.Fatal(err)
				}
				if _, ok := retention.RetiredLabelHash(key); !ok {
					t.Fatal("step key not erased")
				}
				if err = db.QueryRow("SELECT frozen FROM recipe_steps WHERE id=?", step.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if _, err = retention.ParseSourceProjection([]byte(raw), "recipe_steps.frozen"); err != nil {
					t.Fatal("step payload not erased", err)
				}
				for _, hash := range []string{step.InputHash, step.OutputHash} {
					if hash != "" {
						if _, err = store.ReadObject(ctx, project, hash); err == nil {
							t.Fatal("expired step object retained")
						}
					}
				}
			}
			if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-recipe.zip")); err != nil {
				t.Fatal("expired recipe archive", err)
			}
			if transport.calls.Load() != calls {
				t.Fatal("cleanup called a model")
			}
		})
	}
}

func assertRecipeExpiryBindings(t *testing.T, db *sql.DB, project, job string, run service.RecipeExecution, replay bool) {
	t.Helper()
	ctx := context.Background()
	step := run.Steps[len(run.Steps)-1]
	v := capturedVersion(t, db, project, "recipe_steps", step.ID, "frozen")
	revision := v.Reference.Revision
	if replay {
		revision = v.Expiration.PriorRevision
	}
	expiry := retention.Expiry{ReferenceID: v.Reference.ID, Revision: revision, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Deadline: v.Deadline, Storage: retention.Inline}
	var planRaw []byte
	var plan retention.Plan
	if err := db.QueryRow("SELECT plan FROM retention_jobs WHERE id=?", job).Scan(&planRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		t.Fatal(err)
	}
	for _, planned := range plan.Expire {
		if planned.ReferenceID == expiry.ReferenceID {
			expiry = planned
		}
	}
	var raw, hash string
	if err := db.QueryRow("SELECT frozen,frozen_hash FROM recipe_steps WHERE id=?", step.ID).Scan(&raw, &hash); err != nil {
		t.Fatal(err)
	}
	var proof retention.ExpiredProjection
	var err error
	if replay {
		proof, err = retention.ParseSourceProjection([]byte(raw), expiry.Rule)
	} else {
		summary, summaryErr := retention.RecipeEvidence("recipe_steps", step.ID, run.ID, []byte(raw), hash)
		if summaryErr != nil {
			t.Fatal(summaryErr)
		}
		proof, err = retention.ProjectExpired(expiry, []byte(raw), summary, time.Now().UTC().Truncate(time.Microsecond))
	}
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{"unchanged", "step_key", "frozen_hash"}
	if !replay {
		cases = append(cases, "running_parent")
	}
	for _, change := range cases {
		t.Run(change, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var inventory int64
			if err = tx.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&inventory); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "step_key":
				_, err = tx.Exec("UPDATE recipe_steps SET step_key='different_step' WHERE id=?", step.ID)
			case "frozen_hash":
				_, err = tx.Exec("UPDATE recipe_steps SET frozen_hash=? WHERE id=?", strings.Repeat("e", 64), step.ID)
			case "running_parent":
				_, err = tx.Exec("UPDATE recipe_runs SET state='RUNNING' WHERE id=?", run.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = retention.ApplySourceExpiryTx(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, retention.SourceKey{Table: "recipe_steps", Primary: map[string]string{"id": step.ID}}, inventory, []retention.Expiry{expiry}, []retention.ExpiredProjection{proof})
			if change == "unchanged" && err != nil {
				t.Fatal("valid recipe proof rejected", err)
			}
			if change != "unchanged" && err == nil {
				t.Fatal("recipe expiry ignored actual owner or active parent")
			}
		})
	}
}
