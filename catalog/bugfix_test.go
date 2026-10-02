package catalog

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain prints a coverage banner naming the business areas this
// package's tests exercise, so `go test -v` output shows at a glance
// which scenarios were run and each case's verdict (every case logs
// its input and verdict via t.Logf).
func TestMain(m *testing.M) {
	fmt.Println("=== metadata-catalog catalog package business coverage ===")
	fmt.Println("  [bugfix] same-source compatible field updates (description / required relax)")
	fmt.Println("  [bugfix] same-name different-kind entities coexist independently")
	fmt.Println("  [bugfix] current-state / version-history / report-log self-consistency")
	fmt.Println("  [existing] identical duplicate reports: no pile-up, no refresh")
	fmt.Println("  [existing] cross-source conflicts: type / required tightening / explicit removal")
	fmt.Println("  [existing] omission is not deletion; co-contributed drop is fine")
	fmt.Println("  [existing] optimistic CAS version conflicts and concurrent no-loss commits")
	fmt.Println("  [existing] lineage cycle/multi-path traversal, search freshness, validation")
	fmt.Println("========================================================")
	os.Exit(m.Run())
}

// fieldByName finds a field in an entity spec, failing the test when
// the field is absent.
func fieldByName(t *testing.T, e *Entity, name string) FieldSpec {
	t.Helper()
	for _, f := range e.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q missing from entity %s (fields=%v)", name, e.ID, fieldNames(specFromEntity(*e)))
	return FieldSpec{}
}

// TestSameSourceFieldDescriptionUpdate covers the first reported bug:
// a source re-reports a field it already owns with a changed
// description. The new description must land in the current merged
// state and produce a version record, instead of being visible only
// in that source's report log.
func TestSameSourceFieldDescriptionUpdate(t *testing.T) {
	c := New()
	initial := EntitySpec{
		Name:   "orders",
		Source: "bizdb",
		Kind:   "table",
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt, Required: true},
			{Name: "amount", Type: TypeFloat, Description: "old amount doc"},
		},
	}
	e1 := mustRegister(t, c, initial)
	t.Logf("input: bizdb v1 registers amount with description %q -> verdict: accepted version=%d",
		"old amount doc", e1.Version)

	updated := initial
	updated.Fields[1].Description = "new amount doc after clarification"
	e2, err := c.Register(updated)
	t.Logf("input: bizdb re-reports amount with description %q -> verdict: err=%v version=%d",
		updated.Fields[1].Description, err, e2.Version)
	if err != nil {
		t.Fatalf("compatible description update rejected: %v", err)
	}
	if e2.Version != 2 {
		t.Fatalf("description update must create a version, got version=%d", e2.Version)
	}

	// 1) Current merged state carries the new value.
	got := fieldByName(t, e2, "amount")
	if got.Description != "new amount doc after clarification" {
		t.Fatalf("current state kept stale description %q", got.Description)
	}
	// Re-read through Get to make sure it is not just the return value.
	reread := mustGet(t, c, e2.ID)
	if fieldByName(t, reread, "amount").Description != "new amount doc after clarification" {
		t.Fatalf("Get() returned stale field description")
	}
	t.Logf("verdict: Get(%s) current amount.description=%q", e2.ID, got.Description)

	// 2) Version history records the change with the new merged spec.
	hist, _ := c.History(e2.ID)
	if len(hist) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(hist))
	}
	last := hist[1]
	if last.Source != "bizdb" || last.Version != 2 {
		t.Fatalf("version 2 not attributed to bizdb: %+v", last)
	}
	if desc := fieldOfSpec(t, last.Spec, "amount").Description; desc != "new amount doc after clarification" {
		t.Fatalf("history v2 stored stale description %q", desc)
	}
	t.Logf("verdict: history v2 source=%s reason=%q amount.description=%q",
		last.Source, last.Reason, fieldOfSpec(t, last.Spec, "amount").Description)

	// 3) The per-source report log matches the version exactly.
	reps, _ := c.Reports(e2.ID, "bizdb")
	if len(reps) != 2 {
		t.Fatalf("expected 2 bizdb reports, got %d", len(reps))
	}
	if reps[1].Version != 2 || reps[1].Spec.Fields[1].Description != "new amount doc after clarification" {
		t.Fatalf("report log inconsistent: %+v", reps[1])
	}
	t.Logf("verdict: report log v%d carries %q — history/current/log agree",
		reps[1].Version, reps[1].Spec.Fields[1].Description)

	// 4) Re-reporting the identical updated content is a duplicate: no
	//    v3, no timestamp refresh, no extra log entry.
	before := mustGet(t, c, e2.ID)
	_, err = c.Register(updated)
	t.Logf("input: identical re-report of updated content -> verdict: %v", err)
	if reasonOf(err) != ReasonDuplicateReport {
		t.Fatalf("expected duplicate_report, got %v", err)
	}
	after := mustGet(t, c, e2.ID)
	if after.Version != 2 || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("duplicate changed state: version=%d updatedAt %v->%v",
			after.Version, before.UpdatedAt, after.UpdatedAt)
	}
	if reps2, _ := c.Reports(e2.ID, "bizdb"); len(reps2) != 2 {
		t.Fatalf("duplicate piled up in report log: %d", len(reps2))
	}
}

func fieldOfSpec(t *testing.T, s EntitySpec, name string) FieldSpec {
	t.Helper()
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q not in spec %+v", name, s.Fields)
	return FieldSpec{}
}

// TestSameSourceRequiredRelaxation: required -> optional on a field
// the same source previously declared required is a compatible
// relaxation and must be reflected in the merged state with a version.
// The reverse direction (optional -> required tightening) stays
// rejected.
func TestSameSourceRequiredRelaxation(t *testing.T) {
	c := New()
	initial := EntitySpec{
		Name: "orders", Source: "bizdb", Kind: "table",
		Fields: []FieldSpec{{Name: "amount", Type: TypeFloat, Required: true}},
	}
	mustRegister(t, c, initial)

	relaxed := initial
	relaxed.Fields[0].Required = false
	e, err := c.Register(relaxed)
	t.Logf("input: bizdb relaxes amount required true->false -> verdict: err=%v required=%v version=%d",
		err, fieldByName(t, e, "amount").Required, e.Version)
	if err != nil {
		t.Fatalf("relaxation rejected: %v", err)
	}
	if fieldByName(t, e, "amount").Required {
		t.Fatal("merged state still marks amount required after relaxation")
	}
	if e.Version != 2 {
		t.Fatalf("relaxation must create a version, got %d", e.Version)
	}
	hist, _ := c.History(e.ID)
	if !containsString([]string{hist[1].Reason}, "fields relaxed to optional: amount") &&
		hist[1].Reason != "fields relaxed to optional: amount" {
		t.Fatalf("version reason does not mention relaxation: %q", hist[1].Reason)
	}

	// Re-tightening remains a hard conflict.
	again := relaxed
	again.Fields[0].Required = true
	_, err = c.Register(again)
	ce, _ := AsCatalogError(err)
	t.Logf("input: bizdb re-tightens amount to required -> verdict: reason=%s conflicts=%v",
		reasonOf(err), conflicts(ce))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("tightening must be rejected, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictRequiredAdded)

	// Rejected report leaves no trace: version/history/log untouched.
	final := mustGet(t, c, e.ID)
	if final.Version != 2 || fieldByName(t, final, "amount").Required {
		t.Fatalf("rejected tightening mutated state: version=%d", final.Version)
	}
	if hist2, _ := c.History(e.ID); len(hist2) != 2 {
		t.Fatalf("rejected report appended history: %d", len(hist2))
	}
	if reps, _ := c.Reports(e.ID, "bizdb"); len(reps) != 2 {
		t.Fatalf("rejected report appended to log: %d", len(reps))
	}
	t.Logf("verdict: after rejection state still v2 optional, history/log length 2 — no trace left")
}

// TestMultiSourceRequiredRelaxationAcrossContributors: while ANY
// contributor still attests required, the merged field stays
// required; it relaxes only when the last required attestation is
// withdrawn. Re-attesting required afterwards is a rejected
// tightening (relaxation never reverses through a single source).
func TestMultiSourceRequiredRelaxationAcrossContributors(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true}))
	mustRegister(t, c, twoSourceReport("warehouse",
		FieldSpec{Name: "id", Type: TypeInt, Required: true}))

	// bizdb relaxes, warehouse still requires => merged stays required.
	relaxed := twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt})
	e, err := c.Register(relaxed)
	t.Logf("input: bizdb relaxes id while warehouse still requires -> verdict: err=%v required=%v",
		err, fieldByName(t, e, "id").Required)
	if err != nil || !fieldByName(t, e, "id").Required {
		t.Fatalf("merged required must hold while another source requires it")
	}

	// warehouse relaxes too => last required attestation gone.
	e, err = c.Register(twoSourceReport("warehouse", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: warehouse relaxes id as well -> verdict: err=%v required=%v version=%d",
		err, fieldByName(t, e, "id").Required, e.Version)
	if err != nil || fieldByName(t, e, "id").Required {
		t.Fatalf("expected merged id to become optional")
	}

	// Re-tightening from one source is rejected even though it alone
	// once attested required.
	_, err = c.Register(twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true}))
	ce, _ := AsCatalogError(err)
	t.Logf("input: bizdb re-tightens id after global relaxation -> verdict: %v", reasonOf(err))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "id", conflictRequiredAdded)
}

// TestSameNameDifferentKindCoexist covers the second reported bug:
// a table "orders" and a message topic also named "orders" are
// independent logical entities. Each gets its own record, version
// history, report log, search hits and lineage. Same-name same-kind
// reports still merge into one record.
func TestSameNameDifferentKindCoexist(t *testing.T) {
	c := New()

	table := twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true},
		FieldSpec{Name: "amount", Type: TypeFloat})
	topic := EntitySpec{
		Name: "orders", Source: "kafka", Kind: "topic",
		Fields: []FieldSpec{
			{Name: "id", Type: TypeString},
			{Name: "payload", Type: TypeJSON},
		},
	}
	eTable := mustRegister(t, c, table)
	eTopic, err := c.Register(topic)
	t.Logf("input: table orders{%s} then topic orders{%s} -> verdict: err=%v ids=%s,%s",
		"id:int,amount:float", "id:string,payload:json", err, eTable.ID, eTopic.ID)
	if err != nil {
		t.Fatalf("same-name different-kind report must be accepted: %v", err)
	}
	if eTable.ID == eTopic.ID || eTopic.ID != "orders.topic" {
		t.Fatalf("expected independent ids orders.table/orders.topic, got %s/%s", eTable.ID, eTopic.ID)
	}

	// Independent current states: neither field set leaks across.
	tbl := mustGet(t, c, "orders.table")
	top := mustGet(t, c, "orders.topic")
	if len(tbl.Fields) != 2 || len(top.Fields) != 2 {
		t.Fatalf("field sets bled across: table=%v topic=%v",
			fieldNames(specFromEntity(*tbl)), fieldNames(specFromEntity(*top)))
	}
	if fieldByName(t, top, "id").Type != TypeString ||
		fieldByName(t, tbl, "id").Type != TypeInt {
		t.Fatal("same-named field types bled across entities")
	}
	if tbl.Sources[0] != "bizdb" || top.Sources[0] != "kafka" {
		t.Fatalf("sources bled across: %v / %v", tbl.Sources, top.Sources)
	}

	// Independent histories and report logs.
	hTbl, _ := c.History("orders.table")
	hTop, _ := c.History("orders.topic")
	if len(hTbl) != 1 || len(hTop) != 1 || hTop[0].Source != "kafka" {
		t.Fatalf("histories not independent: %d / %d", len(hTbl), len(hTop))
	}
	if reps, _ := c.Reports("orders.topic", "bizdb"); len(reps) != 0 {
		t.Fatalf("bizdb report leaked into topic log: %d", len(reps))
	}

	// Same-name same-kind still merges (not split).
	merge := twoSourceReport("warehouse", FieldSpec{Name: "note", Type: TypeString})
	merged, err := c.Register(merge)
	t.Logf("input: warehouse reports table orders with note -> verdict: err=%v id=%s sources=%v fields=%d",
		err, merged.ID, merged.Sources, len(merged.Fields))
	if err != nil || merged.ID != "orders.table" || len(merged.Sources) != 2 || len(merged.Fields) != 3 {
		t.Fatalf("same-name same-kind must merge: id=%s sources=%v fields=%d",
			merged.ID, merged.Sources, len(merged.Fields))
	}
	topAfter := mustGet(t, c, "orders.topic")
	if len(topAfter.Fields) != 2 || len(topAfter.Sources) != 1 {
		t.Fatal("table merge bled into the topic entity")
	}

	// Search is independent: keyword hits BOTH entities; kind-scoped
	// keyword would find only one (clients scope via tag/owner).
	hits, _ := c.Search(SearchQuery{Keyword: "orders"})
	t.Logf("input: search keyword=orders -> verdict: %v", idsOf(hits))
	if len(hits) != 2 {
		t.Fatalf("expected both entities in search, got %v", idsOf(hits))
	}
	if hits, _ := c.Search(SearchQuery{Keyword: "payload"}); len(hits) != 1 || hits[0].ID != "orders.topic" {
		t.Fatalf("topic-only field search wrong: %v", idsOf(hits))
	}
	if hits, _ := c.Search(SearchQuery{Keyword: "amount"}); len(hits) != 1 || hits[0].ID != "orders.table" {
		t.Fatalf("table-only field search wrong: %v", idsOf(hits))
	}

	// Lineage is independent: edge on the table never reaches the
	// topic and vice versa.
	consumer := EntitySpec{Name: "billing", Source: "billing-svc", Kind: "service",
		Fields: []FieldSpec{{Name: "id", Type: TypeInt}}}
	eCons := mustRegister(t, c, consumer)
	if err := c.AddLineage("orders.table", eCons.ID, "read"); err != nil {
		t.Fatal(err)
	}
	down, _ := c.Downstream("orders.table")
	if len(down.EntityIDs) != 1 || down.EntityIDs[0] != eCons.ID {
		t.Fatalf("table lineage wrong: %v", down.EntityIDs)
	}
	downTopic, _ := c.Downstream("orders.topic")
	if len(downTopic.EntityIDs) != 0 {
		t.Fatalf("table lineage leaked to topic: %v", downTopic.EntityIDs)
	}
	t.Logf("verdict: table downstream=%v topic downstream=%v — lineage isolated",
		down.EntityIDs, downTopic.EntityIDs)
}

// TestCompatibleUpdateAlongsideIncompatibleFieldRejectsWholly:
// a report that carries a genuine compatible adjustment (new field
// description) AND a breaking change (type clash) in the same submit
// must be rejected wholesale — neither the description nor anything
// else from the report may take effect, and no report-log entry may
// appear.
func TestCompatibleUpdateAlongsideIncompatibleFieldRejectsWholly(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat, Description: "old doc"},
	))

	bad := twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeString, Description: "shiny new doc"},
	)
	_, err := c.Register(bad)
	ce, _ := AsCatalogError(err)
	t.Logf("input: same report updates amount doc AND changes its type -> verdict: reason=%s conflicts=%v",
		reasonOf(err), conflicts(ce))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}
	assertConflictErr(t, ce, "amount", conflictTypeChanged)
	if ce.Conflicts[0].Source != "bizdb" {
		t.Fatalf("conflict must name the source, got %+v", ce.Conflicts)
	}

	e := mustGet(t, c, "orders.table")
	f := fieldByName(t, e, "amount")
	if f.Type != TypeFloat || f.Description != "old doc" {
		t.Fatalf("rejected report partially applied: %+v", f)
	}
	if e.Version != 1 {
		t.Fatalf("rejected report bumped version: %d", e.Version)
	}
	if h, _ := c.History("orders.table"); len(h) != 1 {
		t.Fatalf("rejected report appended history: %d", len(h))
	}
	if reps, _ := c.Reports("orders.table", ""); len(reps) != 1 {
		t.Fatalf("rejected report left a log entry: %d", len(reps))
	}
	t.Logf("verdict: type still float, description still %q, version/history/log untouched — total rejection",
		"old doc")
}

// TestOmissionOfFieldNeverDeletesEvenWithCompatibleUpdates: when a
// source updates one field and simply does not mention a field
// previously contributed by ANOTHER source, the untouched field must
// survive and the update still commits.
func TestOmissionOfFieldNeverDeletesEvenWithCompatibleUpdates(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))
	mustRegister(t, c, twoSourceReport("warehouse",
		FieldSpec{Name: "note", Type: TypeString, Description: "v1 note"}))

	// warehouse updates note's description and does not mention
	// amount (bizdb's field): amount stays.
	next := twoSourceReport("warehouse",
		FieldSpec{Name: "note", Type: TypeString, Description: "v2 note"})
	e, err := c.Register(next)
	t.Logf("input: warehouse updates note doc, omits amount -> verdict: err=%v fields=%v",
		err, fieldNames(specFromEntity(*e)))
	if err != nil {
		t.Fatalf("omission of another source's field rejected: %v", err)
	}
	if fieldByName(t, e, "amount").Type != TypeFloat {
		t.Fatal("omitted other-source field was deleted")
	}
	if fieldByName(t, e, "note").Description != "v2 note" {
		t.Fatal("compatible description update did not land")
	}
}

// TestTripleConsistencyAfterCompatibleUpdate is an end-to-end
// consistency check across several compatible updates: after every
// accepted report the current state, the latest version snapshot and
// the report-log entry must describe the same thing; history versions
// are dense and every log entry points at the version it produced.
func TestTripleConsistencyAfterCompatibleUpdate(t *testing.T) {
	c := New()
	v1 := EntitySpec{
		Name: "orders", Source: "bizdb", Kind: "table", Owners: []string{"alice"},
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt, Required: true},
			{Name: "amount", Type: TypeFloat, Required: true, Description: "d1"},
		},
	}
	mustRegister(t, c, v1)

	steps := []func(EntitySpec) EntitySpec{
		func(s EntitySpec) EntitySpec {
			s.Fields[1].Description = "d2"
			return s
		},
		func(s EntitySpec) EntitySpec {
			s.Fields[1].Description = "d3"
			s.Fields[1].Required = false
			return s
		},
		func(s EntitySpec) EntitySpec {
			s.Fields[1].Description = "d4"
			s.Fields = append(s.Fields, FieldSpec{Name: "note", Type: TypeString})
			return s
		},
	}
	cur := v1
	for i, mutate := range steps {
		cur = mutate(cloneSpec(cur))
		if _, err := c.Register(cur); err != nil {
			t.Fatalf("step %d rejected: %v", i, err)
		}
	}

	id := "orders.table"
	current := mustGet(t, c, id)
	hist, _ := c.History(id)
	reps, _ := c.Reports(id, "bizdb")

	// Dense versions 1..4 and one log entry per accepted report.
	if current.Version != 4 {
		t.Fatalf("expected version 4, got %d", current.Version)
	}
	if len(hist) != 4 || len(reps) != 4 {
		t.Fatalf("history=%d reports=%d, want 4/4", len(hist), len(reps))
	}
	// The latest history snapshot equals the current state.
	head := hist[len(hist)-1]
	if head.Version != current.Version || head.Spec.Description != specFromEntity(*current).Description {
		t.Fatalf("history head does not match current state")
	}
	headFields := map[string]FieldSpec{}
	for _, f := range head.Spec.Fields {
		headFields[f.Name] = f
	}
	for _, f := range current.Fields {
		if hf := headFields[f.Name]; hf != f {
			t.Fatalf("field %s differs between current and history head: %+v vs %+v", f.Name, f, hf)
		}
	}
	// Each log entry points at the version it produced and that
	// version snapshot contains exactly what the source reported.
	for i, r := range reps {
		if r.Version != i+1 {
			t.Fatalf("report %d points at version %d, want %d", i, r.Version, i+1)
		}
		v := hist[r.Version-1]
		if v.Source != "bizdb" {
			t.Fatalf("version %d source mismatch", r.Version)
		}
		// The source's own reported fields must all be present in that
		// version's merged snapshot with identical specs.
		vFields := map[string]FieldSpec{}
		for _, f := range v.Spec.Fields {
			vFields[f.Name] = f
		}
		for _, rf := range r.Spec.Fields {
			if vf := vFields[rf.Name]; vf != rf {
				t.Fatalf("v%d: logged report field %+v not reflected in version snapshot %+v",
					r.Version, rf, vf)
			}
		}
	}
	t.Logf("input: 4 sequential reports (1 create + desc/relax/add) -> verdict: current=v%d history=%d reports=%d, all mutually consistent",
		current.Version, len(hist), len(reps))
}

// TestKindValidationRejectsUnsafeKind keeps the (name, kind) ID
// encoding unambiguous and HTTP-path-safe: kind must be present and
// must contain neither '.' nor '/'.
func TestKindValidationRejectsUnsafeKind(t *testing.T) {
	c := New()
	for _, kind := range []string{"", "mes.sage", "mes/sage"} {
		spec := twoSourceReport("kafka", FieldSpec{Name: "id", Type: TypeString})
		spec.Kind = kind
		_, err := c.Register(spec)
		t.Logf("input: kind=%q -> verdict: %v", kind, reasonOf(err))
		if reasonOf(err) != ReasonInvalidSpec {
			t.Fatalf("kind %q must be invalid_spec, got %v", kind, err)
		}
	}
}

// TestDropCoContributedRequiredFieldRelaxesMerged: when the only
// source that required a field stops contributing it while another
// (optional) source still does, the field survives and the merged
// required flag relaxes in the same version.
func TestDropCoContributedRequiredFieldRelaxesMerged(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true}))
	mustRegister(t, c, twoSourceReport("warehouse",
		FieldSpec{Name: "id", Type: TypeInt}))

	// bizdb re-reports without id (warehouse keeps it).
	e, err := c.Register(twoSourceReport("bizdb",
		FieldSpec{Name: "ref", Type: TypeString}))
	t.Logf("input: bizdb drops required id while warehouse contributes it optional -> verdict: err=%v", err)
	if err != nil {
		t.Fatalf("co-contributed drop must be accepted: %v", err)
	}
	id := fieldByName(t, e, "id")
	if id.Required {
		t.Fatal("merged id must relax to optional after sole required source dropped it")
	}
	attr := e.FieldSources["id"]
	if len(attr) != 1 || attr[0] != "warehouse" {
		t.Fatalf("attribution not updated: %v", attr)
	}
	hist, _ := c.History("orders.table")
	if !strings.Contains(hist[len(hist)-1].Reason, "fields relaxed to optional: id") {
		t.Fatalf("version reason %q does not record relaxation", hist[len(hist)-1].Reason)
	}
	t.Logf("verdict: id survives as optional, attribution=%v, reason=%q",
		attr, hist[len(hist)-1].Reason)
}
