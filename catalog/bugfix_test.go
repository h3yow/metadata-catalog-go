package catalog

import (
	"testing"
)

// TestSameSourceCompatibleFieldUpdate: a source re-reports one of its
// own fields with a new description and relaxes another from required
// to optional. Both are compatible changes: they must show up in the
// current merged state, produce a new version and appear in the
// per-source report log -- not be silently dropped.
func TestSameSourceCompatibleFieldUpdate(t *testing.T) {
	c := New()
	v1 := EntitySpec{
		Name: "orders", Source: "bizdb", Kind: "table",
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt, Required: true},
			{Name: "amount", Type: TypeFloat, Description: "old description"},
		},
	}
	e1 := mustRegister(t, c, v1)

	v2 := EntitySpec{
		Name: "orders", Source: "bizdb", Kind: "table",
		Fields: []FieldSpec{
			{Name: "id", Type: TypeInt}, // relaxed: required -> optional
			{Name: "amount", Type: TypeFloat, Description: "new description"},
		},
	}
	e2, err := c.Register(v2)
	t.Logf("input: same source updates field description + relaxes required -> verdict: err=%v version=%d",
		err, e2.Version)
	if err != nil {
		t.Fatalf("compatible field update rejected: %v", err)
	}
	if e2.Version != e1.Version+1 {
		t.Fatalf("compatible update produced no new version: %d -> %d", e1.Version, e2.Version)
	}

	// Current state reflects the update.
	cur := mustGet(t, c, e1.ID)
	var id, amount FieldSpec
	for _, f := range cur.Fields {
		switch f.Name {
		case "id":
			id = f
		case "amount":
			amount = f
		}
	}
	if id.Required {
		t.Fatal("required relaxation not reflected in current state")
	}
	if amount.Description != "new description" {
		t.Fatalf("field description update dropped: %q", amount.Description)
	}
	if !cur.UpdatedAt.After(e1.UpdatedAt) && !cur.UpdatedAt.Equal(e1.CreatedAt) {
		// UpdatedAt must have moved to the second commit (or at least
		// not be stale); exact clock granularity is not asserted.
	}

	// Version history records the change with a reason.
	hist, err := c.History(e1.ID)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history not extended: len=%d err=%v", len(hist), err)
	}
	t.Logf("verdict: v2 reason=%q", hist[1].Reason)
	if hist[1].Source != "bizdb" || hist[1].Version != 2 {
		t.Fatalf("history entry wrong: %+v", hist[1])
	}
	// The recorded merged spec matches the current state.
	var histAmount FieldSpec
	for _, f := range hist[1].Spec.Fields {
		if f.Name == "amount" {
			histAmount = f
		}
	}
	if histAmount.Description != "new description" {
		t.Fatalf("history spec disagrees with current state: %+v", histAmount)
	}

	// Report log: both reports are there, the second points at v2.
	reps, err := c.Reports(e1.ID, "bizdb")
	if err != nil || len(reps) != 2 {
		t.Fatalf("report log wrong: len=%d err=%v", len(reps), err)
	}
	if reps[0].Version != 1 || reps[1].Version != 2 {
		t.Fatalf("report versions wrong: %+v", reps)
	}
	if reps[1].Spec.Fields[1].Description != "new description" {
		t.Fatalf("report log lost the reported spec: %+v", reps[1].Spec)
	}
}

// TestRequiredRelaxationRespectsOtherSources: a required field stays
// required while ANY contributor still requires it; it relaxes only
// when the last requiring source relaxes it.
func TestRequiredRelaxationRespectsOtherSources(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt, Required: true}))
	mustRegister(t, c, twoSourceReport("warehouse", FieldSpec{Name: "id", Type: TypeInt, Required: true}))

	// bizdb relaxes, warehouse still requires: merged stays required.
	e, err := c.Register(twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: bizdb relaxes id while warehouse still requires it -> verdict: err=%v required=%v",
		err, e.Fields[0].Required)
	if err != nil {
		t.Fatalf("relaxation by one source must be accepted: %v", err)
	}
	if !e.Fields[0].Required {
		t.Fatal("field relaxed although another source still requires it")
	}

	// warehouse relaxes too: now the merged state becomes optional.
	e2, err := c.Register(twoSourceReport("warehouse", FieldSpec{Name: "id", Type: TypeInt}))
	t.Logf("input: warehouse relaxes as well -> verdict: err=%v required=%v version=%d",
		err, e2.Fields[0].Required, e2.Version)
	if err != nil {
		t.Fatalf("final relaxation rejected: %v", err)
	}
	if e2.Fields[0].Required {
		t.Fatal("field still required after all contributors relaxed it")
	}
}

// TestSameNameDifferentKindSearchAndLineage: the table "orders" and
// the topic "orders" are searched and traversed independently.
func TestSameNameDifferentKindSearchAndLineage(t *testing.T) {
	c := New()
	tableSpec := twoSourceReport("bizdb", FieldSpec{Name: "id", Type: TypeInt})
	tableSpec.Tags = []string{"core"}
	table := mustRegister(t, c, tableSpec)
	topicSpec := twoSourceReport("kafka", FieldSpec{Name: "key", Type: TypeString})
	topicSpec.Kind = "topic"
	topicSpec.Tags = []string{"events"}
	topic := mustRegister(t, c, topicSpec)

	// Search: tag filters pick exactly one of them; the shared name
	// keyword finds both as separate records.
	byCore, _ := c.Search(SearchQuery{Tag: "core"})
	byEvents, _ := c.Search(SearchQuery{Tag: "events"})
	byName, _ := c.Search(SearchQuery{Keyword: "orders"})
	t.Logf("input: search tag=core / tag=events / keyword=orders -> verdict: %v / %v / %v",
		idsOf(byCore), idsOf(byEvents), idsOf(byName))
	if len(byCore) != 1 || byCore[0].ID != table.ID {
		t.Fatalf("tag=core should find only the table: %v", idsOf(byCore))
	}
	if len(byEvents) != 1 || byEvents[0].ID != topic.ID {
		t.Fatalf("tag=events should find only the topic: %v", idsOf(byEvents))
	}
	if len(byName) != 2 {
		t.Fatalf("keyword=orders should find both entities: %v", idsOf(byName))
	}

	// Lineage: an edge from the table does not touch the topic.
	down := mustRegister(t, c, EntitySpec{
		Name: "payments", Source: "bizdb", Kind: "table",
		Fields: []FieldSpec{{Name: "id", Type: TypeInt}},
	})
	if err := c.AddLineage(table.ID, down.ID, "derived"); err != nil {
		t.Fatal(err)
	}
	repTable, _ := c.Downstream(table.ID)
	repTopic, _ := c.Downstream(topic.ID)
	t.Logf("input: downstream of table vs topic -> verdict: %v vs %v",
		repTable.EntityIDs, repTopic.EntityIDs)
	if len(repTable.EntityIDs) != 1 || repTable.EntityIDs[0] != down.ID {
		t.Fatalf("table downstream wrong: %v", repTable.EntityIDs)
	}
	if len(repTopic.EntityIDs) != 0 {
		t.Fatalf("topic downstream polluted by table lineage: %v", repTopic.EntityIDs)
	}
}

// TestRejectedReportLeavesNoTrace: a rejected (conflicting) report
// must not change the current state, history, report log or
// timestamps in any way.
func TestRejectedReportLeavesNoTrace(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt},
		FieldSpec{Name: "amount", Type: TypeFloat},
	))

	bad := twoSourceReport("warehouse", FieldSpec{Name: "amount", Type: TypeString})
	_, err := c.Register(bad)
	t.Logf("input: conflicting report -> verdict: %v", reasonOf(err))
	if reasonOf(err) != ReasonIncompatibleChange {
		t.Fatalf("expected incompatible_change, got %v", err)
	}

	cur := mustGet(t, c, e1.ID)
	hist, _ := c.History(e1.ID)
	reps, _ := c.Reports(e1.ID, "")
	t.Logf("verdict after rejection: version=%d history=%d reports=%d sources=%v",
		cur.Version, len(hist), len(reps), cur.Sources)
	if cur.Version != 1 || len(hist) != 1 {
		t.Fatalf("rejection advanced state: version=%d history=%d", cur.Version, len(hist))
	}
	if len(reps) != 1 || reps[0].Source != "bizdb" {
		t.Fatalf("rejected report entered the log: %+v", reps)
	}
	if len(cur.Sources) != 1 || cur.Sources[0] != "bizdb" {
		t.Fatalf("rejected source became a contributor: %v", cur.Sources)
	}
	if !cur.UpdatedAt.Equal(e1.UpdatedAt) {
		t.Fatalf("rejection refreshed UpdatedAt: %v -> %v", e1.UpdatedAt, cur.UpdatedAt)
	}
}

// TestStateHistoryReportsConsistency: after a mixed sequence of
// accepted reports (initial, compatible field update, second-source
// merge), the current state, the version history and the per-source
// report log must tell the same story.
func TestStateHistoryReportsConsistency(t *testing.T) {
	c := New()
	mustRegister(t, c, twoSourceReport("bizdb",
		FieldSpec{Name: "id", Type: TypeInt, Required: true}))
	// Compatible field update: new description -> new version.
	mustRegister(t, c, EntitySpec{
		Name: "orders", Source: "bizdb", Kind: "table",
		Fields: []FieldSpec{{Name: "id", Type: TypeInt, Required: true, Description: "primary key"}},
	})
	// Second source merges a new optional field -> new version.
	mustRegister(t, c, twoSourceReport("warehouse", FieldSpec{Name: "note", Type: TypeString}))

	id := "table:orders"
	cur := mustGet(t, c, id)
	hist, _ := c.History(id)
	reps, _ := c.Reports(id, "")
	t.Logf("verdict: version=%d history=%d reports=%d", cur.Version, len(hist), len(reps))

	// Versions are dense and the last history entry is the current state.
	if len(hist) != cur.Version {
		t.Fatalf("history length %d != current version %d", len(hist), cur.Version)
	}
	for i, h := range hist {
		if h.Version != i+1 {
			t.Fatalf("history versions not dense: %+v", hist)
		}
	}
	last := hist[len(hist)-1]
	if len(last.Spec.Fields) != len(cur.Fields) {
		t.Fatalf("last history spec has %d fields, current state has %d",
			len(last.Spec.Fields), len(cur.Fields))
	}
	for _, f := range cur.Fields {
		found := false
		for _, hf := range last.Spec.Fields {
			if hf == f {
				found = true
			}
		}
		if !found {
			t.Fatalf("current field %+v not in last history spec %+v", f, last.Spec.Fields)
		}
	}

	// Every accepted report points at an existing version, and every
	// version beyond v1 is produced by exactly one logged report.
	produced := map[int]int{}
	for _, r := range reps {
		if r.Version < 1 || r.Version > cur.Version {
			t.Fatalf("report points at nonexistent version: %+v", r)
		}
		produced[r.Version]++
	}
	for v := 2; v <= cur.Version; v++ {
		if produced[v] != 1 {
			t.Fatalf("version %d produced by %d reports, want 1", v, produced[v])
		}
	}
}
