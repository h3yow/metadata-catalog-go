package catalog

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func mustRegister(t *testing.T, c *Catalog, spec EntitySpec) *Entity {
	t.Helper()
	e, err := c.Register(spec)
	if err != nil {
		t.Fatalf("Register(%s/%s) failed: %v", spec.Source, spec.Name, err)
	}
	return e
}

func reasonOf(err error) Reason {
	if ce, ok := AsCatalogError(err); ok {
		return ce.Reason
	}
	return ""
}

func TestRegisterAndDuplicateReport(t *testing.T) {
	c := New()
	spec := baseSpec()

	e1 := mustRegister(t, c, spec)
	t.Logf("input: initial register %s -> verdict: %s version=%d", e1.ID, "accepted", e1.Version)
	if e1.Version != 1 {
		t.Fatalf("expected version 1, got %d", e1.Version)
	}

	// Identical repeat must be recognised, not stored again.
	_, err := c.Register(baseSpec())
	t.Logf("input: identical repeat -> verdict: %v", err)
	if reasonOf(err) != ReasonDuplicateReport {
		t.Fatalf("expected duplicate_report, got %v", err)
	}

	// Reordered fields/owners with same content is still a duplicate.
	reordered := baseSpec()
	reordered.Fields = []FieldSpec{reordered.Fields[1], reordered.Fields[0]}
	_, err = c.Register(reordered)
	t.Logf("input: reordered identical fields -> verdict: %v", err)
	if reasonOf(err) != ReasonDuplicateReport {
		t.Fatalf("expected duplicate_report on reorder, got %v", err)
	}

	got := mustGet(t, c, e1.ID)
	if got.Version != 1 {
		t.Fatalf("duplicate report rewrote version: %d", got.Version)
	}
	hist, _ := c.History(e1.ID)
	if len(hist) != 1 {
		t.Fatalf("duplicate report rewrote history: %d entries", len(hist))
	}
}

func TestCompatibleEvolution(t *testing.T) {
	c := New()
	e1 := mustRegister(t, c, baseSpec())

	next := baseSpec()
	next.Fields = append(next.Fields, FieldSpec{Name: "note", Type: TypeString})
	next.Description = "added note"

	e2, err := c.Register(next)
	t.Logf("input: add optional field 'note' to v%d -> verdict: %v new version=%d reason=%q",
		e1.Version, err, e2.Version, versionReason(t, c, e1.ID))
	if err != nil || e2.Version != 2 {
		t.Fatalf("compatible evolution rejected: %v", err)
	}

	hist, _ := c.History(e1.ID)
	if len(hist) != 2 || hist[0].Version != 1 || hist[1].Version != 2 {
		t.Fatalf("history mismatch: %+v", hist)
	}
	if !e2.CreatedAt.Equal(e1.CreatedAt) {
		t.Fatal("createdAt must be stable across versions")
	}
}

func TestIncompatibleEvolution(t *testing.T) {
	c := New()
	e := mustRegister(t, c, baseSpec())

	cases := []struct {
		name       string
		mutate     func(s *EntitySpec)
		wantReason Reason
		wantField  string
		wantKind   string
	}{
		{
			name:       "remove field",
			mutate:     func(s *EntitySpec) { s.Fields = s.Fields[:1] },
			wantReason: ReasonIncompatibleChange, wantField: "amount", wantKind: conflictFieldRemoved,
		},
		{
			name:       "change field type",
			mutate:     func(s *EntitySpec) { s.Fields[1].Type = TypeString },
			wantReason: ReasonIncompatibleChange, wantField: "amount", wantKind: conflictTypeChanged,
		},
		{
			name: "add required field",
			mutate: func(s *EntitySpec) {
				s.Fields = append(s.Fields, FieldSpec{Name: "tenant", Type: TypeString, Required: true})
			},
			wantReason: ReasonIncompatibleChange, wantField: "tenant", wantKind: conflictRequiredAdded,
		},
		{
			name:       "change entity kind",
			mutate:     func(s *EntitySpec) { s.Kind = "view" },
			wantReason: ReasonIncompatibleChange, wantField: "kind", wantKind: conflictSemanticChanged,
		},
	}
	for _, tc := range cases {
		spec := baseSpec()
		tc.mutate(&spec)
		_, err := c.Register(spec)
		ce, ok := AsCatalogError(err)
		t.Logf("input: %s -> verdict: reason=%s conflicts=%v", tc.name, reasonOf(err), conflicts(ce))
		if !ok || ce.Reason != tc.wantReason {
			t.Fatalf("%s: expected %s, got %v", tc.name, tc.wantReason, err)
		}
		assertConflictErr(t, ce, tc.wantField, tc.wantKind)
	}

	// Version must not have advanced after rejections.
	got := mustGet(t, c, e.ID)
	if got.Version != 1 {
		t.Fatalf("rejected evolution changed version to %d", got.Version)
	}
}

func TestLineageDuplicateAndSelfEdge(t *testing.T) {
	c := New()
	up := mustRegister(t, c, baseSpec())
	downSpec := baseSpec()
	downSpec.Name = "payments"
	down := mustRegister(t, c, downSpec)

	if err := c.AddLineage(up.ID, down.ID, "derived"); err != nil {
		t.Fatalf("first AddLineage: %v", err)
	}
	err := c.AddLineage(up.ID, down.ID, "derived")
	t.Logf("input: repeat edge %s -> %s -> verdict: %v", up.ID, down.ID, err)
	if reasonOf(err) != ReasonDuplicateEdge {
		t.Fatalf("expected duplicate_edge, got %v", err)
	}

	err = c.AddLineage(up.ID, up.ID, "loop")
	t.Logf("input: self edge -> verdict: %v", err)
	if reasonOf(err) != ReasonSelfEdge {
		t.Fatalf("expected self_edge, got %v", err)
	}
}

// buildDiamond builds: root -> a, root -> b, a -> leaf, b -> leaf,
// leaf -> root (cycle back to the root).
func buildDiamond(t *testing.T) (*Catalog, string, string, string, string) {
	t.Helper()
	c := New()
	ids := make([]string, 4)
	for i, name := range []string{"root", "a", "b", "leaf"} {
		spec := baseSpec()
		spec.Name = name
		spec.Source = "g"
		ids[i] = mustRegister(t, c, spec).ID
	}
	root, a, b, leaf := ids[0], ids[1], ids[2], ids[3]
	for _, pair := range [][2]string{{root, a}, {root, b}, {a, leaf}, {b, leaf}, {leaf, root}} {
		if err := c.AddLineage(pair[0], pair[1], "derived"); err != nil {
			t.Fatalf("edge %s->%s: %v", pair[0], pair[1], err)
		}
	}
	return c, root, a, b, leaf
}

func TestImpactAnalysisCycleAndMultiPath(t *testing.T) {
	c, root, a, b, leaf := buildDiamond(t)

	rep, err := c.Downstream(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input: Downstream(%s) on diamond + cycle -> verdict: entities=%v edgeCount=%d",
		root, rep.EntityIDs, len(rep.Edges))

	if len(rep.EntityIDs) != 3 {
		t.Fatalf("expected 3 unique affected entities (a,b,leaf), got %v", rep.EntityIDs)
	}
	want := []string{a, b, leaf}
	got := append([]string(nil), rep.EntityIDs...)
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("affected set mismatch: got %v want %v", got, want)
		}
	}
	// Each directed edge reported at most once.
	seen := map[string]bool{}
	for _, e := range rep.Edges {
		k := e.UpstreamID + "->" + e.DownstreamID
		if seen[k] {
			t.Fatalf("edge %s reported twice", k)
		}
		seen[k] = true
	}

	// Deterministic / reproducible: rerun gives the same order.
	rep2, _ := c.Downstream(root)
	if fmt.Sprint(rep2.EntityIDs) != fmt.Sprint(rep.EntityIDs) {
		t.Fatalf("impact order not reproducible: %v vs %v", rep2.EntityIDs, rep.EntityIDs)
	}

	// Upstream of root must terminate despite the cycle and include
	// every producer reachable through the diamond.
	up, err := c.Upstream(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input: Upstream(%s) on cycle -> verdict: entities=%v", root, up.EntityIDs)
	if len(up.EntityIDs) != 3 {
		t.Fatalf("expected a,b,leaf upstream, got %v", up.EntityIDs)
	}
}

func TestImpactStableAcrossRuns(t *testing.T) {
	// Run the same analysis repeatedly: map iteration must never leak
	// into the report order.
	c, root, _, _, _ := buildDiamond(t)
	first, _ := c.Downstream(root)
	for i := 0; i < 50; i++ {
		r, _ := c.Downstream(root)
		if fmt.Sprint(r.EntityIDs) != fmt.Sprint(first.EntityIDs) {
			t.Fatalf("run %d reordered: %v", i, r.EntityIDs)
		}
	}
}

func TestSearchReflectsLatestState(t *testing.T) {
	c := New()
	spec := baseSpec()
	spec.Tags = []string{"core"}
	spec.Owners = []string{"alice"}
	mustRegister(t, c, spec)

	// Search by keyword on a field name.
	res, _ := c.Search(SearchQuery{Keyword: "amount"})
	t.Logf("input: keyword='amount' -> verdict: %d hit(s)", len(res))
	if len(res) != 1 || res[0].ID != entityID(spec.Name) {
		t.Fatalf("expected 1 hit, got %v", res)
	}

	// Tag removed in v2: the tag search must return nothing.
	next := baseSpec()
	next.Tags = []string{}
	next.Owners = []string{"alice"}
	next.Fields = append(next.Fields, FieldSpec{Name: "note", Type: TypeString})
	if _, err := c.Register(next); err != nil {
		t.Fatal(err)
	}
	res, _ = c.Search(SearchQuery{Tag: "core"})
	t.Logf("input: tag='core' after tag removal -> verdict: %d hit(s)", len(res))
	if len(res) != 0 {
		t.Fatalf("expected stale tag to disappear, got %v", idsOf(res))
	}

	res, _ = c.Search(SearchQuery{Owner: "alice", Keyword: "orders"})
	t.Logf("input: owner='alice' keyword='orders' -> verdict: %d hit(s)", len(res))
	if len(res) != 1 {
		t.Fatalf("expected 1 combined hit, got %d", len(res))
	}
}

// TestConcurrentNoLostUpdate: many goroutines each add their own
// optional field using optimistic CAS with retry. Every field must end
// up present; the number of versions must equal commits + 1.
func TestConcurrentNoLostUpdate(t *testing.T) {
	c := New()
	mustRegister(t, c, baseSpec())
	id := entityID(baseSpec().Name)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			field := fmt.Sprintf("f%02d", i)
			var attempts int
			for {
				attempts++
				if attempts > 100 {
					errs <- fmt.Errorf("worker %d: too many conflicts", i)
					return
				}
				cur, err := c.Get(id)
				if err != nil {
					errs <- err
					return
				}
				spec := baseSpec()
				spec.Description = cur.Description
				spec.Owners = append([]string(nil), cur.Owners...)
				spec.Tags = append([]string(nil), cur.Tags...)
				spec.Fields = append(append([]FieldSpec(nil), cur.Fields...),
					FieldSpec{Name: field, Type: TypeString})
				_, err = c.RegisterAt(spec, cur.Version)
				if err == nil {
					return
				}
				if reasonOf(err) != ReasonVersionConflict {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent worker error: %v", err)
	}

	final := mustGet(t, c, id)
	t.Logf("input: %d concurrent field additions -> verdict: final version=%d fieldCount=%d",
		n, final.Version, len(final.Fields))
	if final.Version != n+1 {
		t.Fatalf("expected version %d, got %d (lost update)", n+1, final.Version)
	}
	if len(final.Fields) != 2+n {
		t.Fatalf("expected %d fields, got %d (lost field)", 2+n, len(final.Fields))
	}
}

// TestConcurrentReadsDuringWrites ensures readers always see a
// complete committed state: version/fields/tags mutually consistent,
// never a torn intermediate state.
func TestConcurrentReadsDuringWrites(t *testing.T) {
	c := New()
	mustRegister(t, c, baseSpec())
	id := entityID(baseSpec().Name)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: repeatedly add then (if possible) evolve. Only adds
	// optional fields, so each commit succeeds; versions are dense.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			cur, _ := c.Get(id)
			spec := baseSpec()
			spec.Fields = append(append([]FieldSpec(nil), cur.Fields...),
				FieldSpec{Name: fmt.Sprintf("live%04d", i), Type: TypeString})
			if _, err := c.RegisterAt(spec, cur.Version); err == nil {
				i++
			}
			if i > 200 {
				return
			}
		}
	}()

	// Readers: every observed snapshot must be internally consistent
	// and versions observed by one reader must never go backwards.
	// (Get and History are separate read-committed calls, so their
	// versions may differ when writes land in between; that is not a
	// torn state.)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lastVersion := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				e, err := c.Get(id)
				if err != nil {
					continue
				}
				if e.Version < lastVersion {
					t.Errorf("version went backwards: %d -> %d", lastVersion, e.Version)
					return
				}
				lastVersion = e.Version
				names := map[string]bool{}
				for _, f := range e.Fields {
					if names[f.Name] {
						t.Errorf("duplicate field in snapshot: %s", f.Name)
						return
					}
					names[f.Name] = true
				}
				if len(e.Fields) != e.Version+1 {
					t.Errorf("inconsistent snapshot: version=%d fields=%d", e.Version, len(e.Fields))
					return
				}
				if _, err := c.Search(SearchQuery{Keyword: "orders"}); err != nil {
					t.Errorf("search during writes: %v", err)
					return
				}
				time.Sleep(time.Microsecond)
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	// After writers stop, current state and history agree exactly.
	final := mustGet(t, c, id)
	hist, _ := c.History(id)
	if len(hist) != final.Version {
		t.Fatalf("final state/history mismatch: version=%d history=%d", final.Version, len(hist))
	}
}

func TestVersionConflictNoUpdate(t *testing.T) {
	c := New()
	mustRegister(t, c, baseSpec())
	id := entityID(baseSpec().Name)

	next := baseSpec()
	next.Fields = append(next.Fields, FieldSpec{Name: "note", Type: TypeString})
	if _, err := c.RegisterAt(next, 1); err != nil {
		t.Fatal(err) // advances to v2
	}

	stale := baseSpec()
	stale.Fields = append(stale.Fields, FieldSpec{Name: "late", Type: TypeString})
	_, err := c.RegisterAt(stale, 1)
	t.Logf("input: CAS at stale version 1 while current=2 -> verdict: %v", err)
	if reasonOf(err) != ReasonVersionConflict {
		t.Fatalf("expected version_conflict, got %v", err)
	}
	final := mustGet(t, c, id)
	if final.Version != 2 {
		t.Fatalf("stale CAS mutated state: version=%d", final.Version)
	}
}

func TestCreateOnlyCAS(t *testing.T) {
	c := New()
	mustRegister(t, c, baseSpec())

	_, err := c.RegisterAt(baseSpec(), 0)
	t.Logf("input: create-only CAS on existing entity -> verdict: %v", err)
	if reasonOf(err) != ReasonVersionConflict {
		t.Fatalf("expected version_conflict, got %v", err)
	}
}

// small helpers ----------------------------------------------------------------

func mustGet(t *testing.T, c *Catalog, id string) *Entity {
	t.Helper()
	e, err := c.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return e
}

func versionReason(t *testing.T, c *Catalog, id string) string {
	t.Helper()
	hist, err := c.History(id)
	if err != nil || len(hist) < 2 {
		return ""
	}
	return hist[len(hist)-1].Reason
}

func conflicts(ce *CatalogError) []Conflict {
	if ce == nil {
		return nil
	}
	return ce.Conflicts
}

func idsOf(es []*Entity) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func assertConflictErr(t *testing.T, ce *CatalogError, field, kind string) {
	t.Helper()
	for _, c := range ce.Conflicts {
		if c.Field == field && c.Kind == kind {
			return
		}
	}
	t.Fatalf("expected conflict %s/%s, got %+v", field, kind, ce.Conflicts)
}
