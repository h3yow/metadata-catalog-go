// Command demo exercises the metadata catalog end to end: multi-source
// merge with attribution, repeated reports, compatible/incompatible
// evolution, cyclic lineage with impact analysis, search and
// optimistic-concurrency updates.
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
	fmt.Printf("1) registered %s at version %d (source %s)\n", e.ID, e.Version, orders.Source)

	// A second source reports the same logical entity: merged, not duplicated.
	biz := catalog.EntitySpec{
		Name:   "orders",
		Source: "bizdb",
		Kind:   "table",
		Owners: []string{"billing"},
		Tags:   []string{"pii"},
		Fields: []catalog.FieldSpec{
			{Name: "id", Type: catalog.TypeInt, Required: true},
			{Name: "currency", Type: catalog.TypeString},
		},
	}
	e2, _ := c.Register(biz)
	fmt.Printf("2) bizdb reports same entity -> still %s, version %d, sources %v, fields %d\n",
		e2.ID, e2.Version, e2.Sources, len(e2.Fields))
	fmt.Printf("   attribution: amount<- %v, currency<- %v, owners %v\n",
		e2.FieldSources["amount"], e2.FieldSources["currency"], e2.Owners)

	_, err := c.Register(orders)
	fmt.Printf("3) identical re-report from warehouse -> %s\n", reason(err))

	// Cross-source conflict: bizdb disagrees on amount's type.
	bad := biz
	bad.Fields = append([]catalog.FieldSpec{}, biz.Fields...)
	bad.Fields = append(bad.Fields, catalog.FieldSpec{Name: "amount", Type: catalog.TypeString})
	_, err = c.Register(bad)
	if ce, ok := catalog.AsCatalogError(err); ok {
		fmt.Printf("4) cross-source type conflict -> %s: %+v\n", ce.Reason, ce.Conflicts)
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
		{"orders", "payments"},
		{"payments", "receipts"},
		{"receipts", "orders"},
	}
	for _, p := range edges {
		if err := c.AddLineage(p[0], p[1], "derived"); err != nil {
			panic(err)
		}
	}
	err = c.AddLineage("orders", "payments", "derived")
	fmt.Printf("5) duplicate edge -> %s\n", reason(err))

	down, _ := c.Downstream("orders")
	fmt.Printf("6) downstream of orders (cycle-safe): %v\n", down.EntityIDs)
	up, _ := c.Upstream("orders")
	fmt.Printf("   upstream of orders: %v\n", up.EntityIDs)

	hits, _ := c.Search(catalog.SearchQuery{Keyword: "amount"})
	fmt.Printf("7) search keyword=amount -> %d hit(s)\n", len(hits))

	// Optimistic update against a stale version.
	stale := orders
	stale.Fields = append(stale.Fields, catalog.FieldSpec{Name: "note", Type: catalog.TypeString})
	_, err = c.RegisterAt(stale, 1)
	fmt.Printf("8) CAS at stale version -> %s\n", reason(err))

	hist, _ := c.History("orders")
	fmt.Printf("9) orders history versions:")
	for _, h := range hist {
		fmt.Printf(" v%d(%s, %q)", h.Version, h.Source, h.Reason)
	}
	fmt.Println()

	reps, _ := c.Reports("orders", "bizdb")
	fmt.Printf("10) bizdb's report log: %d entr(ies), first at %v\n",
		len(reps), reps[0].ReportedAt.Format("15:04:05"))
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
