package retention

import (
	"slices"
	"testing"
)

// Archive coverage checks call Column once per source column, for every replay
// state. Those value lookups must not clone a complete owner contract or parse
// the primary-key catalog. Public slice-returning APIs still need their copies.
func TestMetadataLookupAllocationBudget(t *testing.T) {
	for _, table := range []string{"users", "generation_previews", "qualification_runs", "capability_heads"} {
		t.Run(table, func(t *testing.T) {
			spec, ok := OwnerSpec(table)
			if !ok {
				t.Fatal("missing reviewed contract")
			}
			var names []string
			for _, policy := range spec.Columns {
				names = append(names, policy.Column)
			}
			var policy ColumnPolicy
			allocs := testing.AllocsPerRun(100, func() { policy, ok = Column(table, names[0]) })
			if !ok || policy.Column != names[0] || allocs != 0 {
				t.Errorf("Column: found=%v column=%q allocations=%g, want zero allocations", ok, policy.Column, allocs)
			}
			var err error
			allocs = testing.AllocsPerRun(100, func() { err = ValidateColumnCoverage(table, names) })
			// Allow the small duplicate-detection map; never a contract copy per column.
			if err != nil || allocs > 3 {
				t.Errorf("coverage: error=%v allocations=%g, want at most 3", err, allocs)
			}
			var key []KeyColumn
			allocs = testing.AllocsPerRun(100, func() { key = PrimaryKey(table) })
			if len(key) == 0 || allocs > 1 {
				t.Errorf("PrimaryKey: allocations=%g, want only one defensive copy", allocs)
			}
			allocs = testing.AllocsPerRun(100, func() { spec, ok = OwnerSpec(table) })
			if !ok || allocs > 2 {
				t.Errorf("OwnerSpec: allocations=%g, want only two defensive copies", allocs)
			}
		})
	}
}

func TestOwnerMetadataCopiesCannotAlterSubsequentLookups(t *testing.T) {
	wantKey := []KeyColumn{{Name: "project_id", Kind: KeyID}, {Name: "capability_key", Kind: KeyText, FreeText: true}}
	spec, ok := OwnerSpec("capability_heads")
	if !ok || !slices.Equal(spec.PrimaryKey, wantKey) {
		t.Fatal("unexpected composite key", spec.PrimaryKey)
	}
	policy, ok := Column("capability_heads", "project_id")
	if !ok {
		t.Fatal("missing project key column")
	}
	for i := range spec.Columns {
		spec.Columns[i] = ColumnPolicy{}
	}
	spec.PrimaryKey[0] = KeyColumn{}
	key := PrimaryKey("capability_heads")
	key[1] = KeyColumn{}
	again, ok := OwnerSpec("capability_heads")
	got, found := Column("capability_heads", "project_id")
	if !ok || !found || got != policy || !slices.Equal(again.PrimaryKey, wantKey) || !slices.Equal(PrimaryKey("capability_heads"), wantKey) {
		t.Fatal("caller altered fixed metadata")
	}
	for _, unknown := range []string{"", "CAPABILITY_HEADS", "future_table"} {
		if _, ok := OwnerSpec(unknown); ok {
			t.Fatal("unknown owner accepted", unknown)
		}
		if _, ok := Column(unknown, "project_id"); ok || PrimaryKey(unknown) != nil || ValidateColumnCoverage(unknown, nil) == nil {
			t.Fatal("unknown metadata accepted", unknown)
		}
	}
}

func BenchmarkColumnMetadataLookup(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := Column("qualification_runs", "id"); !ok {
			b.Fatal("missing reviewed column")
		}
	}
}

func BenchmarkColumnCoverageLookup(b *testing.B) {
	spec, _ := OwnerSpec("qualification_runs")
	var names []string
	for _, policy := range spec.Columns {
		names = append(names, policy.Column)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := ValidateColumnCoverage("qualification_runs", names); err != nil {
			b.Fatal(err)
		}
	}
}
