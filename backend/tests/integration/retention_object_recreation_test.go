package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	"novelstudio/internal/objectwriter"
	"novelstudio/internal/storage"
)

type failingRecreationStore struct{ *storage.LocalRetentionStore }

func (s failingRecreationStore) BindRecreationWrite() (storage.BlobStore, string, func() error, error) {
	return s, s.RetentionNamespace(), nil, nil
}
func (s failingRecreationStore) PutObject(context.Context, string, io.Reader) (storage.BlobRef, error) {
	return storage.BlobRef{}, errors.New("injected write failure")
}

func TestRetentionObjectRecreationRequiresCompletedBoundReceipt(t *testing.T) {
	scanner, files, db, actor, project, preview, hash, _ := prepareObjectCleanup(t)
	ctx := context.Background()
	data := []byte("old unused file")
	attempt := func(store storage.BlobStore, mutate func(*sql.Tx)) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if mutate != nil {
			mutate(tx)
		}
		_, err = objectwriter.Write(ctx, tx, store, project, data)
		return err
	}
	preparePhysicalClaim(t, scanner, db, preview.ID)
	if err := attempt(files, nil); err == nil {
		t.Fatal("write raced prepared deletion")
	}
	if _, err := scanner.AdvanceJob(ctx, preview.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow("SELECT state FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&state); err != nil || state != "DELETED" {
		t.Fatal("fixture did not delete", state, err)
	}
	if err := attempt(files, nil); err == nil {
		t.Fatal("write raced unfinished job")
	}
	finishPhysicalJob(t, scanner, preview.ID)
	var revision int64
	var before string
	if err := db.QueryRow("SELECT revision,CAST(first_seen_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&revision, &before); err != nil {
		t.Fatal(err)
	}
	readReceipts := func() string {
		t.Helper()
		var digest string
		if err := db.QueryRow("SELECT GROUP_CONCAT(CONCAT(phase,':',result,':',evidence_hash,':',recorded_at) ORDER BY phase SEPARATOR '|') FROM retention_receipts WHERE job_id=? AND item_key=?", preview.ID, "object:"+hash).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	receipts := readReceipts()
	if err := attempt(storage.LocalBlobs{Root: t.TempDir()}, nil); err == nil {
		t.Fatal("accepted different physical namespace")
	}
	for _, mutation := range []string{
		"UPDATE retention_receipts SET evidence_hash=REPEAT('0',64) WHERE job_id=? AND phase='VERIFIED'",
		"DELETE FROM retention_receipts WHERE job_id=? AND phase='REMOVED'",
		"UPDATE retention_jobs SET restored_disabled=1 WHERE id=?",
	} {
		if err := attempt(files, func(tx *sql.Tx) {
			if _, err := tx.Exec(mutation, preview.ID); err != nil {
				t.Fatal(err)
			}
		}); err == nil {
			t.Fatal("accepted incomplete or invalid deletion proof", mutation)
		}
	}
	if err := attempt(failingRecreationStore{files.LocalRetentionStore}, nil); err == nil {
		t.Fatal("write failure ignored")
	}
	if err := db.QueryRow("SELECT state FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&state); err != nil || state != "DELETED" {
		t.Fatal("failed write changed committed state", state, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ref, err := objectwriter.Write(ctx, tx, storage.LocalBlobs{Root: files.root}, project, data)
	if err != nil || ref.Hash != hash {
		t.Fatal("explicit recreation", ref, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var after string
	var newRevision int64
	var protected bool
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR),revision,state='PRESENT' AND deleted_at IS NULL AND lease_until>UTC_TIMESTAMP(6) FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&after, &newRevision, &protected); err != nil || after <= before || newRevision != revision+1 || !protected {
		t.Fatal("missing fresh incarnation", after, newRevision, protected, err)
	}
	if readReceipts() != receipts {
		t.Fatal("new write changed historical receipts")
	}
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal("new incarnation not scannable", err)
	}
	_, _ = scanner.AdvanceJob(ctx, preview.ID)
	contents, err := files.ReadObject(ctx, project, hash)
	if err != nil || string(contents) != string(data) || files.deletes != 1 {
		t.Fatal("old job deleted new bytes", files.deletes, err)
	}
	var firstAgain string
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&firstAgain); err != nil || strings.Compare(firstAgain, after) != 0 {
		t.Fatal("scan renewed age", firstAgain, err)
	}
}
