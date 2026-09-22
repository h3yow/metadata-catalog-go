package catalog

// entityRecord bundles the current state of an entity with its
// immutable version history.
type entityRecord struct {
	current Entity
	history []EntityVersion
}

// store is the in-process local storage of the catalog. It is only
// ever accessed while Catalog.mu is held, so it needs no locking of
// its own.
type store struct {
	// entities maps entity ID -> record.
	entities map[string]*entityRecord
	// edges maps "upstreamID\x00downstreamID" -> edge.
	edges map[string]LineageEdge
}

func newStore() *store {
	return &store{
		entities: map[string]*entityRecord{},
		edges:    map[string]LineageEdge{},
	}
}

// entityID derives the stable identity of an entity from its source
// and name. The same (source, name) pair always maps to the same ID,
// which is what makes repeated reports idempotent.
func entityID(source, name string) string {
	return source + "/" + name
}

func edgeKey(upstreamID, downstreamID string) string {
	return upstreamID + "\x00" + downstreamID
}

func (s *store) get(id string) (*entityRecord, bool) {
	rec, ok := s.entities[id]
	return rec, ok
}

// putNew inserts a brand-new entity with version 1.
func (s *store) putNew(e Entity, v EntityVersion) {
	s.entities[e.ID] = &entityRecord{
		current: e,
		history: []EntityVersion{v},
	}
}

// applyVersion replaces the current state and appends to the history
// atomically (from the caller's perspective, under the catalog lock).
func (s *store) applyVersion(rec *entityRecord, e Entity, v EntityVersion) {
	rec.current = e
	rec.history = append(rec.history, v)
}

// addEdge inserts a lineage edge. It returns false when the edge
// already exists, so the caller can reject duplicate registrations
// instead of silently overwriting.
func (s *store) addEdge(e LineageEdge) bool {
	k := edgeKey(e.UpstreamID, e.DownstreamID)
	if _, exists := s.edges[k]; exists {
		return false
	}
	s.edges[k] = e
	return true
}

// adjacency builds the outgoing adjacency list for traversal in the
// given direction. Neighbours are returned sorted by entity ID so
// traversal order is deterministic.
func (s *store) adjacency(direction string) map[string][]LineageEdge {
	adj := map[string][]LineageEdge{}
	for _, e := range s.edges {
		switch direction {
		case "downstream":
			adj[e.UpstreamID] = append(adj[e.UpstreamID], e)
		case "upstream":
			adj[e.DownstreamID] = append(adj[e.DownstreamID], e)
		}
	}
	for id := range adj {
		sortEdges(adj[id])
	}
	return adj
}

func sortEdges(edges []LineageEdge) {
	// insertion sort; edge lists are small
	for i := 1; i < len(edges); i++ {
		for j := i; j > 0 && lessEdge(edges[j], edges[j-1]); j-- {
			edges[j], edges[j-1] = edges[j-1], edges[j]
		}
	}
}

func lessEdge(a, b LineageEdge) bool {
	if a.UpstreamID != b.UpstreamID {
		return a.UpstreamID < b.UpstreamID
	}
	return a.DownstreamID < b.DownstreamID
}

// snapshot returns a copy of all current entities, sorted by ID.
func (s *store) snapshot() []Entity {
	out := make([]Entity, 0, len(s.entities))
	for _, rec := range s.entities {
		out = append(out, rec.current)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
