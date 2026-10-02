package catalog

import (
	"fmt"
	"sync"
	"testing"
)

// twoSourceReport builds a report for the logical entity "orders"
// from the given source, carrying only the part that source owns.
func twoSourceReport(source string, fields ...FieldSpec) EntitySpec {
	return EntitySpec{
		Name:   "orders",
		Source: source,
		Kind:   "table",
		Fields: fields,
	}
}

func TestMultiSourceMergeUnion(t *testing.T) {
	c := New()

	// Business DB reports its part: id/amount, owner alice, tag core.
	biz := twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true},
		FieldSpec{Name: "amount", Type: TypeFloat},
	)
	biz.Owners = []string{"alice"}
	biz.Tags = []string{"core"}
	e1 := mustRegister(t, c, biz)

	// Warehouse reports its part of the SAME logical entity:
	// id (same type), note, owner bob, tag curated.
	wh := twoSourceReport("warehouse",
		FieldSpec{Name: "id", Type: TypeInt, Required: true},
		FieldSpec{Name: "note", Type: TypeString},
	)
	wh.Owners = []string{"bob"}
	wh.Tags = []string{"curated"}
	e2 := mustRegister(t, c, wh)

	t.Logf("input: bizdb{ id,amount; alice; core } + warehouse{ id,note; bob; curated }")
	t.Logf("verdict: one entity id=%s version=%d sources=%v", e2.ID, e2.Version, e2.Sources)

	if e1.ID != e2.ID {
		t.Fatalf("same logical entity split into %s and %s", e1.ID, e2.ID)
	}
	if len(e2.Sources) != 2 {
		t.Fatalf("expected 2 contributing sources, got %v", e2.Sources)
	}
	// Fields union, not overwrite.
	wantFields := map[string]FieldType{"id": TypeInt, "amount": TypeFloat, "note": TypeString}
	if len(e2.Fields) != len(wantFields) {
		t.Fatalf("expected merged fields %v, got %+v", wantFields, e2.Fields)
	}
	for _, f := range e2.Fields {
		if wantFields[f.Name] != f.Type {
			t.Fatalf("field %s: expected type %s, got %s", f.Name, wantFields[f.Name], f.Type)
		}
	}
	// Owners/tags union + dedup.
	if !sameStringSet(e2.Owners, []string{"alice", "bob"}) {
		t.Fatalf("owners not unioned: %v", e2.Owners)
	}
	if !sameStringSet(e2.Tags, []string{"core", "curated"}) {
		t.Fatalf("tags not unioned: %v", e2.Tags)
	}
	// Attribution: who contributed what.
	if got := e2.FieldSources["amount"]; len(got) != 1 || got[0] != "bizdb" {
		t.Fatalf("amount attribution wrong: %v", got)
	}
	if got := e2.FieldSources["id"]; len(got) != 2 {
		t.Fatalf("id should be attributed to both sources: %v", got)
	}
	if got := e2.OwnerSources["bob"]; len(got) != 1 || got[0] != "warehouse" {
		t.Fatalf("owner bob attribution wrong: %v", got)
	}
	if got := e2.TagSources["core"]; len(got) != 1 || got[0] != "bizdb" {
		t.Fatalf("tag core attribution wrong: %v", got)
	}
}

func TestMultiSourceDuplicateReportNoPileUp(t *testing.T) {
	c := New()
	biz := twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt})
	e1 := mustRegister(t, c, biz)
	mustRegister(t, c, twoSourceReport("warehouse", FieldSpec{Name: "note", Type: TypeString}))

	// bizdb re-reports exactly the same content.
	before := mustGet(t, c, e1.ID)
	_, err := c.Register(twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: bizdb identical re-report -> verdict: %v", err)
	if reasonOf(err) != ReasonDuplicateReport {
		t.Fatalf("expected duplicate_report, got %v", err)
	}

	got := mustGet(t, c, e1.ID)
	if got.Version != 2 {
		t.Fatalf("duplicate refreshed version: %d", got.Version)
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("duplicate refreshed UpdatedAt: %v -> %v", before.UpdatedAt, got.UpdatedAt)
	}
	reps, err := c.Reports(e1.ID, "bizdb")
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 {
		t.Fatalf("duplicate piled up in report log: %d bizdb reports", len(reps))
	}
	if len(got.Sources) != 2 {
		t.Fatalf("source info piled up: %v", got.Sources)
	}
}

func TestMultiSourceOmissionIsNotDeletion(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))

	// warehouse reports only the field it cares about; it never
	// mentions amount. That must not delete amount.
	e := mustRegister(t, c, twoSourceReport("warehouse", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: warehouse reports only {id} -> verdict: fields=%v", fieldNames(specFromEntity(*e)))

	found := false
	for _, f := range e.Fields {
		if f.Name == "amount" {
			found = true
		}
	}
	if !found {
		t.Fatal("field from another source was treated as deleted by omission")
	}

	// warehouse re-reports, again without amount: still not a
	// deletion, and not a duplicate either (its own report changed
	// only if content differs; identical re-report is duplicate).
	e2 := mustRegister(t, c, EntitySpec{
		Name: "orders", Source: "warehouse", Kind: "table",
		Fields:      []FieldSpec{{Name: "id", Type: TypeInt}},
		Description: "warehouse copy",
	})
	if len(e2.Fields) != 2 {
		t.Fatalf("omission deleted fields: %+v", e2.Fields)
	}
}

func TestMultiSourceNoOverwrite(t *testing.T) {
	c := New()
	biz := twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt})
	biz.Owners = []string{"alice"}
	biz.Tags = []string{"core"}
	biz.Description = "business orders"
	mustRegister(t, c, biz)

	// A later report from another source must not wipe existing
	// owners/tags/fields.
	wh := twoSourceReport("warehouse", FieldSpec{Name: "note", Type: TypeString})
	e := mustRegister(t, c, wh)
	t.Logf("input: warehouse minimal report after bizdb -> verdict: owners=%v tags=%v fields=%d",
		e.Owners, e.Tags, len(e.Fields))

	if !containsString(e.Owners, "alice") {
		t.Fatalf("later report overwrote owners: %v", e.Owners)
	}
	if !containsString(e.Tags, "core") {
		t.Fatalf("later report overwrote tags: %v", e.Tags)
	}
	if len(e.Fields) != 2 {
		t.Fatalf("later report overwrote fields: %+v", e.Fields)
	}
	if e.Description != "business orders" {
		t.Fatalf("later empty description overwrote: %q", e.Description)
	}
}

func TestMultiSourceTypeConflict(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb", FieldSpec{Name: "amount", Type: TypeFloat}))

	_, err := c.Register(twoSourceReport("warehouse", FieldSpec{Name: "amount", Type: TypeString}))
	ce, _ := AsCatalogError(err)
	t.Logf("input: warehouse reports amount:string vs merged float -> verdict: %v conflicts=%v",
		reasonOf(err), conflicts(ce))

	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictTypeChanged)
	// The conflict must name the responsible source.
	found := false
	for _, cf := range ce.Conflicts {
		if cf.Source == "warehouse" {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflict not attributed to a source: %+v", ce.Conflicts)
	}
	// Rejection must be total: warehouse did not become a contributor.
	got := mustGet(t, c, "orders.table")
	if len(got.Sources) != 1 || got.Sources[0] != "bizdb" {
		t.Fatalf("rejected report still contributed: %v", got.Sources)
	}
}

func TestMultiSourceExplicitRemovalRejected(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))

	spec := twoSourceReport("warehouse", FieldSpec{Name: "id", Type: TypeInt})
	spec.RemovedFields = []string{"amount"}
	_, err := c.Register(spec)
	ce, _ := AsCatalogError(err)
	t.Logf("input: warehouse explicitly removes amount -> verdict: %v conflicts=%v",
		reasonOf(err), conflicts(ce))

	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictFieldRemoved)
	if ce.Conflicts[0].Source != "warehouse" {
		t.Fatalf("removal conflict not attributed: %+v", ce.Conflicts[0])
	}
	got := mustGet(t, c, "orders.table")
	if len(got.Fields) != 2 {
		t.Fatalf("explicit removal took effect: %+v", got.Fields)
	}
}

func TestMultiSourceImplicitRemovalRejected(t *testing.T) {
	c := New()
	// bizdb is the ONLY contributor of amount; dropping it from its
	// own later report is a removal and must be rejected.
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))

	_, err := c.Register(twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))
	ce, _ := AsCatalogError(err)
	t.Logf("input: bizdb drops its only-contributed field -> verdict: %v", reasonOf(err))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictFieldRemoved)

	// But when another source also contributes the field, the
	// dropping source may silently stop contributing it.
	mustRegister(t, c, twoSourceReport("warehouse",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))
	e, err2 := c.Register(twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: bizdb drops amount while warehouse also contributes it -> verdict: err=%v fields=%d",
		err2, len(e.Fields))
	if err2 != nil {
		t.Fatalf("drop of co-contributed field should be accepted: %v", err2)
	}
	if len(e.Fields) != 2 {
		t.Fatalf("co-contributed field lost: %+v", e.Fields)
	}
	if got := e.FieldSources["amount"]; len(got) != 1 || got[0] != "warehouse" {
		t.Fatalf("attribution not updated after drop: %v", got)
	}
}

func TestMultiSourceKindConflict(t *testing.T) {
	// Identity is (name, kind): a same-named report with a different
	// kind is a DIFFERENT logical entity, not a conflict and not a merge.
	// Full coverage lives in TestSameNameDifferentKindCoexist.
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))

	spec := twoSourceReport("kafka", FieldSpec{Name: "id", Type: TypeInt})
	spec.Kind = "topic"
	e, err := c.Register(spec)
	t.Logf("input: same name, kind topic vs table -> verdict: %v new entity id=%s", err, e.ID)
	if err != nil {
		t.Fatalf("same name + different kind must coexist, got: %v", err)
	}
	if e.ID != "orders.topic" {
		t.Fatalf("expected independent id orders.topic, got %s", e.ID)
	}
}

func TestMultiSourceRequiredTightening(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb", FieldSpec{Name: "note", Type: TypeString}))

	_, err := c.Register(twoSourceReport("warehouse",
		FieldSpec{Name: "note", Type: TypeString, Required: true}))
	ce, _ := AsCatalogError(err)
	t.Logf("input: warehouse tightens note to required -> verdict: %v", reasonOf(err))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "note", conflictRequiredAdded)
}

func TestReportsLogPerSource(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))
	mustRegister(t, c, twoSourceReport("warehouse", FieldSpec{Name: "note", Type: TypeString}))
	v2 := twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	)
	mustRegister(t, c, v2)

	all, err := c.Reports("orders.table", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input: 3 accepted reports from 2 sources -> verdict: log=%d entries", len(all))
	if len(all) != 3 {
		t.Fatalf("expected 3 logged reports, got %d", len(all))
	}
	for _, r := range all {
		if r.ReportedAt.IsZero() {
			t.Fatal("report missing timestamp")
		}
		if r.Version < 1 {
			t.Fatal("report missing version")
		}
	}
	biz, _ := c.Reports("orders.table", "bizdb")
	if len(biz) != 2 || biz[1].Version != 3 {
		t.Fatalf("bizdb log wrong: %+v", biz)
	}
	if len(biz[1].Spec.Fields) != 2 {
		t.Fatalf("logged spec does not reflect what bizdb reported: %+v", biz[1].Spec)
	}
	wh, _ := c.Reports("orders.table", "warehouse")
	if len(wh) != 1 {
		t.Fatalf("warehouse log wrong: %+v", wh)
	}
}

// TestConcurrentMultiSourceRegister: many sources concurrently report
// their own distinct field for the same logical entity. Every field
// must survive; the version must equal the number of commits.
func TestConcurrentMultiSourceRegister(t *testing.T) {
	c := New()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := fmt.Sprintf("src%02d", i)
			spec := EntitySpec{
				Name:   "events",
				Source: src,
				Kind:   "table",
				Owners: []string{"owner-" + src},
				Tags:   []string{"tag" + src},
				Fields: []FieldSpec{{Name: fmt.Sprintf("f%02d", i), Type: TypeString}},
			}
			if _, err := c.Register(spec); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent register failed: %v", err)
	}

	e := mustGet(t, c, "events.table")
	t.Logf("input: %d sources concurrently report 1 field each -> verdict: version=%d fields=%d sources=%d",
		n, e.Version, len(e.Fields), len(e.Sources))
	if e.Version != n {
		t.Fatalf("expected version %d, got %d (lost commit)", n, e.Version)
	}
	if len(e.Fields) != n {
		t.Fatalf("expected %d merged fields, got %d (lost update)", n, len(e.Fields))
	}
	if len(e.Sources) != n || len(e.Owners) != n || len(e.Tags) != n {
		t.Fatalf("merged state incomplete: sources=%d owners=%d tags=%d",
			len(e.Sources), len(e.Owners), len(e.Tags))
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%02d", i)
		if got := e.FieldSources[name]; len(got) != 1 {
			t.Fatalf("field %s missing attribution: %v", name, got)
		}
	}
	reps, _ := c.Reports("events.table", "")
	if len(reps) != n {
		t.Fatalf("report log incomplete: %d of %d", len(reps), n)
	}
}
