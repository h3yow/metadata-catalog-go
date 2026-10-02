package catalog

import "testing"

func baseSpec() EntitySpec {
	return EntitySpec{
		Name:   "orders",
		Source: "warehouse",
		Kind:   "table",
		Owners: []string{"data-team"},
		Tags:   []string{"core"},
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt, Required: true},
			{Name: "amount", Type: TypeFloat},
		},
	}
}

func TestDiffSpecsCompatibleAddOptionalField(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Fields = append(new.Fields, FieldSpec{Name: "note", Type: TypeString})

	res := diffSpecs(old, new)
	t.Logf("input: old=%+v new=%+v", old.Fields, new.Fields)
	t.Logf("verdict: duplicate=%v changed=%v conflicts=%v reason=%q", res.duplicate, res.changed, res.conflicts, res.reason)

	if res.duplicate {
		t.Fatal("expected a change, got duplicate")
	}
	if len(res.conflicts) != 0 {
		t.Fatalf("expected compatible evolution, got conflicts: %+v", res.conflicts)
	}
}

func TestDiffSpecsFieldRemoved(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Fields = new.Fields[:1] // drop "amount"

	res := diffSpecs(old, new)
	t.Logf("input: old fields=%v new fields=%v", fieldNames(old), fieldNames(new))
	t.Logf("verdict: conflicts=%+v", res.conflicts)

	assertConflict(t, res, "amount", conflictFieldRemoved)
}

func TestDiffSpecsTypeChanged(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Fields[1].Type = TypeString

	res := diffSpecs(old, new)
	t.Logf("input: amount type %s -> %s", old.Fields[1].Type, new.Fields[1].Type)
	t.Logf("verdict: conflicts=%+v", res.conflicts)

	assertConflict(t, res, "amount", conflictTypeChanged)
}

func TestDiffSpecsRequiredAdded(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Fields = append(new.Fields, FieldSpec{Name: "tenant", Type: TypeString, Required: true})

	res := diffSpecs(old, new)
	t.Logf("input: new required field 'tenant' added")
	t.Logf("verdict: conflicts=%+v", res.conflicts)

	assertConflict(t, res, "tenant", conflictRequiredAdded)
}

func TestDiffSpecsOptionalTightenedToRequired(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Fields[1].Required = true

	res := diffSpecs(old, new)
	t.Logf("input: amount required %v -> %v", old.Fields[1].Required, new.Fields[1].Required)
	t.Logf("verdict: conflicts=%+v", res.conflicts)

	assertConflict(t, res, "amount", conflictRequiredAdded)
}

func TestDiffSpecsDuplicateIgnoresOrder(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	// Same content, different order of fields/owners/tags.
	new.Fields = []FieldSpec{old.Fields[1], old.Fields[0]}
	new.Tags = []string{"core", "core"}

	res := diffSpecs(old, new)
	t.Logf("input: identical content, reordered fields and duplicated tag")
	t.Logf("verdict: duplicate=%v changed=%v conflicts=%v", res.duplicate, res.changed, res.conflicts)

	if !res.duplicate {
		t.Fatalf("expected duplicate, got changed=%v conflicts=%v", res.changed, res.conflicts)
	}
}

func TestDiffSpecsMetadataChangeCompatible(t *testing.T) {
	old := baseSpec()
	new := baseSpec()
	new.Description = "order facts"
	new.Owners = []string{"data-team", "finance"}
	new.Tags = []string{"core", "pii"}

	res := diffSpecs(old, new)
	t.Logf("input: description/owners/tags changed")
	t.Logf("verdict: duplicate=%v conflicts=%v reason=%q", res.duplicate, res.conflicts, res.reason)

	if res.duplicate || len(res.conflicts) != 0 {
		t.Fatalf("expected compatible change, got duplicate=%v conflicts=%v", res.duplicate, res.conflicts)
	}
}

func TestValidateSpec(t *testing.T) {
	cases := []struct {
		name string
		spec EntitySpec
	}{
		{"empty name", EntitySpec{Source: "s"}},
		{"empty source", EntitySpec{Name: "n"}},
		{"duplicate field", EntitySpec{Name: "n", Source: "s", Fields: []FieldSpec{
			{Name: "a", Type: TypeInt}, {Name: "a", Type: TypeInt},
		}}},
		{"unknown type", EntitySpec{Name: "n", Source: "s", Fields: []FieldSpec{
			{Name: "a", Type: "mystery"},
		}}},
	}
	for _, tc := range cases {
		err := validateSpec(tc.spec)
		t.Logf("input: %s -> err=%v", tc.name, err)
		ce, ok := AsCatalogError(err)
		if !ok || ce.Reason != ReasonInvalidSpec {
			t.Fatalf("%s: expected invalid_spec error, got %v", tc.name, err)
		}
	}
}

func fieldNames(s EntitySpec) []string {
	var out []string
	for _, f := range s.Fields {
		out = append(out, f.Name)
	}
	return out
}

func assertConflict(t *testing.T, res evolutionResult, field, kind string) {
	t.Helper()
	for _, c := range res.conflicts {
		if c.Field == field && c.Kind == kind {
			return
		}
	}
	t.Fatalf("expected conflict %s/%s, got %+v", field, kind, res.conflicts)
}
