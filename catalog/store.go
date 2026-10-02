package catalog

import "strings"

// entityRecord bundles the current merged state of an entity with its
// immutable version history, the latest accepted report of every
// contributing source and the chronological report log.
type entityRecord struct {
	current Entity
	history []EntityVersion
	// contribs maps source -> that source's last accepted report.
	contribs map[string]EntitySpec
	// reports is the accepted-report log across all sources, in
	// commit order. Rejected and duplicate reports are never logged.
	reports []SourceReport
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

// entityID derives the stable identity of a logical entity from its
// (trimmed) name AND kind together, never from the reporting source:
// the same logical entity reported by any number of sources maps to
// one record.
//
// Same name + same kind => the same logical entity, merged across all
// sources. Same name + different kind => two independent records (a
// table "orders" and a topic "orders" coexist; their current state,
// version history, report logs, search results and lineage never
// touch each other). validateSpec guarantees kind contains neither
// "." nor "/" and name contains no "/", so the "name.kind" encoding
// is unambiguous (the kind is the part after the final dot) and stays
// usable as an HTTP path segment.
func entityID(name, kind string) string {
	return strings.TrimSpace(name) + "." + strings.TrimSpace(kind)
}

func edgeKey(upstreamID, downstreamID string) string {
	return upstreamID + "\x00" + downstreamID
}

func (s *store) get(id string) (*entityRecord, bool) {
	rec, ok := s.entities[id]
	return rec, ok
}

// putNew inserts a brand-new entity with version 1.
func (s *store) putNew(e Entity, v EntityVersion, report SourceReport) {
	s.entities[e.ID] = &entityRecord{
		current:  e,
		history:  []EntityVersion{v},
		contribs: map[string]EntitySpec{report.Source: cloneSpec(report.Spec)},
		reports:  []SourceReport{report},
	}
}

// applyVersion replaces the current state, appends to the history,
// updates the reporting source's contribution and appends to the
// report log atomically (from the caller's perspective, under the
// catalog lock).
func (s *store) applyVersion(rec *entityRecord, e Entity, v EntityVersion, report SourceReport) {
	rec.current = e
	rec.history = append(rec.history, v)
	rec.contribs[report.Source] = cloneSpec(report.Spec)
	rec.reports = append(rec.reports, report)
}

// recordReport updates the reporting source's contribution and
// appends to the report log without touching the current state or
// history: used when an accepted report leaves the merged state
// unchanged.
func (s *store) recordReport(rec *entityRecord, report SourceReport) {
	rec.contribs[report.Source] = cloneSpec(report.Spec)
	rec.reports = append(rec.reports, report)
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
