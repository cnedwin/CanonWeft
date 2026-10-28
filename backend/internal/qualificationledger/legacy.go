// Package qualificationledger converts only proven pre-budget evaluation
// receipts. It never authorizes execution or invents a weekly grant.
package qualificationledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"novelstudio/migrations"
	"regexp"
	"strings"
	"time"
)

type Record struct {
	Project, Actor, RunID, StepID, GenerationID, CallID                                          string
	RunProject, StepProject, StepRun, GenerationProject, GenerationActor, CallProject, CallActor string
	ChildID, ChildKind, Operation, InputHash, RunCreated, GenerationCreated, CallCreated         string
	RunFrozen, GenerationFrozen                                                                  []byte
	State, Currency                                                                              string
	Reserved, Charged                                                                            int64
}
type Source struct {
	Schema            int    `json:"schema"`
	Kind              string `json:"kind"`
	Boundary          string `json:"boundary"`
	MigrationChecksum string `json:"migration_checksum,omitempty"`
	ManifestHash      string `json:"manifest_hash,omitempty"`
	BoundaryClock     string `json:"boundary_clock"`
}
type Mapping struct {
	Origin                  string
	Provenance              []byte
	Reserved, Held, Charged int64
	State                   string
}
type Proof struct {
	Rule       string `json:"rule"`
	Source     Source `json:"source"`
	RecordHash string `json:"record_hash"`
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func BoundaryChecksum() string {
	raw, err := migrations.Files.ReadFile("0029_qualification_budget.sql")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}
func ParseTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unproved legacy timestamp")
}
func canonical(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func Convert(r Record, source Source) (Mapping, error) {
	var result Mapping
	if source.Schema != 28 {
		return result, errors.New("legacy source must explicitly be schema28")
	}
	boundary, err := ParseTime(source.Boundary)
	if err != nil {
		return result, err
	}
	runTime, err := ParseTime(r.RunCreated)
	if err != nil {
		return result, err
	}
	generationTime, err := ParseTime(r.GenerationCreated)
	if err != nil {
		return result, err
	}
	callTime, err := ParseTime(r.CallCreated)
	if err != nil {
		return result, err
	}
	if source.Kind == "DATABASE" {
		if source.MigrationChecksum != BoundaryChecksum() || !runTime.Before(boundary) || !generationTime.Before(boundary) || !callTime.Before(boundary) {
			return result, errors.New("legacy database lacks executed migration29 time/checksum boundary")
		}
		result.Origin = "LEGACY_28_DB"
		source.Boundary = boundary.Format("2006-01-02 15:04:05.999999")
		source.BoundaryClock = "MYSQL_DATETIME"
	} else if source.Kind == "ARCHIVE" {
		if !hashPattern.MatchString(source.ManifestHash) {
			return result, errors.New("legacy archive lacks original schema28 snapshot provenance")
		}
		result.Origin = "LEGACY_28_ARCHIVE"
		source.Boundary = boundary.Format(time.RFC3339Nano)
		source.BoundaryClock = "UTC_SNAPSHOT"
	} else {
		return result, errors.New("unknown legacy source")
	}
	for _, id := range []string{r.Project, r.Actor, r.RunID, r.StepID, r.GenerationID, r.CallID} {
		if !idPattern.MatchString(id) {
			return result, errors.New("invalid legacy identity")
		}
	}
	if r.RunProject != r.Project || r.StepProject != r.Project || r.StepRun != r.RunID || r.GenerationProject != r.Project || r.CallProject != r.Project || r.GenerationActor != r.Actor || r.CallActor != r.Actor || r.Operation != "evaluation-"+r.StepID || (r.ChildID != "" && (r.ChildID != r.GenerationID || r.ChildKind != "generation")) || (r.ChildID == "" && r.ChildKind != "") {
		return result, errors.New("legacy generation/step/call ownership mismatch")
	}
	var frozen struct {
		Weekly json.RawMessage `json:"weekly_grant"`
		Suite  struct {
			Policy  string `json:"policy"`
			Version int    `json:"version"`
			Hash    string `json:"hash"`
		} `json:"suite"`
		Price struct {
			Currency string `json:"currency"`
		} `json:"price"`
	}
	var generation struct {
		InputHash string `json:"input_hash"`
		Estimate  int64  `json:"estimate_micros"`
		Price     struct {
			Currency string `json:"currency"`
		} `json:"price"`
		Capability *struct {
			Mode      string `json:"mode"`
			Run       string `json:"evaluation_id"`
			Step      string `json:"evaluation_step_id"`
			SuiteHash string `json:"suite_hash"`
		} `json:"capability"`
	}
	if json.Unmarshal(r.RunFrozen, &frozen) != nil || json.Unmarshal(r.GenerationFrozen, &generation) != nil {
		return result, errors.New("invalid legacy frozen data")
	}
	policy := frozen.Suite.Policy
	if (policy != "repository-deterministic-v1" && policy != "repository-deterministic-v2") || frozen.Suite.Version != 1 || !hashPattern.MatchString(frozen.Suite.Hash) || (len(frozen.Weekly) > 0 && string(frozen.Weekly) != "null") {
		return result, errors.New("native or weekly qualification cannot become legacy")
	}
	trial := generation.Capability
	if trial == nil || trial.Mode != "EVALUATION" || trial.Run != r.RunID || (trial.Step != "" && trial.Step != r.StepID) || trial.SuiteHash != frozen.Suite.Hash || generation.InputHash != r.InputHash || !hashPattern.MatchString(r.InputHash) || generation.Estimate != r.Reserved || r.Reserved <= 0 || r.Charged < 0 || (r.Currency != "USD" && r.Currency != "CNY") || generation.Price.Currency != r.Currency || frozen.Price.Currency != r.Currency {
		return result, errors.New("legacy frozen execution or currency mismatch")
	}
	switch r.State {
	case "RESERVED", "DISPATCHED", "UNKNOWN":
		result.Held = r.Reserved
	case "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED":
	default:
		return result, errors.New("unsupported legacy bill state")
	}
	result.Reserved = r.Reserved
	result.Charged = r.Charged
	result.State = r.State
	runRaw, err := canonical(r.RunFrozen)
	if err != nil {
		return result, err
	}
	generationRaw, err := canonical(r.GenerationFrozen)
	if err != nil {
		return result, err
	}
	stable, _ := json.Marshal([]any{r.Project, r.Actor, r.RunID, r.StepID, r.GenerationID, r.CallID, r.Operation, r.InputHash, runTime.Format(time.RFC3339Nano), generationTime.Format(time.RFC3339Nano), callTime.Format(time.RFC3339Nano), json.RawMessage(runRaw), json.RawMessage(generationRaw), r.Reserved, r.Currency})
	hash := sha256.Sum256(stable)
	result.Provenance, _ = json.Marshal(Proof{Rule: "legacy-evaluation-ledger-v1", Source: source, RecordHash: hex.EncodeToString(hash[:])})
	return result, nil
}

func ValidateProof(r Record, origin string, raw []byte) error {
	var proof Proof
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || proof.Rule != "legacy-evaluation-ledger-v1" {
		return errors.New("invalid legacy accounting provenance")
	}
	converted, err := Convert(r, proof.Source)
	if err != nil {
		return err
	}
	normalized, _ := json.Marshal(proof)
	if converted.Origin != origin || !bytes.Equal(converted.Provenance, normalized) {
		return errors.New("legacy accounting provenance does not match current immutable record")
	}
	return nil
}
