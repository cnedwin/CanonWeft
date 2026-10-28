package qualificationledger

import (
	"errors"
	"novelstudio/internal/retention"
	"slices"
	"strings"
)

var ErrExpiredLegacyRunRequired = errors.New("expired legacy evidence requires complete run batch validation")

const expiredLegacyRunRule = "legacy-evaluation-expired-run-v1"
const maxExpiredLegacyCalls = 4096

// OriginalLegacyEvidence must come from the locked original owner/call rows.
// The outer adapter supplies the complete reverse-enumerated run call graph.
type OriginalLegacyEvidence struct {
	Record        Record
	Origin        string
	OriginalProof []byte
}

type ExpiredLegacyCallMapping struct {
	CallID  string
	Mapping Mapping
}

// RootHash binds one shared run envelope to every per-call conversion digest.
// Calls are returned in stable call-ID order. No private frozen bytes survive.
type ExpiredLegacyRunEvidence struct {
	RootHash string
	Calls    []ExpiredLegacyCallMapping
}

type ExpiredLegacyCall struct {
	Record               Record
	Origin               string
	Proof                []byte
	GenerationProjection retention.ExpiredProjection
}

type expiredLegacyRunMember struct {
	StepID         string `json:"step_id"`
	GenerationID   string `json:"generation_id"`
	CallID         string `json:"call_id"`
	ProjectionHash string `json:"projection_hash"`
}

func expiredRunRoot(proofs []ExpiredLegacyProof) (string, error) {
	if len(proofs) == 0 || len(proofs) > maxExpiredLegacyCalls {
		return "", errExpiredLegacy
	}
	first := proofs[0]
	seenSteps, seenGenerations, seenCalls := map[string]bool{}, map[string]bool{}, map[string]bool{}
	members := make([]expiredLegacyRunMember, 0, len(proofs))
	for _, p := range proofs {
		a, b := p.Scope, first.Scope
		if p.Schema != 1 || p.Rule != ExpiredLegacyRule || p.ProjectionHash != expiredProjectionHash(p) || !hashPattern.MatchString(p.ProjectionHash) || a.Project != b.Project || a.Actor != b.Actor || a.RunID != b.RunID || a.RunCreated != b.RunCreated || p.RunFrozenHash != first.RunFrozenHash || expiredJSONHash(p.RunMetadata) != expiredJSONHash(first.RunMetadata) || seenSteps[a.StepID] || seenGenerations[a.GenerationID] || seenCalls[a.CallID] {
			return "", errExpiredLegacy
		}
		seenSteps[a.StepID], seenGenerations[a.GenerationID], seenCalls[a.CallID] = true, true, true
		members = append(members, expiredLegacyRunMember{a.StepID, a.GenerationID, a.CallID, p.ProjectionHash})
	}
	slices.SortFunc(members, func(a, b expiredLegacyRunMember) int { return strings.Compare(a.CallID, b.CallID) })
	return expiredJSONHash([]any{expiredLegacyRunRule, first.Scope.Project, first.Scope.Actor, first.Scope.RunID, first.Scope.RunCreated, first.RunFrozenHash, first.RunMetadata, members}), nil
}

// ExpireLegacyRunEvidence performs no writes. Before materializing its result,
// the owner must lock the project/run and COMPLETE step/generation/call graph,
// require the parent to be terminal and incapable of adding/dispatching work,
// and verify every frozen is due. Store all provenance/envelopes/retention rows
// atomically. UNKNOWN bills are retained unchanged; they do not authorize release.
func ExpireLegacyRunEvidence(originals []OriginalLegacyEvidence) (ExpiredLegacyRunEvidence, error) {
	var result ExpiredLegacyRunEvidence
	if len(originals) == 0 || len(originals) > maxExpiredLegacyCalls {
		return result, errExpiredLegacy
	}
	proofs := make([]ExpiredLegacyProof, 0, len(originals))
	for _, original := range originals {
		mapping, err := ExpireLegacyEvidence(original.Record, original.Origin, original.OriginalProof)
		if err != nil {
			return ExpiredLegacyRunEvidence{}, err
		}
		var proof ExpiredLegacyProof
		if strictExpiredDecode(mapping.Provenance, &proof) != nil {
			return ExpiredLegacyRunEvidence{}, errExpiredLegacy
		}
		proofs = append(proofs, proof)
		result.Calls = append(result.Calls, ExpiredLegacyCallMapping{original.Record.CallID, mapping})
	}
	root, err := expiredRunRoot(proofs)
	if err != nil {
		return ExpiredLegacyRunEvidence{}, err
	}
	result.RootHash = root
	slices.SortFunc(result.Calls, func(a, b ExpiredLegacyCallMapping) int { return strings.Compare(a.CallID, b.CallID) })
	return result, nil
}

// ValidateExpiredLegacyRun requires the COMPLETE current actual call graph.
// RootHash in the independently verified run retention evidence commits that
// complete set. A missing, extra or duplicate member cannot match it. The caller
// must still reverse-enumerate source tables, bind EVERY supplied projection to
// its actual retention sidecar and serialize new calls/steps with this snapshot.
// Results preserve the input call order; mutable bills do not enter the root.
func ValidateExpiredLegacyRun(calls []ExpiredLegacyCall, runProjection retention.ExpiredProjection) ([]Mapping, error) {
	if len(calls) == 0 || len(calls) > maxExpiredLegacyCalls {
		return nil, errExpiredLegacy
	}
	proofs := make([]ExpiredLegacyProof, 0, len(calls))
	for _, call := range calls {
		var proof ExpiredLegacyProof
		if strictExpiredDecode(call.Proof, &proof) != nil {
			return nil, errExpiredLegacy
		}
		proofs = append(proofs, proof)
	}
	root, err := expiredRunRoot(proofs)
	if err != nil || runProjection.Summary.LegacyEvidenceHash != root {
		return nil, errExpiredLegacy
	}
	result := make([]Mapping, 0, len(calls))
	for _, call := range calls {
		mapping, err := validateExpiredLegacyMember(call.Record, call.Origin, call.Proof, runProjection, call.GenerationProjection, root)
		if err != nil {
			return nil, err
		}
		result = append(result, mapping)
	}
	return result, nil
}
