package service

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

type JointReleasePin = domain.ReleasePin
type JointPublicationInput struct {
	OperationID string            `json:"operation_id"`
	Releases    []JointReleasePin `json:"releases"`
}
type JointPublication struct {
	ID        string           `json:"id"`
	Releases  []domain.Release `json:"releases"`
	Recovered bool             `json:"recovered"`
}

func jointPublicationHash(project string, pins []JointReleasePin) string {
	return valueHash(struct {
		Kind, Project string
		Releases      []JointReleasePin
	}{"release.joint-publish", project, pins})
}

// Publication is a publisher action, while creating/editing candidates remains
// an editor action. Request admission still precedes the project lock.
func (s *IP) beginJointPublication(ctx context.Context, a domain.Actor, project string) (*sql.Tx, domain.Actor, error) {
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, a, err
	}
	a, err = s.Workspace.actorTx(ctx, tx, a.ID)
	if err == nil {
		_, _, err = requestIdentityPolicyTx(ctx, tx, a.WorkspaceID)
	}
	if err == nil {
		var p domain.Project
		p, err = projectTx(ctx, tx, a, project)
		if err == nil && ((a.Role != "owner" && a.Role != "publisher") || (p.Role != "owner" && p.Role != "publisher")) {
			err = domain.ErrForbidden
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, a, err
	}
	return tx, a, nil
}

func (s *IP) PublishJoint(ctx context.Context, a domain.Actor, project string, input JointPublicationInput) (JointPublication, error) {
	if !validName(input.OperationID, 120) || len(input.Releases) < 2 || len(input.Releases) > 16 {
		return JointPublication{}, domain.ErrValidation
	}
	input.Releases = slices.Clone(input.Releases)
	slices.SortFunc(input.Releases, func(a, b JointReleasePin) int { return strings.Compare(a.ReleaseID, b.ReleaseID) })
	for i, pin := range input.Releases {
		if pin.ReleaseID == "" || pin.ManifestHash == "" || (i > 0 && pin.ReleaseID == input.Releases[i-1].ReleaseID) {
			return JointPublication{}, domain.ErrValidation
		}
	}
	return retrySourceAuthority(ctx, func() (JointPublication, error) { return s.publishJoint(ctx, a, project, input) })
}

func (s *IP) jointReplayTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, input JointPublicationInput) (JointPublication, bool, error) {
	record, err := requestIdentityTx(ctx, tx, a, "operations", input.OperationID, false)
	if err == sql.ErrNoRows {
		return JointPublication{}, false, nil
	}
	if err != nil {
		return JointPublication{}, false, err
	}
	if record.Kind != "release.joint-publish" || record.Hash != jointPublicationHash(project, input.Releases) {
		return JointPublication{}, false, domain.ErrConflict
	}
	result, err := s.readJointPublicationTx(ctx, tx, project, record.Hash)
	return result, true, err
}

func jointMembersTx(ctx context.Context, tx *sql.Tx, project string, pins []JointReleasePin) ([]domain.Release, error) {
	releases := []domain.Release{}
	branches := map[string]bool{}
	for _, pin := range pins {
		r, err := releaseTx(ctx, tx, project, pin.ReleaseID)
		if err != nil {
			return nil, err
		}
		if r.Hash != pin.ManifestHash || r.Status != "READY" || branches[r.Manifest.BranchID] {
			return nil, domain.ErrConflict
		}
		branches[r.Manifest.BranchID] = true
		releases = append(releases, r)
	}
	return releases, nil
}

func (s *IP) publishJoint(ctx context.Context, a domain.Actor, project string, input JointPublicationInput) (JointPublication, error) {
	empty := JointPublication{}
	tx, a, err := s.beginJointPublication(ctx, a, project)
	if err != nil {
		return empty, err
	}
	replay, found, err := s.jointReplayTx(ctx, tx, a, project, input)
	if err != nil || found {
		tx.Rollback()
		return replay, err
	}
	releases, err := jointMembersTx(ctx, tx, project, input.Releases)
	tx.Rollback()
	if err != nil {
		return empty, err
	}
	// Prospective indexes may be built independently; no visible head moves here.
	for _, r := range releases {
		if err = s.preparePublicationIndex(ctx, a, project, "", r.ID, ""); err != nil {
			return empty, err
		}
	}
	tx, a, err = s.beginJointPublication(ctx, a, project)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	replay, found, err = s.jointReplayTx(ctx, tx, a, project, input)
	if err != nil || found {
		return replay, err
	}
	releases, err = jointMembersTx(ctx, tx, project, input.Releases)
	if err != nil {
		return empty, err
	}
	if err = validateJointReferences(releases); err != nil {
		return empty, err
	}
	prepared := []releasePublication{}
	group := map[string]domain.Release{}
	for _, r := range releases {
		group[r.ID] = r
	}
	for _, r := range releases {
		p, err := s.prepareReleasePublicationWithPeersTx(ctx, tx, project, r, group)
		if err != nil {
			return empty, err
		}
		prepared = append(prepared, p)
	}
	result := JointPublication{ID: jointPublicationHash(project, input.Releases), Releases: []domain.Release{}}
	for _, p := range prepared {
		r, err := s.applyReleasePublicationTx(ctx, tx, a, project, p)
		if err != nil {
			return empty, err
		}
		result.Releases = append(result.Releases, r)
	}
	// Membership uses existing retained operation references. The group ID is a
	// digest of exact immutable pins, so missing/altered membership cannot read back.
	for _, r := range result.Releases {
		if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'release.joint-member',?,?)", a.ID, "request-v2:"+NewID(), result.ID, r.ID); err != nil {
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'release.joint-publish',?,?)", a.ID, input.OperationID, result.ID, result.Releases[0].ID); err != nil {
		return empty, err
	}
	if err = audit(ctx, tx, a, "release.joint-publish", result.Releases[0].ID); err != nil {
		return empty, err
	}
	for _, r := range result.Releases {
		if err = s.captureReleaseSourceTx(ctx, tx, project, r.ID); err != nil {
			return empty, err
		}
	}
	return result, tx.Commit()
}

// Immutable manifests must not silently follow an old version of a source
// changed elsewhere in this same group. Such candidates need explicit rebinding.
func validateJointReferences(releases []domain.Release) error {
	for _, r := range releases {
		for _, ref := range r.Manifest.Artifacts {
			if ref.Dependencies == nil {
				continue
			}
			for _, dep := range ref.Dependencies.Inputs {
				if dep.Pinned {
					continue
				}
				for _, other := range releases {
					if other.ID == r.ID {
						continue
					}
					if slices.Contains(r.Manifest.PeerReleases, other.ID) && peerOwnsReference(other, dep.Source) {
						continue
					}
					if dep.Source.Kind == "resource" {
						for _, changed := range other.Manifest.Artifacts {
							if changed.ResourceID == dep.Source.ID {
								return domain.ErrConflict
							}
						}
					} else if dep.Source.Kind != "skill" && other.Manifest.IPCandidateID != "" && dep.Source.BranchID == other.Manifest.BranchID {
						return domain.ErrConflict
					}
				}
			}
		}
	}
	return nil
}

func (s *IP) ReadJointPublication(ctx context.Context, a domain.Actor, project, id string) (JointPublication, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return JointPublication{}, err
	}
	defer tx.Rollback()
	return s.readJointPublicationTx(ctx, tx, project, id)
}

func (s *IP) readJointPublicationTx(ctx context.Context, tx *sql.Tx, project, id string) (JointPublication, error) {
	result := JointPublication{ID: id, Releases: []domain.Release{}}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.result_id FROM operations o JOIN artifact_releases r ON r.id=o.result_id WHERE r.project_id=? AND o.kind='release.joint-member' AND o.request_hash=? ORDER BY o.result_id`, project, id)
	if err != nil {
		return result, err
	}
	ids := []string{}
	for rows.Next() {
		var member string
		if err = rows.Scan(&member); err != nil {
			rows.Close()
			return result, err
		}
		ids = append(ids, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(ids) < 2 || len(ids) > 16 {
		return result, domain.ErrForbidden
	}
	pins := []JointReleasePin{}
	for _, member := range ids {
		r, err := releaseTx(ctx, tx, project, member)
		if err != nil {
			return result, err
		}
		if r.Status != "PUBLISHED" {
			return result, domain.ErrConflict
		}
		result.Releases = append(result.Releases, r)
		pins = append(pins, JointReleasePin{ReleaseID: r.ID, ManifestHash: r.Hash})
	}
	if jointPublicationHash(project, pins) != id {
		return JointPublication{}, domain.ErrConflict
	}
	// Membership alone is not a publication receipt. A recovered marker records
	// historical grouping only; it never grants permission to publish again.
	var markers, restored, anchors int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(o.kind='release.joint-restored'),0),COALESCE(SUM(o.result_id=?),0)
 FROM operations o JOIN artifact_releases r ON r.id=o.result_id
 WHERE r.project_id=? AND o.request_hash=? AND o.kind IN ('release.joint-publish','release.joint-restored')`, ids[0], project, id).Scan(&markers, &restored, &anchors)
	if err != nil {
		return JointPublication{}, err
	}
	if markers != 1 || anchors != 1 {
		return JointPublication{}, domain.ErrConflict
	}
	result.Recovered = restored == 1
	return result, nil
}
