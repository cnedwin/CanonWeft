package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

// Full workspace restores retain exact IDs and can resume. A project copy has
// new book/source/resource identities: preserve its import ledger for inspection,
// but require a fresh import before more writes instead of claiming equivalence.
func (s *IP) copyRecoveryManuscriptsTx(ctx context.Context, tx *sql.Tx, a domain.Actor, ids RecoveryResourceIDs, books map[string]string) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.kind,d.body FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.kind IN ('novel_manuscript_import','novel_manuscript_progress','novel_chapter_control')`, a.ID, ids.TargetProject)
	if err != nil {
		return err
	}
	type row struct{ id, kind, body string }
	values := []row{}
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.kind, &v.body); err != nil {
			rows.Close()
			return err
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range values {
		if rejectExpiredRuntime([]byte(v.body), "resource_drafts.body") != nil {
			continue
		}
		var body []byte
		name := ""
		switch v.kind {
		case manuscriptKind:
			var d ManuscriptDocument
			if decodeRecoveryDocument([]byte(v.body), &d) != nil || d.Schema != manuscriptSchema {
				return domain.ErrNotReady
			}
			d.BookID = books[d.BookID]
			d.SourceID = ids.Imports[d.SourceID]
			d.Recovered = true
			d.OperationID = ""
			d.RequestHash = ""
			if d.BookID == "" || d.SourceID == "" {
				return domain.ErrNotReady
			}
			for i := range d.Parts {
				if d.Parts[i].Target != nil {
					d.Parts[i].Target.ControlID = ids.Resources[d.Parts[i].Target.ControlID]
					if d.Parts[i].Target.ControlID == "" {
						return domain.ErrNotReady
					}
				}
			}
			body, err = json.Marshal(d)
			name = "长稿导入 · " + v.id
		case manuscriptProgressKind:
			var d ManuscriptProgress
			if decodeRecoveryDocument([]byte(v.body), &d) != nil || d.Schema != "novel-manuscript-progress-v1" {
				return domain.ErrNotReady
			}
			d.BatchID = ids.Resources[d.BatchID]
			if d.BatchID == "" {
				return domain.ErrNotReady
			}
			if d.ControlID != "" {
				d.ControlID = ids.Resources[d.ControlID]
				if d.ControlID == "" {
					return domain.ErrNotReady
				}
			}
			d.OperationID = ""
			d.RequestHash = ""
			body, err = json.Marshal(d)
			name = "导入进度 · " + d.BatchID + " · " + d.PartID
		case "novel_chapter_control":
			d, e := novel.DecodeChapter([]byte(v.body))
			if e != nil || !d.Unplanned {
				continue
			}
			d.BodyID = ids.Resources[d.BodyID]
			if d.BodyID == "" {
				return domain.ErrNotReady
			}
			if d.IPHead != "" {
				d.IPHead = ids.Revisions[d.IPHead]
				if d.IPHead == "" {
					return domain.ErrNotReady
				}
			}
			body, err = json.Marshal(d)
		}
		if err != nil {
			return err
		}
		if body == nil {
			continue
		}
		if err = s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: "resource_drafts", Primary: map[string]string{"resource_id": v.id, "actor_id": a.ID}}); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", body, v.id, a.ID); err != nil {
			return err
		}
		if err = s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: "resource_drafts", Primary: map[string]string{"resource_id": v.id, "actor_id": a.ID}}); err != nil {
			return err
		}
		if name != "" {
			if _, err = tx.ExecContext(ctx, "UPDATE resources SET name=? WHERE id=?", name, v.id); err != nil {
				return err
			}
			if err = captureMetadataSourceTx(ctx, tx, ids.TargetProject, "resources", v.id); err != nil {
				return err
			}
		}
	}
	return nil
}
