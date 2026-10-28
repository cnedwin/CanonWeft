package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"slices"
	"strconv"
)

var recipeTables = []string{"recipe_runs", "recipe_steps"}

func upgradeRecipes(a *Archive) error {
	if a.Manifest.Schema >= 34 {
		return nil
	}
	for _, name := range recipeTables {
		if _, ok := a.Data[name]; ok {
			return errors.New("unexpected recipe table in legacy archive")
		}
		a.Data[name] = nil
	}
	return nil
}

func recipeJSONEqual(a, b []byte) bool {
	x, e1 := recipeCanonicalJSON(a)
	y, e2 := recipeCanonicalJSON(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

func recipeCanonicalJSON(raw []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Match the typed owner's capabilityDigest normalization, including exact
// numeric tokens. Struct field order is not the persisted contract digest.
func recipeJSONDigest(raw []byte) string {
	canonical, err := recipeCanonicalJSON(raw)
	if err != nil {
		return ""
	}
	return digest(canonical)
}

func recipeInputValid(schema, input json.RawMessage) bool {
	if _, err := domain.ResolveRecipeInput(input, nil, nil, nil); err != nil {
		return false
	}
	contract, err := domain.CompileCapabilitySchema(schema)
	if err != nil {
		return false
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	return err == nil && contract.Validate(value) == nil
}

// Validate the exported immutable contracts and relations; disabled restore
// records never regain capability qualification or credential authority.
func validateRecipeBackup(d Data) error {
	return validateRecipeBackupWithEvidence(d, nil)
}

func validateRecipeBackupWithEvidence(d Data, expired map[string]retention.ExpiredProjection) error {
	return validateRecipeBackupWithAllEvidence(d, expired, nil, nil)
}

func validateRecipeBackupWithAllEvidence(d Data, expired, expiredRuns, expiredSteps map[string]retention.ExpiredProjection, definitionEvidence ...map[string]retention.ExpiredProjection) error {
	var definitions map[string]retention.ExpiredProjection
	if len(definitionEvidence) > 0 {
		definitions = definitionEvidence[0]
	}
	bad := errors.New("invalid recipe archive contract or ownership")
	index := map[string]map[string]map[string]string{}
	for _, spec := range tableCatalog {
		if !slices.Contains([]string{"workspaces", "projects", "branches", "users", "ip_revisions", "capability_versions", "model_connection_versions", "model_prices", "generation_tasks", "context_packages", "recipe_runs", "recipe_steps"}, spec.name) {
			continue
		}
		index[spec.name] = map[string]map[string]string{}
		for _, row := range d[spec.name] {
			values := map[string]string{}
			for _, column := range spec.cols() {
				values[column] = spec.value(row, column)
			}
			key := values["id"]
			if spec.name == "model_connection_versions" {
				key = values["connection_id"] + ":" + values["revision"]
			}
			if _, exists := index[spec.name][key]; exists {
				return bad
			}
			index[spec.name][key] = values
		}
	}
	versionValid := func(project string, v service.CapabilityVersion) bool {
		row := index["capability_versions"][v.ID]
		raw, _ := json.Marshal(v.Definition)
		if _, ok := definitions[v.ID]; ok {
			view, err := archiveRecipeCapability(index, v.ID, v.Hash, definitions)
			want, _ := json.Marshal(view.Definition)
			return err == nil && row["project_id"] == project && row["parent_id"] == v.ParentID && bytes.Equal(raw, want)
		}
		return row["project_id"] == project && row["content_hash"] == v.Hash && row["parent_id"] == v.ParentID && recipeJSONEqual(raw, []byte(row["definition"])) && v.Definition.Validate() == nil
	}
	roots := map[string]service.RecipeFrozen{}
	for id, row := range index["recipe_runs"] {
		var frozen service.RecipeFrozen
		raw := []byte(row["frozen"])
		revision, e1 := strconv.ParseInt(row["revision"], 10, 64)
		budget, e2 := strconv.ParseInt(row["budget_limit_micros"], 10, 64)
		project, workspace, actor, branch := row["project_id"], row["workspace_id"], row["actor_id"], row["branch_id"]
		if e1 != nil || e2 != nil || revision < 1 || budget < 1 || budget > 1_000_000_000_000 || !slices.Contains([]string{"USD", "CNY"}, row["currency"]) || row["restored_disabled"] != "1" || row["cancel_requested"] != "1" || row["paused"] != "0" || !slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, row["state"]) {
			return bad
		}
		if index["workspaces"][workspace] == nil || index["projects"][project]["workspace_id"] != workspace || index["users"][actor]["workspace_id"] != workspace || index["branches"][branch]["project_id"] != project {
			return bad
		}
		proof, isExpired := expiredRuns[id]
		if isExpired {
			var err error
			frozen, err = archivedRecipeRun(row, proof, index, definitions)
			if err != nil {
				return err
			}
		} else if digest(raw) != row["frozen_hash"] || strictJSON(raw, &frozen) != nil {
			return bad
		}
		if frozen.Protocol != 1 || !hex64.MatchString(frozen.ManifestHash) || frozen.Definition.Definition.Kind != "recipe" || !versionValid(project, frozen.Definition) {
			return bad
		}
		if !isExpired && !recipeInputValid(frozen.Definition.Definition.InputSchema, frozen.Input) {
			return bad
		}
		if frozen.BaseHead != "" {
			revision := index["ip_revisions"][frozen.BaseHead]
			if index["branches"][revision["branch_id"]]["project_id"] != project {
				return bad
			}
		}
		roots[id] = frozen
	}
	type history struct {
		attempts    map[int]bool
		active, max int
		activeState string
	}
	histories := map[string]*history{}
	children := map[string]string{}
	for id, row := range index["recipe_steps"] {
		root, ok := roots[row["run_id"]]
		_, retiredLayout := definitions[root.Definition.ID]
		if !ok {
			return bad
		}
		parent := index["recipe_runs"][row["run_id"]]
		ordinal, e1 := strconv.Atoi(row["ordinal"])
		attempt, e2 := strconv.Atoi(row["attempt"])
		if e1 != nil || e2 != nil || ordinal < 0 || ordinal >= len(root.Definition.Definition.Steps) || attempt < 1 || attempt > 2147483647 || row["project_id"] != parent["project_id"] || !slices.Contains([]string{"0", "1"}, row["active"]) {
			return bad
		}
		var frozen service.RecipeStepFrozen
		raw := []byte(row["frozen"])
		proof, isExpired := expiredSteps[id]
		if isExpired {
			var err error
			frozen, err = archivedRecipeStep(row, proof, root, ordinal, index, definitions)
			if err != nil {
				return err
			}
		} else if digest(raw) != row["frozen_hash"] || strictJSON(raw, &frozen) != nil {
			return bad
		}
		keyMatches := (!retiredLayout && row["step_key"] == frozen.Step.Key) || (isExpired && retention.LabelIdentityHash(row["step_key"]) == archiveRecipeKeyHash(retiredLayout, frozen.Step.Key))
		if row["credential_ref"] != "" || !keyMatches || frozen.Input.Key != frozen.Step.Key || frozen.Step.AgentID != frozen.Agent.ID || !hex64.MatchString(frozen.ManifestHash) || !versionValid(row["project_id"], frozen.Agent) {
			return bad
		}
		want, _ := json.Marshal(root.Definition.Definition.Steps[ordinal])
		got, _ := json.Marshal(frozen.Step)
		if !bytes.Equal(want, got) || frozen.Agent.Definition.Kind != "agent" || frozen.Price.Currency != parent["currency"] || frozen.Price.ConnectionID != frozen.Config.ID || frozen.Price.ConnectionRevision != frozen.Config.Revision {
			return bad
		}
		input := frozen.Input
		if input.PriceID != frozen.Price.ID || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 131072 || input.InputText != "" || (!isExpired && domain.ValidateRecipeRouting(input.Input, input.Bindings, frozen.Step.After) != nil) {
			return bad
		}
		if !isExpired && len(input.Bindings) == 0 && !recipeInputValid(frozen.Agent.Definition.InputSchema, input.Input) {
			return bad
		}
		// Legacy inputs may lack routing evidence; preserving them must not
		// invent it. When pins exist, every pin must identify the original
		// earlier attempt, including attempts made inactive by a later retry.
		if len(frozen.Sources) > 0 {
			if row["input_hash"] == "" || len(frozen.Sources) != len(frozen.Step.After) {
				return bad
			}
			for i, pin := range frozen.Sources {
				source := index["recipe_steps"][pin.StepID]
				before, err := strconv.Atoi(source["ordinal"])
				if err != nil || before < 0 || before >= ordinal || pin.Key != frozen.Step.After[i] || retention.LabelIdentityHash(source["step_key"]) != archiveRecipeKeyHash(retiredLayout, pin.Key) || source["run_id"] != row["run_id"] || source["project_id"] != row["project_id"] || !hex64.MatchString(pin.OutputHash) || source["output_hash"] != pin.OutputHash {
					return bad
				}
			}
		}
		if input.ContextID != "" {
			pack := index["context_packages"][input.ContextID]
			if pack["project_id"] != parent["project_id"] || pack["branch_id"] != parent["branch_id"] || pack["actor_id"] != parent["actor_id"] {
				return bad
			}
		}
		config := index["model_connection_versions"][fmt.Sprintf("%s:%d", frozen.Config.ID, frozen.Config.Revision)]
		price := index["model_prices"][frozen.Price.ID]
		configRaw, _ := json.Marshal(frozen.Config)
		priceRaw, _ := json.Marshal(frozen.Price)
		if !recipeJSONEqual(configRaw, []byte(config["config"])) || !recipeJSONEqual(priceRaw, []byte(price["payload"])) || price["workspace_id"] != parent["workspace_id"] {
			return bad
		}
		if !slices.Contains([]string{"WAITING", "PREPARING", "RUNNING", "WAITING_CONFIRMATION", "COMPLETED", "FAILED", "UNKNOWN"}, row["state"]) {
			return bad
		}
		if row["confirmed_hash"] != "" && row["confirmed_hash"] != row["output_hash"] {
			return bad
		}
		if slices.Contains([]string{"WAITING_CONFIRMATION", "COMPLETED"}, row["state"]) && row["output_hash"] == "" {
			return bad
		}
		if row["state"] == "COMPLETED" && frozen.Step.WaitForConfirmation && row["confirmed_hash"] == "" {
			return bad
		}
		key := row["run_id"] + ":" + retention.LabelIdentityHash(row["step_key"])
		h := histories[key]
		if h == nil {
			h = &history{attempts: map[int]bool{}}
			histories[key] = h
		}
		if h.attempts[attempt] {
			return bad
		}
		h.attempts[attempt] = true
		if attempt > h.max {
			h.max = attempt
		}
		if row["active"] == "1" {
			if h.active != 0 {
				return bad
			}
			h.active = attempt
			h.activeState = row["state"]
		}
		if child := row["generation_id"]; child != "" {
			if children[child] != "" || row["input_hash"] == "" {
				return bad
			}
			children[child] = id
			generation := index["generation_tasks"][child]
			if proof, ok := expired[child]; ok {
				want := recipeGenerationIdentity{run: row["run_id"], step: id, input: row["input_hash"], baseHead: root.BaseHead, context: input.ContextID, role: frozen.Agent.Definition.Role, agent: frozen.Agent.ID, manifest: frozen.ManifestHash, configHash: recipeJSONDigest(configRaw), priceHash: recipeJSONDigest(priceRaw)}
				if generation["project_id"] != row["project_id"] || generation["actor_id"] != parent["actor_id"] || generation["branch_id"] != parent["branch_id"] || !expiredRecipeGenerationMatches(proof, want) {
					return bad
				}
				continue
			}
			var g service.GenerationFrozen
			if json.Unmarshal([]byte(generation["frozen"]), &g) != nil || generation["project_id"] != row["project_id"] || generation["actor_id"] != parent["actor_id"] || generation["branch_id"] != parent["branch_id"] || g.RecipeRunID != row["run_id"] || g.RecipeStepID != id || g.RecipeInputHash != row["input_hash"] || g.BaseHead != root.BaseHead || g.Capability == nil || g.Capability.AgentVersionID != frozen.Agent.ID || g.Capability.ManifestHash != frozen.ManifestHash {
				return bad
			}
			childConfig, _ := json.Marshal(g.Config)
			childPrice, _ := json.Marshal(g.Price)
			if !recipeJSONEqual(childConfig, configRaw) || !recipeJSONEqual(childPrice, priceRaw) || g.ContextID != input.ContextID || g.Role != frozen.Agent.Definition.Role {
				return bad
			}
		}
	}
	for id, root := range roots {
		_, retiredLayout := definitions[root.Definition.ID]
		for _, step := range root.Definition.Definition.Steps {
			h := histories[id+":"+archiveRecipeKeyHash(retiredLayout, step.Key)]
			if h == nil || h.active != h.max || len(h.attempts) != h.max || index["recipe_runs"][id]["state"] == "COMPLETED" && h.activeState != "COMPLETED" {
				return bad
			}
		}
	}
	for id, row := range index["generation_tasks"] {
		var g struct {
			State string `json:"state"`
			Run   string `json:"recipe_run_id"`
			Step  string `json:"recipe_step_id"`
			Input string `json:"recipe_input_hash"`
		}
		if proof, ok := expired[id]; ok {
			if proof.Summary.Generation != nil {
				g.Run, g.Step, g.Input = proof.Summary.Generation.RecipeRunID, proof.Summary.Generation.RecipeStepID, proof.Summary.Generation.RecipeInputHash
			} else if children[id] != "" {
				// Older proofs carry no recipe relationship. Do not invent one
				// from a current step or silently authorize an unproven link.
				return bad
			}
		} else if json.Unmarshal([]byte(row["frozen"]), &g) != nil || g.State == "EXPIRED" {
			return bad
		}
		if g.Run != "" || g.Step != "" || g.Input != "" {
			if roots[g.Run].Protocol != 1 || children[id] != g.Step || g.Input == "" {
				return bad
			}
		}
	}
	return nil
}
