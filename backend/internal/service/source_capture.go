package service

import (
	"context"
	"database/sql"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

func (s *IP) captureSourceValuesTx(ctx context.Context, tx *sql.Tx, project string, sources ...retention.SourceKey) error {
	var read retention.ReadSourceObject
	if s.Blobs != nil {
		read = s.Blobs.ReadObject
	}
	return retention.CaptureSourceValuesWithObjects(ctx, tx, protectedLedger(), project, read, sources...)
}

func (s *IP) captureCandidateSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "ip_revisions", Primary: map[string]string{"id": id}})
}

func (s *IP) captureReleaseSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "artifact_releases", Primary: map[string]string{"id": id}})
}

func captureCallSourcesTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return retention.CaptureSourceValues(ctx, tx, protectedLedger(), project, retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": id}})
}

func captureMetadataSourceTx(ctx context.Context, tx *sql.Tx, project, table, id string) error {
	return retention.CaptureSourceValues(ctx, tx, protectedLedger(), project, retention.SourceKey{Table: table, Primary: map[string]string{"id": id}})
}

func captureDiagnosticSourceTx(ctx context.Context, tx *sql.Tx, project, table, id string) error {
	return captureDiagnosticKeyTx(ctx, tx, project, table, map[string]string{"id": id})
}

func captureDiagnosticKeyTx(ctx context.Context, tx *sql.Tx, project, table string, primary map[string]string) error {
	return retention.CaptureSourceValues(ctx, tx, protectedLedger(), project, retention.SourceKey{Table: table, Primary: primary, DiagnosticsOnly: true})
}

func (s *IP) captureDiscoverySourceTx(ctx context.Context, tx *sql.Tx, project, table, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: table, Primary: map[string]string{"id": id}})
}

func (s *IP) captureImportSourceTx(ctx context.Context, tx *sql.Tx, project, table, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: table, Primary: map[string]string{"id": id}})
}

func (s *IP) captureReviewTaskSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "platform_tasks", Primary: map[string]string{"id": id}})
}

func (s *IP) captureCapabilitySourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "capability_versions", Primary: map[string]string{"id": id}})
}

func (s *IP) captureSkillSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "skill_packages", Primary: map[string]string{"id": id}})
}

func (s *IP) captureRecipeSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "recipe_runs", Primary: map[string]string{"id": id}})
}

func (s *IP) captureToolSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "tool_trials", Primary: map[string]string{"id": id}})
}

func (s *IP) captureEvaluationSourceTx(ctx context.Context, tx *sql.Tx, project, run, step, record string) error {
	keys := []retention.SourceKey{}
	for _, target := range []struct{ table, id string }{{"qualification_runs", run}, {"qualification_steps", step}, {"capability_qualifications", record}} {
		if target.id != "" {
			keys = append(keys, retention.SourceKey{Table: target.table, Primary: map[string]string{"id": target.id}})
		}
	}
	return s.captureSourceValuesTx(ctx, tx, project, keys...)
}

func (s *IP) captureEvaluationCreationTx(ctx context.Context, tx *sql.Tx, run QualificationRun) error {
	keys := []retention.SourceKey{{Table: "qualification_runs", Primary: map[string]string{"id": run.ID}}}
	for _, step := range run.Steps {
		keys = append(keys, retention.SourceKey{Table: "qualification_steps", Primary: map[string]string{"id": step.ID}})
		// Keep the collector's existing bounded target limit. Large suites are
		// journaled in batches inside this same creation transaction, so a
		// failed later batch still rolls back the whole run and every step.
		if len(keys) == 128 {
			if err := s.captureSourceValuesTx(ctx, tx, run.ProjectID, keys...); err != nil {
				return err
			}
			keys = nil
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return s.captureSourceValuesTx(ctx, tx, run.ProjectID, keys...)
}

func (s *IP) captureApprovalSourceTx(ctx context.Context, tx *sql.Tx, project, run string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "qualification_approvals", Primary: map[string]string{"run_id": run}})
}

func (s *IP) captureSemanticSourceTx(ctx context.Context, tx *sql.Tx, project, id, acceptance string) error {
	key := retention.SourceKey{Table: "semantic_review_runs", Primary: map[string]string{"id": id}}
	if acceptance != "" {
		key = retention.SourceKey{Table: "semantic_review_acceptances", Primary: map[string]string{"id": acceptance}}
	}
	return s.captureSourceValuesTx(ctx, tx, project, key)
}

func (s *IP) observeGenerationCreationTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "generation_tasks", Primary: map[string]string{"id": id}, ObserveOnly: true})
}

func (s *IP) captureGenerationSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "generation_tasks", Primary: map[string]string{"id": id}})
}

func (s *IP) captureChangePlanSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "change_plans", Primary: map[string]string{"id": id}})
}

func (s *IP) capturePrivateIndexSourceTx(ctx context.Context, tx *sql.Tx, project, id string, optional bool) error {
	return s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "working_indexes", Primary: map[string]string{"id": id}, IfPresent: optional})
}

func (s *IP) capturePublicIndexSourceTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	var read retention.ReadSourceObject
	if s.Blobs != nil {
		read = s.Blobs.ReadObject
	}
	return retention.CaptureKnowledgeIndex(ctx, tx, protectedLedger(), project, id, read)
}

// These are the already authorized inputs actually included in the package,
// not a second interpretation of raw caller selections or current heads.
func contextWorkingSourceKeys(actor string, pack ContextPackage) ([]retention.SourceKey, error) {
	keys := []retention.SourceKey{{Table: "context_packages", Primary: map[string]string{"id": pack.ID}}}
	seen := map[string]int{}
	add := func(table string, primary map[string]string, optional bool) error {
		key, err := retention.RowOwnerKey(table, primary)
		if err != nil {
			return err
		}
		identity := table + ":" + key
		if position, exists := seen[identity]; exists {
			keys[position].IfPresent = keys[position].IfPresent && optional
		} else {
			seen[identity] = len(keys)
			keys = append(keys, retention.SourceKey{Table: table, Primary: primary, IfPresent: optional})
		}
		return nil
	}
	working := func(ref domain.WorkingRef) error {
		switch ref.Kind {
		case "ip_draft":
			return add("ip_drafts", map[string]string{"branch_id": ref.ID, "actor_id": actor}, false)
		case "resource_draft":
			if err := add("resource_drafts", map[string]string{"resource_id": ref.ID, "actor_id": actor}, false); err != nil {
				return err
			}
			// Legacy body-only drafts have no policy row; the source reader must
			// preserve that absence rather than inventing a policy version.
			return add("resource_policy_drafts", map[string]string{"resource_id": ref.ID, "actor_id": actor}, true)
		case "ip_candidate":
			return add("ip_revisions", map[string]string{"id": ref.ID}, false)
		case "release_candidate":
			return add("artifact_releases", map[string]string{"id": ref.ID}, false)
		default:
			return domain.ErrValidation
		}
	}
	for _, input := range pack.Unconfirmed {
		if err := working(input.Ref); err != nil {
			return nil, err
		}
	}
	for _, hit := range pack.PrivateHits {
		if err := working(hit.Ref); err != nil {
			return nil, err
		}
	}
	if pack.SessionSource != nil {
		if err := add("memory_sessions", map[string]string{"id": pack.SessionSource.ID}, false); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
