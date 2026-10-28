package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type JointHistoryMember struct {
	ReleaseID    string `json:"release_id"`
	ManifestHash string `json:"manifest_hash"`
	BranchID     string `json:"branch_id"`
}

type JointHistoryItem struct {
	ID        string               `json:"id"`
	Recovered bool                 `json:"recovered"`
	Members   []JointHistoryMember `json:"members"`
}

type JointHistoryPage struct {
	Items []JointHistoryItem `json:"items"`
	Next  string             `json:"next"`
}

// IDs are ordered lexically, not by an invented publication time. Read both
// markers and members so a damaged group cannot silently disappear from history.
func jointGroupIDsTx(ctx context.Context, tx *sql.Tx, project, after string, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.request_hash FROM operations o
 JOIN artifact_releases r ON r.id=o.result_id WHERE r.project_id=? AND o.request_hash>?
 AND o.kind IN ('release.joint-member','release.joint-publish','release.joint-restored')
 ORDER BY o.request_hash LIMIT ?`, project, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *IP) ListJointPublications(ctx context.Context, actor domain.Actor, project, after string) (JointHistoryPage, error) {
	result := JointHistoryPage{Items: []JointHistoryItem{}}
	if after != "" && (len(after) != 64 || !lowerHex32(after[:32]) || !lowerHex32(after[32:])) {
		return result, domain.ErrValidation
	}
	tx, _, _, err := s.begin(ctx, actor, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	ids, err := jointGroupIDsTx(ctx, tx, project, after, 21)
	if err != nil {
		return result, err
	}
	if len(ids) > 20 {
		ids = ids[:20]
		result.Next = ids[len(ids)-1]
	}
	for _, id := range ids {
		group, err := s.readJointPublicationTx(ctx, tx, project, id)
		if err != nil {
			return JointHistoryPage{}, err
		}
		item := JointHistoryItem{ID: id, Recovered: group.Recovered, Members: []JointHistoryMember{}}
		for _, release := range group.Releases {
			item.Members = append(item.Members, JointHistoryMember{ReleaseID: release.ID, ManifestHash: release.Hash, BranchID: release.Manifest.BranchID})
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

type recoveredJointGroup struct {
	ID   string
	Pins []JointReleasePin
}

func (s *IP) recoveryJointGroupsTx(ctx context.Context, tx *sql.Tx, ids RecoveryResourceIDs, manifests map[string]domain.Manifest) ([]recoveredJointGroup, error) {
	groups, err := jointGroupIDsTx(ctx, tx, ids.SourceProject, "", len(ids.Releases)+1)
	if err != nil {
		return nil, err
	}
	if len(groups) > len(ids.Releases) {
		return nil, domain.ErrNotReady
	}
	result := []recoveredJointGroup{}
	for _, id := range groups {
		original, err := s.readJointPublicationTx(ctx, tx, ids.SourceProject, id)
		if err != nil {
			if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrConflict) {
				return nil, domain.ErrNotReady
			}
			return nil, err
		}
		group := recoveredJointGroup{Pins: []JointReleasePin{}}
		for _, release := range original.Releases {
			manifest, ok := manifests[release.ID]
			mapped := ids.Releases[release.ID]
			if !ok || mapped == "" || manifest.ReleaseID != mapped {
				return nil, domain.ErrNotReady
			}
			group.Pins = append(group.Pins, JointReleasePin{ReleaseID: mapped, ManifestHash: valueHash(manifest)})
		}
		slices.SortFunc(group.Pins, func(a, b JointReleasePin) int { return strings.Compare(a.ReleaseID, b.ReleaseID) })
		group.ID = jointPublicationHash(ids.TargetProject, group.Pins)
		result = append(result, group)
	}
	return result, nil
}

// Caller owns the recovery savepoint. All records get fresh identities; neither
// the original actor's action key nor a replayable publish receipt is restored.
func (s *IP) writeRecoveredJointGroupsTx(ctx context.Context, tx *sql.Tx, actor, project string, groups []recoveredJointGroup) error {
	keys := []retention.SourceKey{}
	for _, group := range groups {
		for i := 0; i <= len(group.Pins); i++ {
			kind, release := "release.joint-member", group.Pins[0].ReleaseID
			if i < len(group.Pins) {
				release = group.Pins[i].ReleaseID
			} else {
				kind = "release.joint-restored"
			}
			key := "request-v2:" + NewID()
			if _, err := tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,?,?,?)", actor, key, kind, group.ID, release); err != nil {
				return err
			}
			keys = append(keys, retention.SourceKey{Table: "operations", Primary: map[string]string{"actor_id": actor, "operation_id": key}})
		}
	}
	for offset := 0; offset < len(keys); offset += 256 {
		if err := s.captureSourceValuesTx(ctx, tx, project, keys[offset:min(offset+256, len(keys))]...); err != nil {
			return err
		}
	}
	return nil
}
