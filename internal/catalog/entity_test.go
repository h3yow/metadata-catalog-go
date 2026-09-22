package catalog

import "testing"

func baseSpec() EntitySpec {
	return EntitySpec{
		Name:   "orders",
		Source: "billing-team",
		Kind:   "table",
		Fields: []FieldSpec{
			{Name: "id", Type: "int64", Semantic: "order.id", Optional: false},
			{Name: "amount", Type: "decimal", Semantic: "order.amount", Optional: false},
		},
		Tags:   []string{"finance"},
		Owners: []string{"alice"},
	}
}

func TestRegisterCreate(t *testing.T) {
	c := New()
	res, err := c.Register(baseSpec())
	judge(t, "create entity orders with 2 fields", res, err)
	assertNoErr(t, err)
	if res.Change != ChangeCreated || res.Version != 1 {
		t.Fatalf("want created@v1, got %+v", res)
	}
	got, err := c.Get("orders")
	assertNoErr(t, err)
	if got.Version != 1 || len(got.Fields) != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestRegisterDuplicateExact(t *testing.T) {
	c := New()
	r1, err := c.Register(baseSpec())
	assertNoErr(t, err)
	// 同一来源、完全一致的载荷重复上报
	r2, err := c.Register(baseSpec())
	judge(t, "repeat exact same report from same source", r2, err)
	assertNoErr(t, err)
	if r2.Change != ChangeNoop || !r2.RepeatReport {
		t.Fatalf("want noop repeat, got %+v", r2)
	}
	if r2.Version != 1 {
		t.Fatalf("version must stay 1, got %d", r2.Version)
	}
	hist, err := c.History("orders")
	assertNoErr(t, err)
	if len(hist) != 1 {
		t.Fatalf("history must not be rewritten/extended on duplicate, got %d versions", len(hist))
	}
	_ = r1
}

func TestRegisterDuplicateFromAnotherSource(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	other := baseSpec()
	other.Source = "platform-scanner"
	res, err := c.Register(other)
	judge(t, "identical report from a different source", res, err)
	assertNoErr(t, err)
	if res.Change != ChangeNoop || res.RepeatReport {
		t.Fatalf("want noop but first sighting of source, got %+v", res)
	}
	got, _ := c.Get("orders")
	if len(got.Sources) != 2 || got.Sources[0] != "billing-team" || got.Sources[1] != "platform-scanner" {
		t.Fatalf("sources should accumulate in first-seen order, got %v", got.Sources)
	}
}

func TestRegisterCompatibleAddOptionalField(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	evolved := baseSpec()
	evolved.Fields = append(evolved.Fields,
		FieldSpec{Name: "coupon", Type: "string", Semantic: "order.coupon", Optional: true})
	res, err := c.Register(evolved)
	judge(t, "add optional field coupon", res, err)
	assertNoErr(t, err)
	if res.Change != ChangeCompatible || res.Version != 2 {
		t.Fatalf("want compatible@v2, got %+v", res)
	}
	if len(res.Changed) != 1 || res.Changed[0].Field != "coupon" {
		t.Fatalf("want one field change for coupon, got %+v", res.Changed)
	}
	hist, _ := c.History("orders")
	if len(hist) != 2 || hist[1].Version != 2 {
		t.Fatalf("history should append v2 immutably, got %+v", hist)
	}
	if hist[0].Version != 1 || len(hist[0].Spec.Fields) != 2 {
		t.Fatalf("v1 snapshot must remain unchanged, got %+v", hist[0])
	}
}

func TestRejectAddRequiredField(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	evolved := baseSpec()
	evolved.Fields = append(evolved.Fields,
		FieldSpec{Name: "tenant_id", Type: "int64", Semantic: "tenant.id", Optional: false})
	_, err := c.Register(evolved)
	judge(t, "add REQUIRED field tenant_id", nil, err)
	assertReason(t, err, ReasonRequiredFieldAdded)
	got, _ := c.Get("orders")
	if got.Version != 1 || len(got.Fields) != 2 {
		t.Fatalf("rejected change must not mutate entity, v=%d fields=%d", got.Version, len(got.Fields))
	}
}

func TestRejectFieldRemoved(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	shrunk := baseSpec()
	shrunk.Fields = shrunk.Fields[:1]
	_, err := c.Register(shrunk)
	judge(t, "drop registered field amount", nil, err)
	assertReason(t, err, ReasonFieldRemoved)
	if !errContains(err, "amount") {
		t.Fatalf("detail should name removed field: %v", err)
	}
}

func TestRejectFieldTypeChanged(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	changed := baseSpec()
	changed.Fields[1].Type = "string" // amount decimal -> string
	_, err := c.Register(changed)
	judge(t, "change field amount type decimal->string", nil, err)
	assertReason(t, err, ReasonFieldTypeChanged)
}

func TestRejectFieldSemanticChanged(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	changed := baseSpec()
	changed.Fields[0].Semantic = "invoice.id" // order.id -> invoice.id
	_, err := c.Register(changed)
	judge(t, "change field id semantic order.id->invoice.id", nil, err)
	assertReason(t, err, ReasonFieldSemanticChanged)
}

func TestRejectKindChanged(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	changed := baseSpec()
	changed.Kind = "topic"
	_, err := c.Register(changed)
	judge(t, "change entity kind table->topic", nil, err)
	assertReason(t, err, ReasonKindChanged)
}

func TestInvalidInput(t *testing.T) {
	c := New()
	bad := baseSpec()
	bad.Name = ""
	_, err := c.Register(bad)
	judge(t, "entity with empty name", nil, err)
	assertReason(t, err, ReasonInvalidInput)

	bad2 := baseSpec()
	bad2.Fields[1].Type = ""
	_, err = c.Register(bad2)
	judge(t, "field with empty type", nil, err)
	assertReason(t, err, ReasonInvalidInput)
}

func TestMetadataUnionDoesNotDuplicate(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	withTag := baseSpec()
	withTag.Tags = []string{"finance", "pii"}
	withTag.Owners = []string{"bob"}
	res, err := c.Register(withTag)
	assertNoErr(t, err)
	judge(t, "report adds tag pii and owner bob", res, err)
	if res.Version != 2 {
		t.Fatalf("metadata union bumps version, got %d", res.Version)
	}
	got, _ := c.Get("orders")
	if len(got.Tags) != 2 || got.Tags[0] != "finance" || got.Tags[1] != "pii" {
		t.Fatalf("tags want [finance pii], got %v", got.Tags)
	}
	if len(got.Owners) != 2 {
		t.Fatalf("owners want union, got %v", got.Owners)
	}
}
