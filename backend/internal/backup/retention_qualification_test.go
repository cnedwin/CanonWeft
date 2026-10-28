package backup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/qualificationledger"
	"novelstudio/internal/retention"
)

func expiredNativeQualificationFixture(t *testing.T, runExpired, generationExpired bool) (*Archive, qualificationArchiveEvidence) {
	t.Helper()
	a := weeklyBudgetArchive(t)
	evidence := qualificationArchiveEvidence{runs: map[string]retention.ExpiredProjection{}, generations: map[string]retention.ExpiredProjection{}}
	project, run, step, call, generation := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("2", 32)
	when := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	makeProof := func(table, id string, summary retention.EvidenceSummary) retention.ExpiredProjection {
		original := a.Data[table][0][tableIndex(table, "frozen")]
		key, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
		ref, _ := retention.ReferenceID(project, table, key, "frozen")
		p, err := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: table + ".frozen", PayloadHash: digest(original), Revision: 1, Deadline: when, Storage: retention.Inline}, original, summary, when)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(p)
		archiveQualificationCell(a, table, 0, "frozen", string(raw))
		return p
	}
	if runExpired {
		evidence.runs[run] = makeProof("qualification_runs", run, retention.EvidenceSummary{Price: &retention.PriceSummary{Currency: "USD"}, WeeklyGrant: &retention.WeeklyGrantSummary{ID: strings.Repeat("e", 32), ScheduleID: strings.Repeat("f", 32), Revision: 1, Week: "2026-W43", CapMicros: 100}})
	}
	if generationExpired {
		evidence.generations[generation] = makeProof("generation_tasks", generation, retention.EvidenceSummary{CallID: call, InputHash: strings.Repeat("3", 64), EstimateMicros: 50, Price: &retention.PriceSummary{Currency: "USD"}, Capability: &retention.CapabilitySummary{Mode: "EVALUATION", EvaluationID: run, EvaluationStepID: step}})
	}
	return a, evidence
}

func anonymizeQualificationGeneration(a *Archive, evidence *qualificationArchiveEvidence) {
	id, actor, step := strings.Repeat("2", 32), strings.Repeat("1", 32), strings.Repeat("c", 32)
	anonymous := "anonymous-evaluation"
	archiveQualificationCell(a, "generation_tasks", 0, "operation_id", anonymous)
	evidence.aliases = []retention.RequestAliasRecord{{Table: "generation_tasks", AnonymousKey: anonymous, OriginalKeyHash: digest([]byte("evaluation-" + step)), Identity: retention.RequestIdentitySummary{SourceID: id, ActorID: actor}}}
}

func TestRetentionArchiveNativeQualificationRejectsBrokenEvidenceAndAmounts(t *testing.T) {
	for _, change := range []string{"missing mapping", "duplicate generation", "duplicate call", "negative amount", "invalid amount", "overflow amount", "currency", "run identity", "wrong proof owner", "altered source", "missing run proof", "missing generation proof", "missing alias", "alias digest", "alias actor", "alias source", "alias table"} {
		t.Run(change, func(t *testing.T) {
			a, evidence := expiredNativeQualificationFixture(t, true, true)
			switch change {
			case "missing mapping":
				a.Data["qualification_calls"] = nil
				archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
			case "duplicate generation":
				a.Data["generation_tasks"] = append(a.Data["generation_tasks"], a.Data["generation_tasks"][0])
			case "duplicate call":
				a.Data["model_calls"] = append(a.Data["model_calls"], a.Data["model_calls"][0])
			case "negative amount", "invalid amount", "overflow amount":
				amount := map[string]string{"negative amount": "-1", "invalid amount": "NaN", "overflow amount": "9223372036854775808"}[change]
				archiveQualificationCell(a, "qualification_calls", 0, "charged_micros", amount)
				archiveQualificationCell(a, "model_calls", 0, "charged_micros", amount)
				archiveQualificationCell(a, "qualification_week_grants", 0, "spent_micros", amount)
			case "currency":
				archiveQualificationCell(a, "model_call_accounting", 0, "currency", "CNY")
			case "run identity":
				archiveQualificationCell(a, "qualification_calls", 0, "run_id", strings.Repeat("9", 32))
			case "wrong proof owner":
				p := evidence.runs[strings.Repeat("b", 32)]
				p.ReferenceID = strings.Repeat("9", 64)
				evidence.runs[strings.Repeat("b", 32)] = p
				raw, _ := json.Marshal(p)
				archiveQualificationCell(a, "qualification_runs", 0, "frozen", string(raw))
			case "altered source":
				archiveQualificationCell(a, "qualification_runs", 0, "frozen", "{}")
			case "missing run proof":
				evidence.runs = nil
			case "missing generation proof":
				evidence.generations = nil
			default:
				anonymizeQualificationGeneration(a, &evidence)
				switch change {
				case "missing alias":
					evidence.aliases = nil
				case "alias digest":
					evidence.aliases[0].OriginalKeyHash = strings.Repeat("9", 64)
				case "alias actor":
					evidence.aliases[0].Identity.ActorID = strings.Repeat("9", 32)
				case "alias source":
					evidence.aliases[0].Identity.SourceID = strings.Repeat("9", 32)
				case "alias table":
					evidence.aliases[0].Table = "model_calls"
				}
			}
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err == nil {
				t.Fatal("corrupt accounting accepted", change)
			}
		})
	}
}

func TestRetentionArchiveNativeQualificationAllowsVerifiedAliasAndLaterInvoice(t *testing.T) {
	a, evidence := expiredNativeQualificationFixture(t, true, true)
	anonymizeQualificationGeneration(a, &evidence)
	if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"qualification_calls", "model_calls"} {
		archiveQualificationCell(a, table, 0, "state", "RECONCILED")
		archiveQualificationCell(a, table, 0, "charged_micros", "71")
	}
	archiveQualificationCell(a, "qualification_calls", 0, "held_micros", "0")
	archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
	archiveQualificationCell(a, "qualification_week_grants", 0, "spent_micros", "71")
	if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
		t.Fatal("late invoice rejected", err)
	}
}

func expiredLegacyQualificationFixture(t *testing.T, kind string) (*Archive, qualificationArchiveEvidence) {
	t.Helper()
	a, evidence := expiredNativeQualificationFixture(t, false, false)
	suite := strings.Repeat("4", 64)
	archiveQualificationCell(a, "qualification_runs", 0, "frozen", `{"suite":{"policy":"repository-deterministic-v2","version":1,"hash":"`+suite+`"},"price":{"currency":"USD"},"private_prompt":"PRIVATE_CANARY"}`)
	for _, table := range []string{"qualification_steps", "generation_tasks", "model_calls", "model_call_accounting", "qualification_calls"} {
		clone := make(Row, len(a.Data[table][0]))
		for i, cell := range a.Data[table][0] {
			clone[i] = append([]byte(nil), cell...)
		}
		a.Data[table] = append(a.Data[table], clone)
	}
	a.Data["qualification_week_grants"], a.Data["qualification_schedules"] = nil, nil
	archiveQualificationCell(a, "qualification_runs", 0, "created_at", "2026-10-24T00:00:00Z")
	specs := map[string]tableSpec{}
	for _, spec := range tableCatalog {
		specs[spec.name] = spec
	}
	g := qualificationBudgetGraph{specs: specs}
	var originals []qualificationledger.OriginalLegacyEvidence
	for i := 0; i < 2; i++ {
		step, gen, call := strings.Repeat("c", 32), strings.Repeat("2", 32), strings.Repeat("d", 32)
		if i == 1 {
			step, gen, call = strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32)
		}
		for table, values := range map[string]map[string]string{
			"qualification_steps":   {"id": step, "child_id": gen},
			"generation_tasks":      {"id": gen, "call_id": call, "operation_id": "evaluation-" + step, "created_at": "2026-10-24T00:00:01Z", "frozen": `{"input_hash":"` + strings.Repeat("3", 64) + `","estimate_micros":50,"price":{"currency":"USD"},"capability":{"mode":"EVALUATION","evaluation_id":"` + strings.Repeat("b", 32) + `","evaluation_step_id":"` + step + `","suite_hash":"` + suite + `"}}`},
			"model_calls":           {"id": call, "created_at": "2026-10-24T00:00:02Z"},
			"model_call_accounting": {"call_id": call},
			"qualification_calls":   {"call_id": call, "step_id": step, "grant_id": ""},
		} {
			for col, value := range values {
				archiveQualificationCell(a, table, i, col, value)
			}
		}
		r := g.record(a.Data["qualification_calls"][i], a.Data["qualification_runs"][0], a.Data["qualification_steps"][i], a.Data["model_calls"][i], a.Data["generation_tasks"][i], a.Data["model_call_accounting"][i])
		source := qualificationledger.Source{Schema: 28, Kind: kind, Boundary: "2026-10-25T00:00:00Z"}
		if kind == "DATABASE" {
			source.MigrationChecksum = qualificationledger.BoundaryChecksum()
		} else {
			source.ManifestHash = strings.Repeat("8", 64)
		}
		mapping, err := qualificationledger.Convert(r, source)
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, qualificationledger.OriginalLegacyEvidence{Record: r, Origin: mapping.Origin, OriginalProof: mapping.Provenance})
	}
	sealed, err := qualificationledger.ExpireLegacyRunEvidence(originals)
	if err != nil {
		t.Fatal(err)
	}
	makeProof := func(table string, index int, summary retention.EvidenceSummary) retention.ExpiredProjection {
		id := g.value(table, a.Data[table][index], "id")
		original := a.Data[table][index][tableIndex(table, "frozen")]
		key, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
		ref, _ := retention.ReferenceID(strings.Repeat("a", 32), table, key, "frozen")
		when := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
		p, err := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: table + ".frozen", PayloadHash: digest(original), Revision: 1, Deadline: when, Storage: retention.Inline}, original, summary, when)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(p)
		archiveQualificationCell(a, table, index, "frozen", string(raw))
		return p
	}
	toSummary := func(m qualificationledger.ExpiredLegacyMetadata, hash string) retention.EvidenceSummary {
		return retention.EvidenceSummary{ConfigID: m.ConfigID, ConfigRevision: m.ConfigRevision, ConfigHash: m.ConfigHash, InputHash: m.InputHash, CallID: m.CallID, EstimateMicros: m.Estimate, Price: &retention.PriceSummary{Currency: m.Currency}, Suite: m.Suite, Capability: m.Capability, LegacyEvidenceHash: hash}
	}
	for i, item := range sealed.Calls {
		var p qualificationledger.ExpiredLegacyProof
		if err := json.Unmarshal(item.Mapping.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		// Root members are sorted by call ID, independent of source row order.
		index := 0
		if item.CallID == strings.Repeat("7", 32) {
			index = 1
		}
		archiveQualificationCell(a, "qualification_calls", index, "origin", item.Mapping.Origin)
		archiveQualificationCell(a, "qualification_calls", index, "legacy_source", string(item.Mapping.Provenance))
		if i == 0 {
			evidence.runs[p.Scope.RunID] = makeProof("qualification_runs", 0, toSummary(p.RunMetadata, sealed.RootHash))
		}
		evidence.generations[p.Scope.GenerationID] = makeProof("generation_tasks", index, toSummary(p.GenerationMetadata, p.ProjectionHash))
	}
	return a, evidence
}

func TestRetentionArchiveExpiredLegacyQualificationRequiresWholeRun(t *testing.T) {
	for _, kind := range []string{"DATABASE", "ARCHIVE"} {
		t.Run(kind, func(t *testing.T) {
			a, evidence := expiredLegacyQualificationFixture(t, kind)
			before, _ := json.Marshal(a.Data)
			if strings.Contains(string(before), "PRIVATE_CANARY") {
				t.Fatal("private text retained")
			}
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(a.Data)
			if string(before) != string(after) {
				t.Fatal("accounting mutated")
			}
			anonymizeQualificationGeneration(a, &evidence)
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
				t.Fatal("legacy alias rejected", err)
			}
			for _, table := range []string{"qualification_calls", "model_calls"} {
				archiveQualificationCell(a, table, 0, "state", "RECONCILED")
				archiveQualificationCell(a, table, 0, "charged_micros", "71")
			}
			archiveQualificationCell(a, "qualification_calls", 0, "held_micros", "0")
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
				t.Fatal("legacy late invoice rejected", err)
			}
		})
	}
	for _, change := range []string{"missing member", "missing generation proof", "missing run proof", "different root", "altered provenance", "mixed native", "missing mapping"} {
		t.Run(change, func(t *testing.T) {
			a, evidence := expiredLegacyQualificationFixture(t, "DATABASE")
			switch change {
			case "missing member":
				for _, table := range []string{"qualification_calls", "model_calls", "model_call_accounting", "generation_tasks", "qualification_steps"} {
					a.Data[table] = a.Data[table][:1]
				}
				delete(evidence.generations, strings.Repeat("6", 32))
			case "missing generation proof":
				delete(evidence.generations, strings.Repeat("6", 32))
			case "missing run proof":
				evidence.runs = nil
			case "different root":
				p := evidence.runs[strings.Repeat("b", 32)]
				p.Summary.LegacyEvidenceHash = strings.Repeat("9", 64)
				evidence.runs[strings.Repeat("b", 32)] = p
				raw, _ := json.Marshal(p)
				archiveQualificationCell(a, "qualification_runs", 0, "frozen", string(raw))
			case "altered provenance":
				archiveQualificationCell(a, "qualification_calls", 0, "legacy_source", string(a.Data["qualification_calls"][1][tableIndex("qualification_calls", "legacy_source")]))
			case "mixed native":
				archiveQualificationCell(a, "qualification_calls", 0, "origin", "NATIVE_30")
				archiveQualificationCell(a, "qualification_calls", 0, "legacy_source", "")
			case "missing mapping":
				a.Data["qualification_calls"] = nil
			}
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err == nil {
				t.Fatal("incomplete or forged run accepted", change)
			}
		})
	}
}

func TestRetentionArchiveNativeQualificationKeepsUnknownHoldAfterInputExpiry(t *testing.T) {
	for _, kind := range []string{"run", "generation", "both"} {
		t.Run(kind, func(t *testing.T) {
			a, evidence := expiredNativeQualificationFixture(t, kind != "generation", kind != "run")
			before, _ := json.Marshal(a.Data)
			if err := validateQualificationBudgetWithEvidence(a.Data, evidence); err != nil {
				t.Fatal("proven expired accounting input rejected", err)
			}
			after, _ := json.Marshal(a.Data)
			if string(before) != string(after) {
				t.Fatal("validation changed unsettled money/history")
			}
			if err := validateQualificationBudget(a.Data); err == nil {
				t.Fatal("unproven expired input accepted")
			}
		})
	}
}

func TestRetentionArchiveQualificationRunProofRequiresIndependentJournal(t *testing.T) {
	for _, change := range []string{"valid", "missing journal", "missing current", "changed source", "changed owner", "changed independent proof"} {
		t.Run(change, func(t *testing.T) {
			a, _ := archiveCleanupFixture(t)
			metadata, at := retentionHistoryFixtureFor(t, "qualification_runs")
			history, err := readArchiveReferenceHistory(metadata, at)
			if err != nil {
				t.Fatal(err)
			}
			var evidence string
			for _, record := range history.Expiry {
				evidence = record.Evidence
			}
			branch := strings.Repeat("1", 32)
			testRow(a, "branches", map[string]string{"id": branch, "project_id": strings.Repeat("a", 32)})
			testRow(a, "qualification_runs", map[string]string{"id": strings.Repeat("b", 32), "project_id": strings.Repeat("a", 32), "workspace_id": strings.Repeat("b", 32), "actor_id": strings.Repeat("c", 32), "branch_id": branch, "frozen": evidence})
			switch change {
			case "missing journal":
				history.Expiry = nil
			case "missing current":
				history.Current = nil
			case "changed source":
				archiveQualificationCell(a, "qualification_runs", 0, "frozen", "{}")
			case "changed owner":
				archiveQualificationCell(a, "qualification_runs", 0, "id", strings.Repeat("9", 32))
			case "changed independent proof":
				for key, record := range history.Expiry {
					record.Evidence = "{}"
					history.Expiry[key] = record
				}
			}
			proofs, err := readArchiveFrozenEvidence(a.Data, history, "qualification_runs")
			if change == "valid" {
				if err != nil || len(proofs) != 1 {
					t.Fatal("valid run journal rejected", err)
				}
			} else if err == nil {
				t.Fatal("unbound run proof accepted", change)
			}
		})
	}
}
