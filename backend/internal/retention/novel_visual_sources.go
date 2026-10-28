package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"strconv"
)

// Method envelopes canonicalize nested JSON objects; equality ignores only
// object-key ordering, preserving arrays, field values and exact JSON numbers.
func novelVisualBasisEqual(left, right []byte) bool {
	decode := func(raw []byte) (any, bool) {
		if !json.Valid(raw) {
			return nil, false
		}
		var value any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&value)
		return value, err == nil
	}
	a, ok := decode(left)
	if !ok {
		return false
	}
	b, ok := decode(right)
	return ok && stableHash(a) == stableHash(b)
}

func (b *contextSourceBuilder) novelVisual(ctx context.Context, pin *models.CharacterImageTask, read ReadSourceObject) error {
	if pin.Novel == nil {
		return nil
	}
	target := pin.Novel
	if !id32.MatchString(target.BookID) || (target.Kind != "cover" && target.Kind != "scene") || pin.CharacterID != "" || financialDigest([]byte(pin.Basis)) != pin.CharacterHash {
		return ErrSourceOwnership
	}
	book, err := b.index.sourceRow("novels", map[string]string{"id": target.BookID})
	if err != nil || book.Values["project_id"] != b.project || book.Values["branch_id"] != b.branch {
		return ErrSourceOwnership
	}
	var basis models.NovelImageBasis
	if json.Unmarshal([]byte(pin.Basis), &basis) != nil || basis.SettingsRevision < 1 {
		return ErrSourceOwnership
	}
	source, err := b.index.sourceRow("novel_settings_versions", map[string]string{"novel_id": target.BookID, "revision": strconv.FormatInt(basis.SettingsRevision, 10)})
	if err != nil {
		return ErrSourceOwnership
	}
	var expected, actual novel.Setup
	if json.Unmarshal([]byte(source.Values["payload"]), &expected) != nil || json.Unmarshal(basis.Setup, &actual) != nil || stableHash(expected) != stableHash(actual) {
		return ErrSourceOwnership
	}
	if err = b.link(source, "payload", ""); err != nil {
		return err
	}
	for _, input := range basis.Required {
		if err = b.required(ctx, input, read); err != nil {
			return err
		}
	}
	return nil
}
