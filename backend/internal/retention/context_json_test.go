package retention

import (
	"encoding/json"
	"testing"
)

func TestContextJSONAllowsSQLFloatNotationOnly(t *testing.T) {
	type document struct {
		Revision int64   `json:"revision"`
		Score    float64 `json:"score"`
		Zero     float64 `json:"zero"`
	}
	want := document{Revision: 9007199254740993, Score: 0.000001, Zero: 0}
	raw, _ := json.Marshal(want)
	hash := financialDigest(raw)
	for _, input := range []string{string(raw), `{"revision":9007199254740993,"score":1E-6,"zero":0.0}`} {
		var got document
		if err := contextJSON([]byte(input), &got, hash); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{
		`{"revision":9007199254740992,"score":1E-6,"zero":0.0}`,
		`{"revision":9007199254740993,"Score":1E-6,"zero":0.0}`,
		`{"revision":9007199254740993,"score":1E-6,"score":1E-6,"zero":0.0}`,
		`{"revision":9007199254740993,"score":1E-6,"zero":0E99999999}`,
	} {
		var got document
		if contextJSON([]byte(input), &got, hash) == nil {
			t.Fatal("altered shape/value accepted", input)
		}
	}
}
