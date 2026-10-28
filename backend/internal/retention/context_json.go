package retention

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// MySQL JSON serializes retrieval scores as doubles (including 0.0 and E
// notation). Compare numeric values exactly while retaining exact object keys,
// array order, nulls and the typed canonical digest. Financial rules are unchanged.
func contextJSON(raw []byte, target any, hash string) error {
	if !hash64.MatchString(hash) || strictJSONWithLimit(raw, target, 16<<20) != nil {
		return ErrSourceOwnership
	}
	canonical, err := json.Marshal(target)
	if err != nil || financialDigest(canonical) != hash {
		return ErrSourceOwnership
	}
	decode := func(raw []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		err := d.Decode(&v)
		return v, err
	}
	a, err := decode(raw)
	if err != nil {
		return ErrSourceOwnership
	}
	b, err := decode(canonical)
	if err != nil || !equalContextJSON(a, b) {
		return ErrSourceOwnership
	}
	return nil
}

func contextNumber(text string) (*big.Rat, bool) {
	if len(text) > 128 {
		return nil, false
	}
	if at := strings.IndexAny(text, "eE"); at >= 0 {
		exponent, err := strconv.Atoi(text[at+1:])
		if err != nil || exponent < -400 || exponent > 400 {
			return nil, false
		}
	}
	return new(big.Rat).SetString(text)
}

func equalContextJSON(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !equalContextJSON(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !equalContextJSON(left[i], right[i]) {
				return false
			}
		}
		return true
	case json.Number:
		right, ok := b.(json.Number)
		if !ok {
			return false
		}
		if left == right {
			return true
		}
		x, xOK := contextNumber(string(left))
		y, yOK := contextNumber(string(right))
		return xOK && yOK && x.Cmp(y) == 0
	default:
		return a == b
	}
}
