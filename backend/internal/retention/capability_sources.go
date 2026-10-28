package retention

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

// Reconstruct the immutable manifest only. Never consult the mutable head,
// grant qualification, or discard the original version after revocation.
type frozenCapabilityManifest struct {
	domain.CapabilityVersion
	Versions     []domain.CapabilityVersion
	ManifestHash string
}

func (b *contextSourceBuilder) capabilityManifest(ctx context.Context, root domain.CapabilityRef, hash string, read ReadSourceObject) (frozenCapabilityManifest, error) {
	manifest, err := b.capabilityClosure(ctx, root, read)
	if err != nil {
		return manifest, err
	}
	if manifest.ManifestHash != hash {
		return manifest, ErrSourceOwnership
	}
	return manifest, nil
}

// Some older frozen contracts retain a full immutable root reference but no
// manifest digest. Follow only that root's immutable dependencies in that case.
func (b *contextSourceBuilder) capabilityClosure(ctx context.Context, root domain.CapabilityRef, read ReadSourceObject) (frozenCapabilityManifest, error) {
	var first frozenCapabilityManifest
	pending := []domain.CapabilityRef{root}
	seen := map[string]domain.CapabilityVersion{}
	versions := []domain.CapabilityVersion{}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return first, err
		}
		ref := pending[0]
		pending = pending[1:]
		if prior, ok := seen[ref.ID]; ok {
			if prior.Hash != ref.Hash || prior.Definition.Kind != ref.Kind {
				return first, ErrSourceOwnership
			}
			continue
		}
		if len(seen) >= 256 || !id32.MatchString(ref.ID) {
			return first, ErrSourceOwnership
		}
		row, err := b.index.sourceRow("capability_versions", map[string]string{"id": ref.ID})
		var definition domain.CapabilityDefinition
		if err != nil || row.Values["project_id"] != b.project || row.Values["content_hash"] != ref.Hash || row.Values["kind"] != ref.Kind || authorDocument(row.Values["definition"], &definition) != nil || definition.Validate() != nil || definition.Kind != ref.Kind || definition.Key != row.Values["capability_key"] || authorCapabilityHash(row.Values["parent_id"], definition) != ref.Hash {
			return first, ErrSourceOwnership
		}
		// The driver exposes DATETIME as RFC3339 when parseTime is enabled;
		// capabilityTx uses CAST AS CHAR, including all six fractional digits.
		created, err := time.Parse(time.RFC3339Nano, row.Values["created_at"])
		if err != nil {
			created, err = time.Parse("2006-01-02 15:04:05.999999", row.Values["created_at"])
		}
		if err != nil || created.IsZero() {
			return first, ErrSourceOwnership
		}
		v := domain.CapabilityVersion{ID: ref.ID, Hash: ref.Hash, ParentID: row.Values["parent_id"], Definition: definition, InputSchemaText: string(definition.InputSchema), OutputSchemaText: string(definition.OutputSchema), CreatedAt: created.Format("2006-01-02 15:04:05.000000")}
		if ref.ID == root.ID {
			first.CapabilityVersion = v
		}
		seen[ref.ID] = v
		versions = append(versions, v)
		pending = append(pending, definition.Dependencies...)
		if err := b.link(row, "definition", ""); err != nil {
			return first, err
		}
		if definition.PackageID != "" {
			if _, err := b.capabilityPackage(ctx, definition.PackageID, definition.PackageHash, read); err != nil {
				return first, err
			}
		}
	}
	slices.SortFunc(versions, func(a, c domain.CapabilityVersion) int { return strings.Compare(a.ID, c.ID) })
	first.ManifestHash = capabilityProjectionHash([]any{root, versions})
	first.Versions = versions
	return first, nil
}

func (b *contextSourceBuilder) capabilityPackage(ctx context.Context, id, hash string, read ReadSourceObject) (skills.Bundle, error) {
	var bundle skills.Bundle
	row, err := b.index.sourceRow("skill_packages", map[string]string{"id": id})
	if err != nil || row.Values["project_id"] != b.project || row.Values["bundle_hash"] != hash {
		return bundle, ErrSourceOwnership
	}
	data, err := read(ctx, b.project, hash)
	if err != nil || contentJSON(data, &bundle, hash) != nil || bundle.Validate() != nil {
		return bundle, ErrSourceOwnership
	}
	var report skills.Report
	if authorDocument(row.Values["report"], &report) != nil || report.Hash != hash {
		return bundle, ErrSourceOwnership
	}
	for _, field := range []string{"bundle_hash", "report", "source"} {
		if err := b.link(row, field, ""); err != nil {
			return bundle, err
		}
	}
	return bundle, nil
}

func exactJSONBytes(value any, expected []byte) bool {
	raw, err := json.Marshal(value)
	return err == nil && string(raw) == string(expected)
}
