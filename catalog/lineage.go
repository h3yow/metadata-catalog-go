package catalog

// Traversal semantics
//
// Lineage is a directed graph whose edges point producer -> consumer.
// Both Downstream and Upstream run an iterative breadth-first search:
//
//   - The root is marked visited before traversal starts, so cycles
//     (including cycles that lead back to the root) never enqueue the
//     same entity twice and recursion depth is bounded by the number
//     of entities.
//   - When several paths reach the same entity, it is reported once
//     (first visit wins); every distinct edge is still reported once.
//   - At each node neighbours are visited in entity-ID order, so the
//     report is stable and reproducible for the same graph,
//     independent of map iteration order.
//   - The root itself is never included in EntityIDs.

// Downstream returns every entity reachable by following producer ->
// consumer edges from id (the impact set when id changes).
func (c *Catalog) Downstream(id string) (*ImpactReport, error) {
	return c.traverse(id, "downstream")
}

// Upstream returns every entity reachable by following edges backwards
// from id (the set of producers whose changes can affect id).
func (c *Catalog) Upstream(id string) (*ImpactReport, error) {
	return c.traverse(id, "upstream")
}

func (c *Catalog) traverse(root, direction string) (*ImpactReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.st.get(root); !ok {
		return nil, errf(ReasonEntityNotFound, "entity %q not found", root)
	}

	adj := c.st.adjacency(direction)

	visited := map[string]bool{root: true}
	queue := []string{root}
	report := &ImpactReport{Root: root, Direction: direction}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, edge := range adj[current] {
			// Record the edge before deduping the neighbour: every
			// distinct path contributes its edge, but revisited nodes
			// are never re-enqueued.
			report.Edges = append(report.Edges, edge)

			next := edge.DownstreamID
			if direction == "upstream" {
				next = edge.UpstreamID
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			report.EntityIDs = append(report.EntityIDs, next)
			queue = append(queue, next)
		}
	}

	return report, nil
}
