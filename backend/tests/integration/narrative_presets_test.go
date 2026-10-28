package integration

import "testing"

func TestNarrativePresetsInstallPinnedCandidatesWithoutSelfQualification(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "记忆与改编方法", "narrative-presets")
	root := "/api/projects/" + pid + "/capabilities"
	for _, tc := range []struct{ endpoint, role string }{{"memory", "bookkeeper"}, {"adaptation", "continuity_editor"}, {"character-visual", "asset_designer"}} {
		t.Run(tc.endpoint, func(t *testing.T) {
			installed := b.request("POST", root+"/presets/"+tc.endpoint, map[string]any{}, 201)
			repeated := b.request("POST", root+"/presets/"+tc.endpoint, map[string]any{}, 201)
			if installed["root_id"] != repeated["root_id"] || len(installed["versions"].([]any)) != 3 {
				t.Fatalf("installation receipt mismatch: root=%v repeated=%v versions=%d", installed["root_id"], repeated["root_id"], len(installed["versions"].([]any)))
			}
			recipe := b.request("GET", root+"/"+installed["root_id"].(string), nil, 200)
			definition := recipe["definition"].(map[string]any)
			steps := definition["steps"].([]any)
			if len(steps) != 1 || steps[0].(map[string]any)["wait_for_confirmation"] != true {
				t.Fatalf("recipe lacks independent confirmation gate: steps=%d", len(steps))
			}
			agentID := steps[0].(map[string]any)["agent_id"].(string)
			agent := b.request("GET", root+"/"+agentID, nil, 200)
			if agent["definition"].(map[string]any)["role"] != tc.role {
				t.Fatalf("agent role=%v want=%s", agent["definition"].(map[string]any)["role"], tc.role)
			}
			// TrialReady describes a directly callable Agent, never a Recipe.
			// Recipe preparation checks each child Agent's manifest separately.
			agentManifest := b.request("GET", root+"/"+agentID+"/manifest", nil, 200)
			assertNarrativeManifest(t, agentManifest, "agent", true, 2)
			manifest := b.request("GET", root+"/"+installed["root_id"].(string)+"/manifest", nil, 200)
			assertNarrativeManifest(t, manifest, "recipe", false, 3)
		})
	}
}

func assertNarrativeManifest(t *testing.T, manifest map[string]any, kind string, trialReady bool, versions int) {
	t.Helper()
	codes := []string{}
	for _, finding := range manifest["findings"].([]any) {
		codes = append(codes, finding.(map[string]any)["code"].(string))
	}
	actualKind := manifest["root"].(map[string]any)["kind"]
	actualVersions := len(manifest["versions"].([]any))
	if actualKind != kind || manifest["qualified"] != false || manifest["trial_ready"] != trialReady || actualVersions != versions || len(codes) != 1 || codes[0] != "QUALIFICATION_REQUIRED" {
		t.Fatalf("manifest: kind=%v qualified=%v trial_ready=%v versions=%d findings=%v; want kind=%s qualified=false trial_ready=%t versions=%d and only QUALIFICATION_REQUIRED", actualKind, manifest["qualified"], manifest["trial_ready"], actualVersions, codes, kind, trialReady, versions)
	}
}
