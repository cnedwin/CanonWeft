package retention

import "testing"

func TestToolCaseInputPreservesExactValues(t *testing.T) {
	for _, tc := range []struct {
		actual, expected string
		match            bool
	}{
		{`{"source":"渡口","n":1.0}`, `{"n":1e0,"source":"渡口"}`, true},
		{`{"source":"隐藏输入","source":"渡口"}`, `{"source":"渡口"}`, false},
		{`{"source":"渡口"}`, `{"source":"隐藏输入","source":"渡口"}`, false},
		{`{"n":9007199254740993}`, `{"n":9007199254740992}`, false},
		{`{"n":null}`, `{}`, false},
		{`{"source":[1,2]}`, `{"source":[2,1]}`, false},
		{`{"source":"渡口"} {}`, `{"source":"渡口"}`, false},
	} {
		if toolCaseInputMatches([]byte(tc.actual), []byte(tc.expected)) != tc.match {
			t.Fatalf("case input equality mismatch: %s vs %s", tc.actual, tc.expected)
		}
	}
}
