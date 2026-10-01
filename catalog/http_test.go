package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func newTestServer(t *testing.T) (*httptest.Server, *Catalog) {
	t.Helper()
	c := New()
	srv := httptest.NewServer(NewHTTPHandler(c))
	t.Cleanup(srv.Close)
	return srv, c
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
	return resp
}

func decodeErr(t *testing.T, resp *http.Response) errorResponse {
	t.Helper()
	defer resp.Body.Close()
	var er errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return er
}

func TestHTTPMultiSourceFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := postJSON(t, srv.URL+"/api/entities", whSpec())
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register warehouse: status %d", resp.StatusCode)
	}
	resp = postJSON(t, srv.URL+"/api/entities", bizSpec())
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register bizdb: status %d", resp.StatusCode)
	}

	var e Entity
	getJSON(t, srv.URL+"/api/entities/table/orders", &e)
	t.Logf("GET merged entity: fields=%v owners=%v tags=%v sources=%v version=%d",
		fieldNamesOf(e.Fields), e.Owners, e.Tags, e.Sources, e.Version)
	if e.Version != 2 || len(e.Fields) != 3 || len(e.Sources) != 2 {
		t.Fatalf("merged entity wrong: %+v", e)
	}
	if fmt.Sprint(e.FieldSources["amount"]) != fmt.Sprint([]string{"warehouse"}) {
		t.Fatalf("provenance lost over HTTP: %v", e.FieldSources)
	}

	var contribs []SourceContribution
	getJSON(t, srv.URL+"/api/entities/table/orders/sources", &contribs)
	if len(contribs) != 2 || contribs[0].Source != "bizdb" {
		t.Fatalf("sources endpoint: %+v", contribs)
	}

	var hist []EntityVersion
	getJSON(t, srv.URL+"/api/entities/table/orders/history", &hist)
	if len(hist) != 2 || hist[0].Source != "warehouse" || hist[1].Source != "bizdb" {
		t.Fatalf("history endpoint: %+v", hist)
	}
}

func TestHTTPConflictAndDuplicate(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := postJSON(t, srv.URL+"/api/entities", whSpec())
	resp.Body.Close()

	// Cross-source type conflict -> 422 + structured reason.
	bad := bizSpec()
	bad.Fields[0] = FieldSpec{Name: "id", Type: TypeString, Required: true}
	resp = postJSON(t, srv.URL+"/api/entities", bad)
	er := decodeErr(t, resp)
	t.Logf("conflicting report -> %d %s %+v", resp.StatusCode, er.Reason, er.Conflicts)
	if resp.StatusCode != http.StatusUnprocessableEntity || er.Reason != ReasonSourceConflict {
		t.Fatalf("expected 422 source_conflict, got %d %s", resp.StatusCode, er.Reason)
	}
	if len(er.Conflicts) != 1 || er.Conflicts[0].Field != "id" ||
		er.Conflicts[0].Kind != conflictTypeChanged || er.Conflicts[0].Source != "bizdb" {
		t.Fatalf("conflict payload wrong: %+v", er.Conflicts)
	}

	// Identical re-report -> 409 duplicate_report.
	resp = postJSON(t, srv.URL+"/api/entities", whSpec())
	er = decodeErr(t, resp)
	if resp.StatusCode != http.StatusConflict || er.Reason != ReasonDuplicateReport {
		t.Fatalf("expected 409 duplicate_report, got %d %s", resp.StatusCode, er.Reason)
	}

	// Unknown entity -> 404.
	resp, err := http.Get(srv.URL + "/api/entities/table/ghost")
	if err != nil {
		t.Fatal(err)
	}
	er = decodeErr(t, resp)
	if resp.StatusCode != http.StatusNotFound || er.Reason != ReasonEntityNotFound {
		t.Fatalf("expected 404 entity_not_found, got %d %s", resp.StatusCode, er.Reason)
	}
}

func TestHTTPSearchLineageAndImpact(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, name := range []string{"orders", "payments", "receipts"} {
		s := whSpec()
		s.Name = name
		resp := postJSON(t, srv.URL+"/api/entities", s)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("register %s: %d", name, resp.StatusCode)
		}
	}
	edges := [][2]string{
		{"table/orders", "table/payments"},
		{"table/payments", "table/receipts"},
		{"table/receipts", "table/orders"}, // cycle
	}
	for _, p := range edges {
		resp := postJSON(t, srv.URL+"/api/lineage", lineageRequest{
			UpstreamID: p[0], DownstreamID: p[1], Relation: "derived",
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("edge %v: %d", p, resp.StatusCode)
		}
	}

	var rep ImpactReport
	getJSON(t, srv.URL+"/api/entities/table/orders/downstream", &rep)
	t.Logf("downstream of orders: %v", rep.EntityIDs)
	if len(rep.EntityIDs) != 2 {
		t.Fatalf("expected payments+receipts, got %v", rep.EntityIDs)
	}
	// Stable, reproducible order across repeated queries.
	for i := 0; i < 10; i++ {
		var again ImpactReport
		getJSON(t, srv.URL+"/api/entities/table/orders/downstream", &again)
		if fmt.Sprint(again.EntityIDs) != fmt.Sprint(rep.EntityIDs) {
			t.Fatalf("impact order not reproducible: %v vs %v", again.EntityIDs, rep.EntityIDs)
		}
	}

	var hits []*Entity
	getJSON(t, srv.URL+"/api/entities?tag=core&keyword=payment", &hits)
	if len(hits) != 1 || hits[0].ID != "table/payments" {
		t.Fatalf("search tag+keyword: %+v", hits)
	}
	getJSON(t, srv.URL+"/api/entities?owner=data-team", &hits)
	if len(hits) != 3 {
		t.Fatalf("search owner: %d hits", len(hits))
	}
}

// TestHTTPConcurrentSubmitNoLostUpdates hammers the HTTP service with
// concurrent registrations from distinct sources; every commit must
// survive and readers must only observe self-consistent states.
func TestHTTPConcurrentSubmitNoLostUpdates(t *testing.T) {
	srv, _ := newTestServer(t)

	const n = 12
	var wg sync.WaitGroup
	errs := make(chan string, n*2)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := EntitySpec{
				Name:   "metrics",
				Source: fmt.Sprintf("agent%02d", i),
				Kind:   "table",
				Fields: []FieldSpec{
					{Name: "ts", Type: TypeDate, Required: true},
					{Name: fmt.Sprintf("m%02d", i), Type: TypeFloat},
				},
			}
			buf, _ := json.Marshal(spec)
			resp, err := http.Post(srv.URL+"/api/entities", "application/json", bytes.NewReader(buf))
			if err != nil {
				errs <- err.Error()
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				errs <- fmt.Sprintf("agent%02d: status %d", i, resp.StatusCode)
			}
		}(i)
	}
	// Concurrent readers: every snapshot must be self-consistent.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := 0
			for i := 0; i < 30; i++ {
				var e Entity
				resp, err := http.Get(srv.URL + "/api/entities/table/metrics")
				if err != nil {
					continue
				}
				if resp.StatusCode == http.StatusNotFound {
					resp.Body.Close()
					continue
				}
				if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
					resp.Body.Close()
					errs <- "decode: " + err.Error()
					return
				}
				resp.Body.Close()
				if e.Version < last {
					errs <- fmt.Sprintf("version went backwards: %d -> %d", last, e.Version)
					return
				}
				last = e.Version
				if len(e.Sources) != e.Version {
					errs <- fmt.Sprintf("torn state: version=%d sources=%d", e.Version, len(e.Sources))
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent HTTP: %s", e)
	}

	var e Entity
	getJSON(t, srv.URL+"/api/entities/table/metrics", &e)
	t.Logf("input: %d concurrent HTTP registrations -> version=%d fields=%d sources=%d",
		n, e.Version, len(e.Fields), len(e.Sources))
	if e.Version != n || len(e.Sources) != n || len(e.Fields) != n+1 {
		t.Fatalf("lost update through HTTP: %+v", e)
	}
}
