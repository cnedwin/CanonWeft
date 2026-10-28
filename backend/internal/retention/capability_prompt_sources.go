package retention

import (
	"context"
	"encoding/json"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/skills"
)

type capabilityPromptData struct {
	AgentVersion string          `json:"agent_version,omitempty"`
	Input        json.RawMessage `json:"input"`
	ManifestHash string          `json:"manifest_hash"`
	Methods      []struct {
		Files []struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"files"`
		Instructions string `json:"instructions"`
		PackageHash  string `json:"package_hash"`
		VersionID    string `json:"version_id"`
	} `json:"methods"`
}

func (b *contextSourceBuilder) capabilityPrompt(ctx context.Context, use *domain.CapabilityUse, request models.Request, recipeInputHash string, read ReadSourceObject) error {
	if use == nil {
		if recipeInputHash != "" {
			return ErrSourceOwnership
		}
		return nil
	}
	if use.Mode != "TRIAL" && use.Mode != "QUALIFIED" && use.Mode != "EVALUATION" {
		return ErrSourceOwnership
	}
	if use.AgentVersionID == "" {
		// The no-method arm is verified by qualificationUse against the frozen
		// suite/case, not a fabricated agent.
		if use.Mode != "EVALUATION" || use.ManifestHash != "" {
			return ErrSourceOwnership
		}
		return nil
	}
	row, err := b.index.sourceRow("capability_versions", map[string]string{"id": use.AgentVersionID})
	if err != nil {
		return err
	}
	manifest, err := b.capabilityManifest(ctx, domain.CapabilityRef{ID: use.AgentVersionID, Hash: row.Values["content_hash"], Kind: row.Values["kind"]}, use.ManifestHash, read)
	if err != nil {
		return err
	}
	trial := use.Mode == "TRIAL"
	open, close, _ := models.CapabilityMethodFrame(trial)
	start := strings.LastIndex(request.Prompt, open)
	if start < 0 {
		return ErrSourceOwnership
	}
	payloadStart := start + len(open)
	end := strings.Index(request.Prompt[payloadStart:], close)
	if end < 0 {
		return ErrSourceOwnership
	}
	end += payloadStart
	var payload capabilityPromptData
	if authorDocument(request.Prompt[payloadStart:end], &payload) != nil || payload.ManifestHash != use.ManifestHash || (trial && payload.AgentVersion != use.AgentVersionID) || (!trial && payload.AgentVersion != "") {
		return ErrSourceOwnership
	}
	bundles := map[string]skills.Bundle{}
	for _, v := range manifest.Versions {
		if v.Definition.Kind != "skill" {
			continue
		}
		if _, ok := bundles[v.Definition.PackageID]; ok {
			continue
		}
		bundle, err := b.capabilityPackage(ctx, v.Definition.PackageID, v.Definition.PackageHash, read)
		if err != nil {
			return err
		}
		bundles[v.Definition.PackageID] = bundle
	}
	input := payload.Input
	if recipeInputHash != "" {
		data, err := read(ctx, b.project, recipeInputHash)
		if err != nil {
			return err
		}
		input = data
	}
	want, err := models.AppendCapabilityMethod(models.Request{}, manifest.CapabilityVersion, use.ManifestHash, manifest.Versions, bundles, input, trial)
	if err != nil || request.Prompt[start:end+len(close)] != want.Prompt || !strings.Contains(request.System, want.System) || capabilityProjectionHash(request.Schema) != capabilityProjectionHash(want.Schema) {
		return ErrSourceOwnership
	}
	return nil
}
