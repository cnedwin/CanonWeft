package retention

import (
	"context"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/tooltrial"
)

func (index *SourceIndex) attachToolSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "tool_trials" || (m.Field != "frozen" && m.Field != "frozen.input_hash" && m.Field != "output_hash") {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		var frozen tooltrial.Frozen
		if authorDocument(row.Values["frozen"], &frozen) != nil || row.Values["capability_id"] != frozen.VersionID || frozen.ImageHash != isolation.ApprovedImageSHA256 {
			return ErrSourceOwnership
		}
		b := contextSourceBuilder{index: index, project: project, actor: row.Values["actor_id"], m: &m.MaterialReference}
		if m.Field == "frozen" {
			if err := b.link(row, "frozen.input_hash", financialDigest([]byte(frozen.InputHash))); err != nil {
				return err
			}
			continue
		}
		v, err := b.capabilityManifest(ctx, domain.CapabilityRef{ID: frozen.VersionID, Hash: frozen.VersionHash, Kind: "tool"}, frozen.ManifestHash, read)
		if err != nil || v.Definition.PackageID != frozen.PackageID || v.Definition.PackageHash != frozen.PackageHash {
			return ErrSourceOwnership
		}
		bundle, err := b.capabilityPackage(ctx, frozen.PackageID, frozen.PackageHash, read)
		if err != nil {
			return err
		}
		data, err := read(ctx, project, frozen.InputHash)
		var actual isolation.Request
		if err != nil || contentJSON(data, &actual, frozen.InputHash) != nil {
			return ErrSourceOwnership
		}
		// Prepare validates schema and copies approved package bytes; it never
		// launches the VM or evaluates any script. Compare every request field.
		want, err := tooltrial.Prepare(v.Definition, bundle, actual.Input)
		if err != nil || !exactJSONBytes(want, data) {
			return ErrSourceOwnership
		}
		if !json.Valid(actual.Input) {
			return ErrSourceOwnership
		}
		if m.Field == "frozen.input_hash" {
			if err := b.toolQualification(ctx, row, frozen, actual, read); err != nil {
				return err
			}
		}
		if m.Field == "output_hash" {
			if err := b.toolOutput(ctx, row, frozen, v.Definition, actual, read); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *contextSourceBuilder) toolOutput(ctx context.Context, row SourceRow, frozen tooltrial.Frozen, definition domain.CapabilityDefinition, request isolation.Request, read ReadSourceObject) error {
	data, err := read(ctx, b.project, row.Values["output_hash"])
	var output tooltrial.Output
	if err != nil || contentJSON(data, &output, row.Values["output_hash"]) != nil {
		return ErrSourceOwnership
	}
	hash, err := isolation.RequestHash(request)
	result := output.Result
	if err != nil || result.InputHash != hash || result.ImageHash != frozen.ImageHash || len(result.Stdout)+len(result.Stderr) > request.MaxOutputBytes {
		return ErrSourceOwnership
	}
	switch result.Reason {
	case "completed", "timeout", "output_limit":
	default:
		return ErrSourceOwnership
	}
	want := tooltrial.Output{Result: result, Stdout: string(result.Stdout), Stderr: string(result.Stderr), Valid: tooltrial.ValidateOutput(definition, result) == nil}
	// Re-marshal to reproduce JSON's replacement of invalid UTF-8 display text;
	// the raw receipt bytes remain unchanged and invalid outputs remain invalid.
	if !exactJSONBytes(want, data) {
		return ErrSourceOwnership
	}
	if err := b.link(row, "frozen", ""); err != nil {
		return err
	}
	return b.link(row, "frozen.input_hash", financialDigest([]byte(frozen.InputHash)))
}
