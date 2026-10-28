package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type unavailableOutputStore struct{ storage.BlobStore }

type failLaterObjectStore struct {
	storage.BlobStore
	writes int
	failAt int
}

func (s *failLaterObjectStore) PutObject(ctx context.Context, project string, input io.Reader) (storage.BlobRef, error) {
	s.writes++
	if s.writes == s.failAt {
		return storage.BlobRef{}, errors.New("simulated later object failure")
	}
	return s.BlobStore.PutObject(ctx, project, input)
}

func (s unavailableOutputStore) PutObject(context.Context, string, io.Reader) (storage.BlobRef, error) {
	return storage.BlobRef{}, errors.New("simulated output storage outage")
}

func TestRetentionGenerationReceiptAndOutputCommitTogether(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "saved"
		if failed {
			name = "storage-failed"
		}
		t.Run(name, func(t *testing.T) {
			transport := &generationTransport{response: generationSuccess}
			_, svc, actor, project, task := generationFixture(t, transport)
			db, ctx := svc.IP.Workspace.DB, context.Background()
			installSourceRegistrationFixture(t, db)
			if failed {
				svc.IP.Blobs = unavailableOutputStore{svc.IP.Blobs}
			}
			if _, err := svc.Start(ctx, actor, project, task.ID); err != nil {
				t.Fatal(err)
			}
			wantState := "SUCCEEDED"
			if failed {
				wantState = "INVALID_OUTPUT"
			}
			if state, err := svc.Execute(ctx, task.ID); err != nil || state != wantState {
				t.Fatal(state, err)
			}
			result, err := svc.Read(ctx, actor, project, task.ID)
			if err != nil || result.Call.Charged != 31 || transport.calls.Load() != 1 {
				t.Fatal("receipt lost or duplicated", result.Call, err)
			}
			var count int
			if failed {
				if result.Call.OutputHash != "" {
					t.Fatal("failed object adopted")
				}
				if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=?", project).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed write committed false PRESENT object", count, err)
				}
			} else {
				if result.Output == nil || result.Output.Text != "测试候选" {
					t.Fatal("successful output lost")
				}
				if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=? AND object_hash=? AND state='PRESENT' AND lease_until>UTC_TIMESTAMP(6)", project, result.Call.OutputHash).Scan(&count); err != nil || count != 1 {
					t.Fatal("receipt committed without output protection", count, err)
				}
			}
			var spent, reserved int64
			if err := db.QueryRow("SELECT spent_micros,reserved_micros FROM model_budgets WHERE workspace_id=?", result.WorkspaceID).Scan(&spent, &reserved); err != nil || spent != 31 || reserved != 0 {
				t.Fatal("known usage not settled exactly once", spent, reserved, err)
			}
			for _, state := range []string{"RESERVED", "DISPATCHED", wantState} {
				if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='model_calls' AND r.field_key='accounting' AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.accounting.summary.state'))=?", project, state).Scan(&count); err != nil || count < 1 {
					t.Fatal("call transition lacks immutable accounting proof", state, count, err)
				}
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='model_call_receipts' AND field_key='metadata'", project).Scan(&count); err != nil || count != 1 {
				t.Fatal("settlement committed without receipt provenance", count, err)
			}
		})
	}
}

func TestRetentionReleaseFilesShareReferenceTransaction(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "发布文件保护", "release-writer")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "章节草稿")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	if err := db.QueryRow("SELECT workspace_id,role FROM users WHERE id=?", actor.ID).Scan(&actor.WorkspaceID, &actor.Role); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	input := domain.ReleaseInput{BranchID: bid, OperationID: "protected-release", Updates: []domain.ResourceUpdate{{ResourceID: rid, Text: "灯塔守夜人发现了迟到的船。"}}}
	release, err := ip.PrepareRelease(context.Background(), actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{release.Hash, release.Manifest.Artifacts[0].Hash} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=? AND object_hash=? AND state='PRESENT' AND lease_until>UTC_TIMESTAMP(6)", project, hash).Scan(&count); err != nil || count != 1 {
			t.Fatal("release file missing protection", count, err)
		}
	}
	input.OperationID = "failed-release"
	input.Updates[0].Text = "应当回滚的新正文"
	ip.Blobs = &failLaterObjectStore{BlobStore: ip.Blobs, failAt: 2}
	if _, err := ip.PrepareRelease(context.Background(), actor, project, input); err == nil {
		t.Fatal("failed body write was adopted")
	}
	var releases, objects int
	if err := db.QueryRow("SELECT COUNT(*) FROM artifact_releases WHERE project_id=?", project).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=?", project).Scan(&objects); err != nil || releases != 1 || objects != 2 {
		t.Fatal("partial release committed", releases, objects, err)
	}
}

func TestRetentionSkillArchiveAndBundleCommitTogether(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "方法包保存保护", "skill-archive-writer")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	installSourceRegistrationFixture(t, db)
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	svc := &service.Skills{IP: ip}
	archive := func(suffix string) []byte {
		t.Helper()
		var raw bytes.Buffer
		writer := zip.NewWriter(&raw)
		file, err := writer.Create("SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("---\nname: source-review\ndescription: Use when checking source evidence.\nlicense: LicenseRef-Private\n---\nPreserve evidence. " + suffix)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return raw.Bytes()
	}
	version, err := svc.Import(context.Background(), actor, project, "archive-ok", archive("First version."))
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{version.Report.Hash, version.Source.ArchiveHash} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=? AND object_hash=? AND state='PRESENT' AND lease_until>UTC_TIMESTAMP(6)", project, hash).Scan(&count); err != nil || count != 1 {
			t.Fatal("method source lacks protection", count, err)
		}
	}
	ip.Blobs = &failLaterObjectStore{BlobStore: ip.Blobs, failAt: 2}
	if _, err := svc.Import(context.Background(), actor, project, "archive-failed", archive("Different version.")); err == nil {
		t.Fatal("partial method package adopted")
	}
	var packages, objects int
	if err := db.QueryRow("SELECT COUNT(*) FROM skill_packages WHERE project_id=?", project).Scan(&packages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=?", project).Scan(&objects); err != nil || packages != 1 || objects != 2 {
		t.Fatal("partial archive references committed", packages, objects, err)
	}
}
