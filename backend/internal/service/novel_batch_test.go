package service

import "testing"

func TestNovelBatchMustBeContiguous(t *testing.T) {
	order := []string{"a", "b", "c", "d"}
	for _, ids := range [][]string{{}, {"b", "b"}, {"a", "c"}, {"c", "b"}, {"missing"}} {
		if validateNovelBatchOrder(order, ids) == nil {
			t.Fatalf("accepted invalid chapter sequence %v", ids)
		}
	}
	if err := validateNovelBatchOrder(order, []string{"b", "c", "d"}); err != nil {
		t.Fatal(err)
	}
}
