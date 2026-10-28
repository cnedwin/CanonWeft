package retention

import "testing"

func TestNovelVisualBasisPreservesValuesAcrossObjectOrdering(t *testing.T) {
	original := []byte(`{"setup":{"title":"钟楼","revision":9007199254740993},"characters":["a","b"]}`)
	if !novelVisualBasisEqual(original, []byte(`{"characters":["a","b"],"setup":{"revision":9007199254740993,"title":"钟楼"}}`)) {
		t.Fatal("object order changed identity")
	}
	for _, changed := range []string{`{"setup":{"title":"钟楼","revision":9007199254740992},"characters":["a","b"]}`, `{"setup":{"title":"钟楼","revision":9007199254740993},"characters":["b","a"]}`, `null`, `{`} {
		if novelVisualBasisEqual(original, []byte(changed)) {
			t.Fatal("different basis accepted", changed)
		}
	}
}
