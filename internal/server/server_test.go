package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/h3yow/metadata-catalog-go/internal/catalog"
)

func newTestServer() (*Server, *httptest.Server) {
	s := New(catalog.New())
	return s, httptest.NewServer(s.Handler())
}

func postJSON(t *testing.T, url string, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	t.Logf("INPUT: POST %s body=%s => status=%d body=%v", url, body, resp.StatusCode, m)
	return resp.StatusCode, m
}

func TestHTTPEndToEnd(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	entity := `{"name":"orders","source":"team-a","kind":"table","fields":[
		{"name":"id","type":"int64","semantic":"order.id","optional":false},
		{"name":"amount","type":"decimal","semantic":"order.amount","optional":false}],
		"tags":["finance"],"owners":["alice"]}`

	// 首次登记 201
	status, body := postJSON(t, ts.URL+"/v1/entities", entity)
	if status != http.StatusCreated || body["change"] != "created" {
		t.Fatalf("want 201 created, got %d %v", status, body)
	}
	// 重复上报 200 noop
	status, body = postJSON(t, ts.URL+"/v1/entities", entity)
	if status != http.StatusOK || body["change"] != "noop" {
		t.Fatalf("want 200 noop, got %d %v", status, body)
	}

	// 不兼容：删除字段
	removed := `{"name":"orders","source":"team-a","kind":"table","fields":[
		{"name":"id","type":"int64","semantic":"order.id","optional":false}],
		"tags":["finance"],"owners":["alice"]}`
	status, body = postJSON(t, ts.URL+"/v1/entities", removed)
	if status != http.StatusBadRequest || body["reason"] != "field_removed" {
		t.Fatalf("want 400 reason=field_removed, got %d %v", status, body)
	}

	// 不兼容：类型变化
	typeChanged := `{"name":"orders","source":"team-a","kind":"table","fields":[
		{"name":"id","type":"int64","semantic":"order.id","optional":false},
		{"name":"amount","type":"string","semantic":"order.amount","optional":false}],
		"tags":["finance"],"owners":["alice"]}`
	status, body = postJSON(t, ts.URL+"/v1/entities", typeChanged)
	if status != http.StatusBadRequest || body["reason"] != "field_type_changed" {
		t.Fatalf("want 400 reason=field_type_changed, got %d %v", status, body)
	}

	// 兼容演进：新增可选字段
	okEntity := `{"name":"orders","source":"team-a","kind":"table","fields":[
		{"name":"id","type":"int64","semantic":"order.id","optional":false},
		{"name":"amount","type":"decimal","semantic":"order.amount","optional":false},
		{"name":"coupon","type":"string","semantic":"order.coupon","optional":true}],
		"tags":["finance"],"owners":["alice"]}`
	status, body = postJSON(t, ts.URL+"/v1/entities", okEntity)
	if status != http.StatusOK || body["change"] != "compatible" {
		t.Fatalf("want compatible, got %d %v", status, body)
	}

	// 404
	resp, err := http.Get(ts.URL + "/v1/entities/ghost")
	if err != nil {
		t.Fatal(err)
	}
	var eb map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&eb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || eb["reason"] != "entity_not_found" {
		t.Fatalf("want 404 entity_not_found, got %d %v", resp.StatusCode, eb)
	}
}

func TestHTTPLineageAndSearch(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	audit := `{"name":"audit","source":"s","kind":"table","fields":[{"name":"id","type":"int64"}]}`
	orders := `{"name":"orders","source":"s","kind":"table","fields":[{"name":"id","type":"int64"}],"tags":["finance"],"owners":["alice"]}`
	if code, _ := postJSON(t, ts.URL+"/v1/entities", orders); code != http.StatusCreated {
		t.Fatal("seed orders")
	}
	if code, _ := postJSON(t, ts.URL+"/v1/entities", audit); code != http.StatusCreated {
		t.Fatal("seed audit")
	}

	edge := `{"upstream":"orders","downstream":"audit","source":"job-1"}`
	code, body := postJSON(t, ts.URL+"/v1/lineage", edge)
	if code != 200 || body["duplicate"] != false {
		t.Fatalf("first edge want accepted, got %d %v", code, body)
	}
	code, body = postJSON(t, ts.URL+"/v1/lineage", edge)
	if code != 200 || body["duplicate"] != true {
		t.Fatalf("second edge want duplicate, got %d %v", code, body)
	}

	resp, err := http.Get(ts.URL + "/v1/lineage/downstream/orders")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	ds, _ := m["downstream"].([]any)
	if len(ds) != 1 || ds[0] != "audit" {
		t.Fatalf("downstream want [audit], got %v", m)
	}

	// 自环拒绝
	code, body = postJSON(t, ts.URL+"/v1/lineage", `{"upstream":"orders","downstream":"orders","source":"j"}`)
	if code != 400 || body["reason"] != "self_lineage_loop" {
		t.Fatalf("want self_lineage_loop, got %d %v", code, body)
	}

	// 检索
	resp, err = http.Get(ts.URL + "/v1/search?tag=finance&owner=alice")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0]["name"] != "orders" {
		t.Fatalf("search want [orders], got %v", list)
	}
}
