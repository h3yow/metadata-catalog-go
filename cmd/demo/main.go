// Command demo exercises the metadata catalog end to end: multi-source
// merge, repeated reports, compatible/incompatible evolution, cyclic
// lineage with impact analysis, search and optimistic-concurrency
// updates.
//
// Run with: go run ./cmd/demo
package main

import (
	"fmt"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

func main() {
	c := catalog.New()

	warehouse := catalog.EntitySpec{
		Name:   "orders",
		Source: "warehouse",
		Kind:   "table",
		Owners: []string{"data-team"},
		Tags:   []string{"core"},
		Fields: []catalog.FieldSpec{
			{Name: "id", Type: catalog.TypeInt, Required: true},
			{Name: "amount", Type: catalog.TypeFloat},
		},
	}
	bizdb := catalog.EntitySpec{
		Name:   "orders",
		Source: "bizdb",
		Kind:   "table",
		Owners: []string{"finance"},
		Tags:   []string{"pii"},
		Fields: []catalog.FieldSpec{
			{Name: "id", Type: catalog.TypeInt, Required: true},
			{Name: "note", Type: catalog.TypeString},
		},
	}

	e, _ := c.Register(warehouse)
	fmt.Printf("1) warehouse registered %s at version %d\n", e.ID, e.Version)

	e, _ = c.Register(bizdb)
	fmt.Printf("2) bizdb merged into same entity -> version %d, fields=%d sources=%v\n",
		e.Version, len(e.Fields), e.Sources)
	fmt.Printf("   amount contributed by %v, note by %v\n",
		e.FieldSources["amount"], e.FieldSources["note"])

	_, err := c.Register(warehouse)
	fmt.Printf("3) identical re-report -> %s\n", reason(err))

	bad := bizdb
	bad.Fields = append([]catalog.FieldSpec(nil), bizdb.Fields...)
	bad.Fields[0] = catalog.FieldSpec{Name: "id", Type: catalog.TypeString, Required: true}
	_, err = c.Register(bad)
	if ce, ok := catalog.AsCatalogError(err); ok {
		fmt.Printf("4) bizdb says id:string vs warehouse id:int -> %s: %+v\n", ce.Reason, ce.Conflicts)
	}

	drop := bizdb
	drop.DropFields = []string{"amount"}
	_, err = c.Register(drop)
	if ce, ok := catalog.AsCatalogError(err); ok {
		fmt.Printf("5) bizdb explicitly drops amount -> %s: %+v\n", ce.Reason, ce.Conflicts)
	}

	// Lineage with a cycle: orders -> payments -> receipts -> orders
	for _, name := range []string{"payments", "receipts"} {
		s := warehouse
		s.Name = name
		if _, err := c.Register(s); err != nil {
			panic(err)
		}
	}
	edges := [][2]string{
		{"table/orders", "table/payments"},
		{"table/payments", "table/receipts"},
		{"table/receipts", "table/orders"},
	}
	for _, p := range edges {
		if err := c.AddLineage(p[0], p[1], "derived"); err != nil {
			panic(err)
		}
	}
	err = c.AddLineage("table/orders", "table/payments", "derived")
	fmt.Printf("6) duplicate edge -> %s\n", reason(err))

	down, _ := c.Downstream("table/orders")
	fmt.Printf("7) downstream of orders (cycle-safe): %v\n", down.EntityIDs)
	up, _ := c.Upstream("table/orders")
	fmt.Printf("   upstream of orders: %v\n", up.EntityIDs)

	hits, _ := c.Search(catalog.SearchQuery{Tag: "pii"})
	fmt.Printf("8) search tag=pii -> %d hit(s): %s\n", len(hits), hits[0].ID)

	// Optimistic update against a stale version.
	stale := warehouse
	stale.Fields = append(append([]catalog.FieldSpec(nil), warehouse.Fields...),
		catalog.FieldSpec{Name: "late", Type: catalog.TypeString})
	_, err = c.RegisterAt(stale, 1)
	fmt.Printf("9) CAS at stale version -> %s\n", reason(err))

	hist, _ := c.History("table/orders")
	fmt.Printf("10) orders history:")
	for _, h := range hist {
		fmt.Printf(" v%d(by %s)", h.Version, h.Source)
	}
	fmt.Println()

	contribs, _ := c.Contributions("table/orders")
	for _, ct := range contribs {
		fmt.Printf("    source %-10s reported %d field(s) at %s\n",
			ct.Source, len(ct.Spec.Fields), ct.ReportedAt.Format("15:04:05.000"))
	}
}

func reason(err error) string {
	if err == nil {
		return "accepted"
	}
	if ce, ok := catalog.AsCatalogError(err); ok {
		return string(ce.Reason)
	}
	return err.Error()
}
