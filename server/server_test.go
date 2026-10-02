package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

func newTestServer(t *testing.T) (*httptest.Server, *catalog.Catalog) {
	t.Helper()
	c := catalog.New()
	srv := httptest.NewServer(New(c))
	t.Cleanup(srv.Close)
	return srv, c
}

func postReport(t *testing.T, srv *httptest.Server, spec catalog.EntitySpec) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/v1/reports", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/reports: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func specOf(source string, fields ...catalog.FieldSpec) catalog.EntitySpec {
	return catalog.EntitySpec{
		Name: "orders", Source: source, Kind: "table", Fields: fields,
	}
}

func TestHTTPMultiSourceFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	st, body := postReport(t, srv, specOf("bizdb",
		catalog.FieldSpec{Name: "id", Type: catalog.TypeInt, Required: true},
		catalog.FieldSpec{Name: "amount", Type: catalog.TypeFloat},
	))
	if st != http.StatusOK {
		t.Fatalf("first report: status %d body %v", st, body)
	}

	// Identical re-report -> 409 duplicate_report.
	st, body = postReport(t, srv, specOf("bizdb",
		catalog.FieldSpec{Name: "id", Type: catalog.TypeInt, Required: true},
		catalog.FieldSpec{Name: "amount", Type: catalog.TypeFloat},
	))
	t.Logf("input: identical re-report -> verdict: status=%d reason=%v", st, body["reason"])
	if st != http.StatusConflict || body["reason"] != string(catalog.ReasonDuplicateReport) {
		t.Fatalf("duplicate: status=%d body=%v", st, body)
	}

	// Second source merges.
	wh := specOf("warehouse", catalog.FieldSpec{Name: "note", Type: catalog.TypeString})
	wh.Owners = []string{"bob"}
	wh.Tags = []string{"curated"}
	st, body = postReport(t, srv, wh)
	if st != http.StatusOK {
		t.Fatalf("second source report: status %d body %v", st, body)
	}

	// Current state: merged, attributed.
	var e catalog.Entity
	if getJSON(t, srv.URL+"/v1/entities/orders.table", &e) != http.StatusOK {
		t.Fatal("GET entity failed")
	}
	t.Logf("input: GET /v1/entities/orders.table -> verdict: version=%d sources=%v fields=%d",
		e.Version, e.Sources, len(e.Fields))
	if len(e.Sources) != 2 || len(e.Fields) != 3 {
		t.Fatalf("merge over HTTP wrong: sources=%v fields=%+v", e.Sources, e.Fields)
	}
	if got := e.FieldSources["note"]; len(got) != 1 || got[0] != "warehouse" {
		t.Fatalf("attribution missing over HTTP: %v", e.FieldSources)
	}

	// Cross-source conflict -> 409 with structured conflicts.
	// (warehouse keeps contributing note; only amount's type clashes.)
	bad := specOf("warehouse",
		catalog.FieldSpec{Name: "note", Type: catalog.TypeString},
		catalog.FieldSpec{Name: "amount", Type: catalog.TypeString},
	)
	st, body = postReport(t, srv, bad)
	t.Logf("input: conflicting type report -> verdict: status=%d reason=%v", st, body["reason"])
	if st != http.StatusConflict || body["reason"] != string(catalog.ReasonIncompatibleChange) {
		t.Fatalf("conflict: status=%d body=%v", st, body)
	}
	confs, _ := body["conflicts"].([]any)
	if len(confs) == 0 {
		t.Fatalf("conflict body missing conflicts: %v", body)
	}
	first, _ := confs[0].(map[string]any)
	if first["source"] != "warehouse" || first["field"] != "amount" || first["kind"] != "type_changed" {
		t.Fatalf("conflict not attributed: %v", first)
	}

	// Version history.
	var vs []catalog.EntityVersion
	getJSON(t, srv.URL+"/v1/entities/orders.table/versions", &vs)
	if len(vs) != 2 || vs[1].Source != "warehouse" {
		t.Fatalf("versions wrong: %+v", vs)
	}

	// Per-source report log.
	var reps []catalog.SourceReport
	getJSON(t, srv.URL+"/v1/entities/orders.table/reports?source=warehouse", &reps)
	if len(reps) != 1 || reps[0].Source != "warehouse" || reps[0].ReportedAt.IsZero() {
		t.Fatalf("reports log wrong: %+v", reps)
	}

	// Unknown entity -> 404.
	var ignored map[string]any
	if st := getJSON(t, srv.URL+"/v1/entities/nope", &ignored); st != http.StatusNotFound {
		t.Fatalf("unknown entity: status %d", st)
	}
}

func TestHTTPSearchLineageImpact(t *testing.T) {
	srv, _ := newTestServer(t)

	mk := func(name string, tags []string) catalog.EntitySpec {
		return catalog.EntitySpec{Name: name, Source: "etl", Kind: "table", Tags: tags,
			Fields: []catalog.FieldSpec{{Name: "id", Type: catalog.TypeInt}}}
	}
	for _, s := range []catalog.EntitySpec{
		mk("raw_orders", []string{"ingest"}),
		mk("orders", []string{"core"}),
		mk("revenue", []string{"core", "finance"}),
	} {
		if st, body := postReport(t, srv, s); st != http.StatusOK {
			t.Fatalf("register %s: %d %v", s.Name, st, body)
		}
	}
	postEdge := func(up, down string) int {
		body, _ := json.Marshal(map[string]string{
			"upstream_id": up, "downstream_id": down, "relation": "derived",
		})
		resp, err := http.Post(srv.URL+"/v1/lineage", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := postEdge("raw_orders.table", "orders.table"); st != http.StatusCreated {
		t.Fatalf("edge 1: %d", st)
	}
	if st := postEdge("orders.table", "revenue.table"); st != http.StatusCreated {
		t.Fatalf("edge 2: %d", st)
	}
	if st := postEdge("orders.table", "revenue.table"); st != http.StatusConflict {
		t.Fatalf("duplicate edge: %d", st)
	}

	// Search by tag.
	var hits []catalog.Entity
	getJSON(t, srv.URL+"/v1/entities?tag=core", &hits)
	t.Logf("input: GET /v1/entities?tag=core -> verdict: %d hits", len(hits))
	if len(hits) != 2 {
		t.Fatalf("tag search: %d hits", len(hits))
	}
	// Search by keyword.
	getJSON(t, srv.URL+"/v1/entities?keyword=revenue", &hits)
	if len(hits) != 1 || hits[0].ID != "revenue.table" {
		t.Fatalf("keyword search: %+v", hits)
	}

	// Impact analysis, stable across repeated queries.
	var first, again catalog.ImpactReport
	getJSON(t, srv.URL+"/v1/entities/raw_orders.table/downstream", &first)
	t.Logf("input: GET downstream of raw_orders -> verdict: %v", first.EntityIDs)
	if len(first.EntityIDs) != 2 {
		t.Fatalf("downstream: %v", first.EntityIDs)
	}
	for i := 0; i < 20; i++ {
		getJSON(t, srv.URL+"/v1/entities/raw_orders.table/downstream", &again)
		if fmt.Sprint(again.EntityIDs) != fmt.Sprint(first.EntityIDs) {
			t.Fatalf("impact order unstable: %v vs %v", again.EntityIDs, first.EntityIDs)
		}
	}
	var up catalog.ImpactReport
	getJSON(t, srv.URL+"/v1/entities/revenue.table/upstream", &up)
	if len(up.EntityIDs) != 2 {
		t.Fatalf("upstream: %v", up.EntityIDs)
	}
}

// TestHTTPConcurrentSubmitNoLoss: many sources submit distinct fields
// for the same entity over HTTP concurrently. Every commit must land
// (fields == version at the end) and every concurrent read must
// observe a self-consistent state (fields == version on every GET).
func TestHTTPConcurrentSubmitNoLoss(t *testing.T) {
	srv, _ := newTestServer(t)

	const n = 20
	var writers sync.WaitGroup
	var reader sync.WaitGroup
	errs := make(chan string, n*2)

	stop := make(chan struct{})
	// Reader: every observed state must be internally consistent.
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp, err := http.Get(srv.URL + "/v1/entities/events.table")
			if err != nil {
				continue
			}
			var e catalog.Entity
			_ = json.NewDecoder(resp.Body).Decode(&e)
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				continue // not yet created
			}
			if len(e.Fields) != e.Version {
				errs <- fmt.Sprintf("torn read: version=%d fields=%d", e.Version, len(e.Fields))
				return
			}
		}
	}()

	// Writers: one field each, from distinct sources.
	for i := 0; i < n; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			spec := catalog.EntitySpec{
				Name:   "events",
				Source: fmt.Sprintf("src%02d", i),
				Kind:   "table",
				Fields: []catalog.FieldSpec{{Name: fmt.Sprintf("f%02d", i), Type: catalog.TypeString}},
			}
			body, _ := json.Marshal(spec)
			resp, err := http.Post(srv.URL+"/v1/reports", "application/json", bytes.NewReader(body))
			if err != nil {
				errs <- err.Error()
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Sprintf("worker %d: status %d", i, resp.StatusCode)
			}
		}(i)
	}
	writers.Wait()
	close(stop)
	reader.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent HTTP error: %s", e)
	}

	var e catalog.Entity
	getJSON(t, srv.URL+"/v1/entities/events.table", &e)
	t.Logf("input: %d concurrent HTTP reports -> verdict: version=%d fields=%d sources=%d",
		n, e.Version, len(e.Fields), len(e.Sources))
	if e.Version != n || len(e.Fields) != n || len(e.Sources) != n {
		t.Fatalf("lost update over HTTP: version=%d fields=%d sources=%d",
			e.Version, len(e.Fields), len(e.Sources))
	}
}
