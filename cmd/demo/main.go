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
			{Name: "id", Type: catalog.TypeInt},
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
		{"table:orders", "table:payments"},
		{"table:payments", "table:receipts"},
		{"table:receipts", "table:orders"},
	}
	for _, p := range edges {
		if err := c.AddLineage(p[0], p[1], "derived"); err != nil {
			panic(err)
		}
	}
	err = c.AddLineage("table:orders", "table:payments", "derived")
	fmt.Printf("5) duplicate edge -> %s\n", reason(err))

	down, _ := c.Downstream("table:orders")
	fmt.Printf("6) downstream of table:orders (cycle-safe): %v\n", down.EntityIDs)
	up, _ := c.Upstream("table:orders")
	fmt.Printf("   upstream of table:orders: %v\n", up.EntityIDs)

	hits, _ := c.Search(catalog.SearchQuery{Keyword: "amount"})
	fmt.Printf("7) search keyword=amount -> %d hit(s)\n", len(hits))

	// Optimistic update against a stale version.
	stale := orders
	stale.Fields = append(stale.Fields, catalog.FieldSpec{Name: "note", Type: catalog.TypeString})
	_, err = c.RegisterAt(stale, 1)
	fmt.Printf("8) CAS at stale version -> %s\n", reason(err))

	hist, _ := c.History("table:orders")
	fmt.Printf("9) table:orders history versions:")
	for _, h := range hist {
		fmt.Printf(" v%d(%s, %q)", h.Version, h.Source, h.Reason)
	}
	fmt.Println()

	reps, _ := c.Reports("table:orders", "bizdb")
	fmt.Printf("10) bizdb's report log: %d entr(ies), first at %v\n",
		len(reps), reps[0].ReportedAt.Format("15:04:05"))

	// Compatible same-source field update: warehouse relaxes id to
	// optional and documents amount. The merged state, version and
	// history all move forward together.
	updated := orders
	updated.Fields = []catalog.FieldSpec{
		{Name: "id", Type: catalog.TypeInt}, // required -> optional
		{Name: "amount", Type: catalog.TypeFloat, Description: "order amount in cents"},
	}
	e3, err := c.Register(updated)
	fmt.Printf("11) compatible field update (relax required, document amount) -> %s, version %d\n",
		reason(err), e3.Version)
	fmt.Printf("    merged fields now: %+v\n", e3.Fields)

	// Same name, different kind: a topic named "orders" is a different
	// entity and coexists with the table.
	topic := catalog.EntitySpec{
		Name:   "orders",
		Source: "kafka",
		Kind:   "topic",
		Fields: []catalog.FieldSpec{{Name: "key", Type: catalog.TypeString}},
	}
	et, err := c.Register(topic)
	fmt.Printf("12) topic also named orders -> %s, independent entity %s (table is %s)\n",
		reason(err), et.ID, e3.ID)
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
