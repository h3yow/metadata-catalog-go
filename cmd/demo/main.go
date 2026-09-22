// Command demo exercises the metadata catalog end to end: repeated
// reports, compatible/incompatible evolution, cyclic lineage with
// impact analysis, search and optimistic-concurrency updates.
//
// Run with: go run ./cmd/demo
package main

import (
	"fmt"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

func main() {
	c := catalog.New()

	orders := catalog.EntitySpec{
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

	e, _ := c.Register(orders)
	fmt.Printf("1) registered %s at version %d\n", e.ID, e.Version)

	_, err := c.Register(orders)
	fmt.Printf("2) identical report -> %s\n", reason(err))

	v2 := orders
	v2.Fields = append(v2.Fields, catalog.FieldSpec{Name: "note", Type: catalog.TypeString})
	e2, _ := c.Register(v2)
	fmt.Printf("3) add optional field -> version %d\n", e2.Version)

	broken := orders
	broken.Fields = broken.Fields[:1]
	_, err = c.Register(broken)
	if ce, ok := catalog.AsCatalogError(err); ok {
		fmt.Printf("4) remove field -> %s: %+v\n", ce.Reason, ce.Conflicts)
	}

	// Lineage with a cycle: orders -> payments -> receipts -> orders
	for _, name := range []string{"payments", "receipts"} {
		s := orders
		s.Name = name
		if _, err := c.Register(s); err != nil {
			panic(err)
		}
	}
	edges := [][2]string{
		{"warehouse/orders", "warehouse/payments"},
		{"warehouse/payments", "warehouse/receipts"},
		{"warehouse/receipts", "warehouse/orders"},
	}
	for _, p := range edges {
		if err := c.AddLineage(p[0], p[1], "derived"); err != nil {
			panic(err)
		}
	}
	err = c.AddLineage("warehouse/orders", "warehouse/payments", "derived")
	fmt.Printf("5) duplicate edge -> %s\n", reason(err))

	down, _ := c.Downstream("warehouse/orders")
	fmt.Printf("6) downstream of orders (cycle-safe): %v\n", down.EntityIDs)
	up, _ := c.Upstream("warehouse/orders")
	fmt.Printf("   upstream of orders: %v\n", up.EntityIDs)

	hits, _ := c.Search(catalog.SearchQuery{Keyword: "amount"})
	fmt.Printf("7) search keyword=amount -> %d hit(s)\n", len(hits))

	// Optimistic update against a stale version.
	_, err = c.RegisterAt(v2, 1)
	fmt.Printf("8) CAS at stale version -> %s\n", reason(err))

	hist, _ := c.History("warehouse/orders")
	fmt.Printf("9) orders history versions:")
	for _, h := range hist {
		fmt.Printf(" v%d(%q)", h.Version, h.Reason)
	}
	fmt.Println()
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
