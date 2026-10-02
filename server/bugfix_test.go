package server

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

// TestMain prints a coverage banner for the HTTP package tests so
// `go test -v` shows which business scenarios were exercised; each
// case logs its input and verdict inline.
func TestMain(m *testing.M) {
	fmt.Println("=== metadata-catalog server package business coverage ===")
	fmt.Println("  [bugfix] same-name different-kind entities isolated over HTTP")
	fmt.Println("  [bugfix] compatible field update visible via entity/versions/reports")
	fmt.Println("  [existing] end-to-end merge flow, structured conflict mapping")
	fmt.Println("  [existing] search/lineage/impact and concurrent submit no-loss")
	fmt.Println("=========================================================")
	os.Exit(m.Run())
}

// TestHTTPSameNameDifferentKindIsolated: over HTTP a table and a topic
// both called "orders" get independent IDs; GET/versions/reports and
// lineage operate on exactly one of them.
func TestHTTPSameNameDifferentKindIsolated(t *testing.T) {
	srv, _ := newTestServer(t)

	table := specOf("bizdb",
		catalog.FieldSpec{Name: "id", Type: catalog.TypeInt, Required: true},
		catalog.FieldSpec{Name: "amount", Type: catalog.TypeFloat},
	)
	topic := catalog.EntitySpec{
		Name: "orders", Source: "kafka", Kind: "topic",
		Fields: []catalog.FieldSpec{
			{Name: "id", Type: catalog.TypeString},
			{Name: "payload", Type: catalog.TypeJSON},
		},
	}
	if st, body := postReport(t, srv, table); st != http.StatusOK {
		t.Fatalf("table report: %d %v", st, body)
	}
	st, body := postReport(t, srv, topic)
	t.Logf("input: POST topic 'orders' after table 'orders' -> verdict: status=%d id=%v",
		st, body["id"])
	if st != http.StatusOK {
		t.Fatalf("same-name different-kind must coexist, got %d %v", st, body)
	}
	if body["id"] != "orders.topic" {
		t.Fatalf("expected independent id orders.topic, got %v", body["id"])
	}

	// Current states isolated.
	var tbl, top catalog.Entity
	if getJSON(t, srv.URL+"/v1/entities/orders.table", &tbl) != http.StatusOK {
		t.Fatal("GET orders.table failed")
	}
	if getJSON(t, srv.URL+"/v1/entities/orders.topic", &top) != http.StatusOK {
		t.Fatal("GET orders.topic failed")
	}
	t.Logf("verdict: table fields=%d sources=%v | topic fields=%d sources=%v",
		len(tbl.Fields), tbl.Sources, len(top.Fields), top.Sources)
	if len(tbl.Fields) != 2 || len(top.Fields) != 2 {
		t.Fatalf("field sets bled across: %d / %d", len(tbl.Fields), len(top.Fields))
	}
	if len(tbl.Sources) != 1 || tbl.Sources[0] != "bizdb" ||
		len(top.Sources) != 1 || top.Sources[0] != "kafka" {
		t.Fatalf("source attribution bled across: %v / %v", tbl.Sources, top.Sources)
	}

	// Histories and reports isolated.
	var hTop []catalog.EntityVersion
	getJSON(t, srv.URL+"/v1/entities/orders.topic/versions", &hTop)
	if len(hTop) != 1 || hTop[0].Source != "kafka" {
		t.Fatalf("topic history wrong: %+v", hTop)
	}
	var repsTbl []catalog.SourceReport
	getJSON(t, srv.URL+"/v1/entities/orders.table/reports?source=kafka", &repsTbl)
	if len(repsTbl) != 0 {
		t.Fatalf("kafka report leaked into table log: %+v", repsTbl)
	}

	// A conflicting submit against the table is rejected and must not
	// touch the topic either.
	bad := specOf("warehouse",
		catalog.FieldSpec{Name: "amount", Type: catalog.TypeString})
	st, body = postReport(t, srv, bad)
	t.Logf("input: type-clashing report against table -> verdict: status=%d reason=%v",
		st, body["reason"])
	if st != http.StatusConflict || body["reason"] != string(catalog.ReasonIncompatibleChange) {
		t.Fatalf("expected 409 incompatible_change, got %d %v", st, body)
	}
	var topAfter catalog.Entity
	getJSON(t, srv.URL+"/v1/entities/orders.topic", &topAfter)
	if topAfter.Version != 1 || len(topAfter.Sources) != 1 {
		t.Fatal("rejected table report affected the topic entity")
	}
}

// TestHTTPCompatibleFieldUpdate: a same-source field description
// update is immediately visible through the entity endpoint, the
// versions endpoint carries a new snapshot and the reports endpoint
// shows the new content at that version.
func TestHTTPCompatibleFieldUpdate(t *testing.T) {
	srv, _ := newTestServer(t)

	v1 := specOf("bizdb", catalog.FieldSpec{
		Name: "amount", Type: catalog.TypeFloat, Description: "old doc",
	})
	if st, body := postReport(t, srv, v1); st != http.StatusOK {
		t.Fatalf("v1: %d %v", st, body)
	}
	v2 := specOf("bizdb", catalog.FieldSpec{
		Name: "amount", Type: catalog.TypeFloat, Description: "new doc",
	})
	st, body := postReport(t, srv, v2)
	t.Logf("input: field description old doc -> new doc -> verdict: status=%d version=%v",
		st, body["version"])
	if st != http.StatusOK {
		t.Fatalf("compatible update rejected: %d %v", st, body)
	}
	if body["version"].(float64) != 2 {
		t.Fatalf("expected version 2, got %v", body["version"])
	}

	var e catalog.Entity
	getJSON(t, srv.URL+"/v1/entities/orders.table", &e)
	if e.Fields[0].Description != "new doc" {
		t.Fatalf("current state stale: %q", e.Fields[0].Description)
	}
	var vs []catalog.EntityVersion
	getJSON(t, srv.URL+"/v1/entities/orders.table/versions", &vs)
	if len(vs) != 2 || vs[1].Spec.Fields[0].Description != "new doc" {
		t.Fatalf("history stale: %+v", vs)
	}
	var reps []catalog.SourceReport
	getJSON(t, srv.URL+"/v1/entities/orders.table/reports?source=bizdb", &reps)
	if len(reps) != 2 || reps[1].Version != 2 || reps[1].Spec.Fields[0].Description != "new doc" {
		t.Fatalf("report log stale: %+v", reps)
	}
	t.Logf("verdict: GET=%q versions=%d reports=%d@v%d — three views agree",
		e.Fields[0].Description, len(vs), len(reps), reps[1].Version)
}
