package catalog

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// whSpec is the warehouse's partial view of the logical entity
// table/orders.
func whSpec() EntitySpec {
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

// bizSpec is the business database's partial view of the same entity.
func bizSpec() EntitySpec {
	return EntitySpec{
		Name:   "orders",
		Source: "bizdb",
		Kind:   "table",
		Owners: []string{"finance"},
		Tags:   []string{"pii"},
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt, Required: true},
			{Name: "note", Type: TypeString},
		},
	}
}

func TestMultiSourceMergeUnionAndProvenance(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())
	e2 := mustRegister(t, c, bizSpec())

	if e1.ID != e2.ID {
		t.Fatalf("same logical entity produced two records: %q vs %q", e1.ID, e2.ID)
	}
	if e1.ID != "table/orders" {
		t.Fatalf("identity rule changed: id=%q", e1.ID)
	}
	if e2.Version != 2 {
		t.Fatalf("expected version 2 after second source, got %d", e2.Version)
	}

	got := mustGet(t, c, e1.ID)
	t.Logf("merged: fields=%v owners=%v tags=%v sources=%v",
		fieldNamesOf(got.Fields), got.Owners, got.Tags, got.Sources)

	wantFields := []string{"amount", "id", "note"} // sorted by name
	if fmt.Sprint(fieldNamesOf(got.Fields)) != fmt.Sprint(wantFields) {
		t.Fatalf("merged fields = %v, want %v", fieldNamesOf(got.Fields), wantFields)
	}
	if fmt.Sprint(got.Owners) != fmt.Sprint([]string{"data-team", "finance"}) {
		t.Fatalf("owners not unioned: %v", got.Owners)
	}
	if fmt.Sprint(got.Tags) != fmt.Sprint([]string{"core", "pii"}) {
		t.Fatalf("tags not unioned: %v", got.Tags)
	}
	if fmt.Sprint(got.Sources) != fmt.Sprint([]string{"bizdb", "warehouse"}) {
		t.Fatalf("sources = %v", got.Sources)
	}

	// Provenance: every field/owner/tag is attributable.
	assertProv := func(m map[string][]string, key string, want ...string) {
		t.Helper()
		if fmt.Sprint(m[key]) != fmt.Sprint(want) {
			t.Fatalf("provenance[%s] = %v, want %v", key, m[key], want)
		}
	}
	assertProv(got.FieldSources, "id", "bizdb", "warehouse")
	assertProv(got.FieldSources, "amount", "warehouse")
	assertProv(got.FieldSources, "note", "bizdb")
	assertProv(got.OwnerSources, "data-team", "warehouse")
	assertProv(got.OwnerSources, "finance", "bizdb")
	assertProv(got.TagSources, "core", "warehouse")
	assertProv(got.TagSources, "pii", "bizdb")

	// Per-source lookup: what did each source report, and when.
	contribs, err := c.Contributions(e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribs) != 2 || contribs[0].Source != "bizdb" || contribs[1].Source != "warehouse" {
		t.Fatalf("contributions = %+v", contribs)
	}
	if len(contribs[0].Spec.Fields) != 2 || contribs[0].Spec.Fields[1].Name != "note" {
		t.Fatalf("bizdb contribution mismatch: %+v", contribs[0].Spec)
	}
	if contribs[0].ReportedAt.IsZero() || contribs[1].ReportedAt.IsZero() {
		t.Fatal("ReportedAt not recorded")
	}

	// History records which source produced each version.
	hist, _ := c.History(e1.ID)
	if len(hist) != 2 || hist[0].Source != "warehouse" || hist[1].Source != "bizdb" {
		t.Fatalf("history sources = %+v", hist)
	}
}

func TestMultiSourceDuplicateReportNoRefresh(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())
	mustRegister(t, c, bizSpec())

	before := mustGet(t, c, e1.ID)
	contribsBefore, _ := c.Contributions(e1.ID)
	time.Sleep(2 * time.Millisecond)

	// Identical re-report from one source: rejected, nothing changes.
	_, err := c.Register(whSpec())
	t.Logf("input: identical re-report from warehouse -> verdict: %v", err)
	if reasonOf(err) != ReasonDuplicateReport {
		t.Fatalf("expected duplicate_report, got %v", err)
	}

	after := mustGet(t, c, e1.ID)
	if after.Version != before.Version || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("duplicate refreshed state: v%d->v%d updatedAt %v->%v",
			before.Version, after.Version, before.UpdatedAt, after.UpdatedAt)
	}
	contribsAfter, _ := c.Contributions(e1.ID)
	for i := range contribsBefore {
		if !contribsAfter[i].ReportedAt.Equal(contribsBefore[i].ReportedAt) {
			t.Fatalf("duplicate refreshed ReportedAt of %s", contribsAfter[i].Source)
		}
	}
	hist, _ := c.History(e1.ID)
	if len(hist) != 2 {
		t.Fatalf("duplicate appended history: %d entries", len(hist))
	}
}

func TestCrossSourceTypeConflict(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())

	bad := bizSpec()
	bad.Fields[0] = FieldSpec{Name: "id", Type: TypeString, Required: true} // id: string vs int
	_, err := c.Register(bad)
	ce, _ := AsCatalogError(err)
	t.Logf("input: bizdb reports id:string vs warehouse id:int -> verdict: %v conflicts=%+v", err, conflicts(ce))

	if reasonOf(err) != ReasonSourceConflict {
		t.Fatalf("expected source_conflict, got %v", err)
	}
	assertConflictErr(t, ce, "id", conflictTypeChanged)
	if ce.Conflicts[0].Source != "bizdb" {
		t.Fatalf("conflict not attributed to reporting source: %+v", ce.Conflicts[0])
	}

	// Whole report rejected: 'note' must not have leaked in.
	got := mustGet(t, c, e1.ID)
	if got.Version != 1 || len(got.Fields) != 2 {
		t.Fatalf("rejected report mutated state: %+v", got)
	}
	if containsString(got.Sources, "bizdb") {
		t.Fatalf("rejected source registered as contributor: %v", got.Sources)
	}
}

func TestExplicitDropRejected(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())
	mustRegister(t, c, bizSpec())

	// bizdb explicitly drops a field contributed by warehouse.
	drop := bizSpec()
	drop.DropFields = []string{"amount"}
	_, err := c.Register(drop)
	ce, _ := AsCatalogError(err)
	t.Logf("input: bizdb drops warehouse's amount -> verdict: %v conflicts=%+v", err, conflicts(ce))
	if reasonOf(err) != ReasonSourceConflict {
		t.Fatalf("expected source_conflict, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictFieldRemoved)

	// A source dropping its own sole-contributed field is rejected too
	// (as incompatible_change: no other source involved).
	dropOwn := whSpec()
	dropOwn.Fields = dropOwn.Fields[:1] // keep only id
	dropOwn.DropFields = []string{"amount"}
	_, err = c.Register(dropOwn)
	t.Logf("input: warehouse drops its own amount -> verdict: %v", err)
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, mustConflictErr(t, err), "amount", conflictFieldRemoved)

	// Dropping a field the entity does not have is a structural error.
	dropUnknown := bizSpec()
	dropUnknown.DropFields = []string{"ghost"}
	_, err = c.Register(dropUnknown)
	t.Logf("input: bizdb drops unknown field -> verdict: %v", err)
	if reasonOf(err) != ReasonInvalidSpec {
		t.Fatalf("expected invalid_spec, got %v", err)
	}

	if got := mustGet(t, c, e1.ID); got.Version != 2 {
		t.Fatalf("rejected drops mutated state: version=%d", got.Version)
	}
}

func TestOmissionIsNotDeletion(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())

	// bizdb never mentions 'amount': it must survive, attributed to
	// warehouse only.
	e2 := mustRegister(t, c, bizSpec())
	got := mustGet(t, c, e1.ID)
	t.Logf("input: bizdb report without 'amount' -> merged fields=%v", fieldNamesOf(got.Fields))
	if !containsString(fieldNamesOf(got.Fields), "amount") {
		t.Fatal("unmentioned field was treated as deleted")
	}
	if fmt.Sprint(got.FieldSources["amount"]) != fmt.Sprint([]string{"warehouse"}) {
		t.Fatalf("amount provenance = %v", got.FieldSources["amount"])
	}
	_ = e2

	// warehouse re-reports without 'note' (bizdb's field): note stays.
	next := whSpec()
	next.Fields = append(next.Fields, FieldSpec{Name: "currency", Type: TypeString})
	mustRegister(t, c, next)
	got = mustGet(t, c, e1.ID)
	if !containsString(fieldNamesOf(got.Fields), "note") {
		t.Fatal("warehouse's partial report deleted bizdb's field")
	}
}

func TestNoOverwriteAcrossSources(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, whSpec())
	mustRegister(t, c, bizSpec())

	// warehouse re-reports only its own view + one new field; bizdb's
	// owners/tags/fields must not be overwritten.
	next := whSpec()
	next.Fields = append(next.Fields, FieldSpec{Name: "currency", Type: TypeString})
	mustRegister(t, c, next)

	got := mustGet(t, c, e1.ID)
	t.Logf("after warehouse re-report: owners=%v tags=%v fields=%v",
		got.Owners, got.Tags, fieldNamesOf(got.Fields))
	if !containsString(got.Owners, "finance") || !containsString(got.Tags, "pii") {
		t.Fatalf("later report overwrote other source's metadata: %+v", got)
	}
	if !containsString(fieldNamesOf(got.Fields), "note") {
		t.Fatal("later report overwrote other source's field")
	}
}

func TestKindSeparatesEntities(t *testing.T) {
	c := New()
	table := mustRegister(t, c, whSpec())

	topic := whSpec()
	topic.Kind = "topic"
	topic.Source = "kafka"
	e2 := mustRegister(t, c, topic)

	t.Logf("input: same name, kind table vs topic -> ids %q vs %q", table.ID, e2.ID)
	if table.ID == e2.ID {
		t.Fatal("same-named entities of different kinds were merged")
	}
	// Both exist independently.
	if got := mustGet(t, c, table.ID); got.Kind != "table" {
		t.Fatalf("original entity clobbered: %+v", got)
	}
}

func TestConcurrentMultiSourceRegister(t *testing.T) {
	c := New()
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := EntitySpec{
				Name:   "events",
				Source: fmt.Sprintf("src%02d", i),
				Kind:   "topic",
				Fields: []FieldSpec{
					{Name: "id", Type: TypeInt, Required: true},
					{Name: fmt.Sprintf("f%02d", i), Type: TypeString},
				},
				Tags: []string{fmt.Sprintf("t%02d", i)},
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

	got := mustGet(t, c, "topic/events")
	t.Logf("input: %d concurrent sources -> version=%d fields=%d sources=%d",
		n, got.Version, len(got.Fields), len(got.Sources))
	if got.Version != n {
		t.Fatalf("expected %d versions, got %d (lost commit)", n, got.Version)
	}
	if len(got.Fields) != n+1 {
		t.Fatalf("expected %d fields, got %d (lost field)", n+1, len(got.Fields))
	}
	if len(got.Sources) != n {
		t.Fatalf("expected %d sources, got %d", n, len(got.Sources))
	}
	for i := 0; i < n; i++ {
		f := fmt.Sprintf("f%02d", i)
		if fmt.Sprint(got.FieldSources[f]) != fmt.Sprint([]string{fmt.Sprintf("src%02d", i)}) {
			t.Fatalf("field %s provenance wrong: %v", f, got.FieldSources[f])
		}
	}
	contribs, _ := c.Contributions(got.ID)
	if len(contribs) != n {
		t.Fatalf("expected %d contributions, got %d", n, len(contribs))
	}
	if !sort.StringsAreSorted([]string{contribs[0].Source, contribs[len(contribs)-1].Source}) {
		t.Fatal("contributions not sorted")
	}
}

func mustConflictErr(t *testing.T, err error) *CatalogError {
	t.Helper()
	ce, ok := AsCatalogError(err)
	if !ok {
		t.Fatalf("expected CatalogError, got %v", err)
	}
	return ce
}
