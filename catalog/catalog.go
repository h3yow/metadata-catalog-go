package catalog

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Catalog is the local metadata catalog. All operations are safe for
// concurrent use: a single mutex serializes commits, so readers never
// observe a half-applied registration/update and concurrent updates
// cannot be lost.
type Catalog struct {
	mu sync.Mutex
	st *store
}

// New creates an empty catalog.
func New() *Catalog {
	return &Catalog{st: newStore()}
}

// Register registers a new entity or merges a new report of an
// existing one. It performs an unconditional (last-writer) commit.
//
//   - Reports from every source of the same logical entity (same
//     kind + normalised name, see entityID) are merged into one
//     record; each source's contribution is tracked separately.
//   - A report identical to what its source already contributed is
//     rejected with ReasonDuplicateReport; nothing is written,
//     history is untouched and no timestamp is refreshed.
//   - A report that clashes with its own source's previous
//     contribution (field removed, type changed, required added) is
//     rejected with ReasonIncompatibleChange; a report that clashes
//     with *other* sources (disagreeing types, explicit drops) is
//     rejected with ReasonSourceConflict. Both carry the concrete
//     Conflicts with source, field and kind.
//   - A compatible report is merged and committed as a new version.
func (c *Catalog) Register(spec EntitySpec) (*Entity, error) {
	return c.RegisterAt(spec, -1)
}

// RegisterAt is like Register but performs optimistic concurrency
// control: the report is only applied when the entity's current
// version equals expectedVersion.
//
// expectedVersion semantics:
//   - -1: no check (unconditional commit, used by Register);
//   - 0: the entity must not exist yet (create-only);
//   - >0: the entity must exist at exactly that version.
//
// On mismatch the call fails with ReasonVersionConflict and no state
// changes, letting the caller re-read, merge and retry instead of
// silently overwriting a concurrent update.
func (c *Catalog) RegisterAt(spec EntitySpec, expectedVersion int) (*Entity, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	id := entityID(spec.Kind, spec.Name)
	now := time.Now().UTC()

	rec, exists := c.st.get(id)
	if expectedVersion == 0 && exists {
		return nil, errf(ReasonVersionConflict,
			"entity %q: expected to create, already exists at version %d", id, rec.current.Version)
	}
	if expectedVersion > 0 && (!exists || rec.current.Version != expectedVersion) {
		current := 0
		if exists {
			current = rec.current.Version
		}
		return nil, errf(ReasonVersionConflict,
			"entity %q: expected version %d but current version is %d", id, expectedVersion, current)
	}

	var recArg *entityRecord
	if exists {
		recArg = rec
	}
	out := mergeReport(recArg, cloneSpec(spec))
	switch {
	case out.duplicate:
		version := 0
		if exists {
			version = rec.current.Version
		}
		return nil, errf(ReasonDuplicateReport,
			"entity %q: report identical to the stored contribution of source %q (version %d)",
			id, spec.Source, version)
	case out.invalid != nil:
		return nil, out.invalid
	case len(out.conflicts) > 0:
		reason := ReasonIncompatibleChange
		if out.cross {
			reason = ReasonSourceConflict
		}
		return nil, &CatalogError{
			Reason:    reason,
			Message:   "entity " + id + ": report from source " + spec.Source + " rejected",
			Conflicts: out.conflicts,
		}
	}

	e := out.entity
	e.UpdatedAt = now
	if !exists {
		e.Version = 1
		e.CreatedAt = now
		c.st.putNew(e, EntityVersion{
			Version:   1,
			Source:    spec.Source,
			Spec:      out.mergedSpec,
			CreatedAt: now,
			Reason:    out.reason,
		}, &SourceContribution{
			Source:     spec.Source,
			Spec:       cloneSpec(spec),
			ReportedAt: now,
		})
	} else {
		e.Version = rec.current.Version + 1
		e.CreatedAt = rec.current.CreatedAt
		c.st.applyVersion(rec, e, EntityVersion{
			Version:   e.Version,
			Source:    spec.Source,
			Spec:      out.mergedSpec,
			CreatedAt: now,
			Reason:    out.reason,
		})
		rec.sources[spec.Source] = &SourceContribution{
			Source:     spec.Source,
			Spec:       cloneSpec(spec),
			ReportedAt: now,
		}
	}
	cp := cloneEntity(e)
	return &cp, nil
}

// Get returns a copy of the current state of an entity.
func (c *Catalog) Get(id string) (*Entity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.st.get(id)
	if !ok {
		return nil, errf(ReasonEntityNotFound, "entity %q not found", id)
	}
	e := cloneEntity(rec.current)
	return &e, nil
}

// History returns the immutable version history of an entity, oldest
// version first.
func (c *Catalog) History(id string) ([]EntityVersion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.st.get(id)
	if !ok {
		return nil, errf(ReasonEntityNotFound, "entity %q not found", id)
	}
	out := make([]EntityVersion, len(rec.history))
	copy(out, rec.history)
	return out, nil
}

// Contributions returns the last accepted report of every source of
// an entity, sorted by source name: what each source reported and
// when it was committed. Identical re-reports are rejected as
// duplicates, so a contribution's ReportedAt is never refreshed by
// repeated identical reports.
func (c *Catalog) Contributions(id string) ([]SourceContribution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.st.get(id)
	if !ok {
		return nil, errf(ReasonEntityNotFound, "entity %q not found", id)
	}
	out := make([]SourceContribution, 0, len(rec.sources))
	for _, s := range rec.sources {
		out = append(out, SourceContribution{
			Source:     s.Source,
			Spec:       cloneSpec(s.Spec),
			ReportedAt: s.ReportedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// AddLineage registers a directed upstream -> downstream dependency.
// Registering the same pair twice is rejected with
// ReasonDuplicateEdge instead of silently overwriting.
func (c *Catalog) AddLineage(upstreamID, downstreamID, relation string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if upstreamID == downstreamID {
		return errf(ReasonSelfEdge, "entity %q cannot depend on itself", upstreamID)
	}
	if _, ok := c.st.get(upstreamID); !ok {
		return errf(ReasonEntityNotFound, "upstream entity %q not found", upstreamID)
	}
	if _, ok := c.st.get(downstreamID); !ok {
		return errf(ReasonEntityNotFound, "downstream entity %q not found", downstreamID)
	}
	edge := LineageEdge{
		UpstreamID:   upstreamID,
		DownstreamID: downstreamID,
		Relation:     relation,
		RegisteredAt: time.Now().UTC(),
	}
	if !c.st.addEdge(edge) {
		return errf(ReasonDuplicateEdge, "lineage %q -> %q already registered", upstreamID, downstreamID)
	}
	return nil
}

// Search returns entities matching all non-empty conditions in q.
// Results reflect the latest committed state and are sorted by ID.
func (c *Catalog) Search(q SearchQuery) ([]*Entity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	var out []*Entity
	for _, e := range c.st.snapshot() {
		if q.Tag != "" && !containsString(e.Tags, q.Tag) {
			continue
		}
		if q.Owner != "" && !containsString(e.Owners, q.Owner) {
			continue
		}
		if kw != "" && !matchesKeyword(e, kw) {
			continue
		}
		cp := cloneEntity(e)
		out = append(out, &cp)
	}
	return out, nil
}

func matchesKeyword(e Entity, kw string) bool {
	if strings.Contains(strings.ToLower(e.Name), kw) ||
		strings.Contains(strings.ToLower(e.Description), kw) ||
		strings.Contains(strings.ToLower(e.Kind), kw) {
		return true
	}
	for _, f := range e.Fields {
		if strings.Contains(strings.ToLower(f.Name), kw) {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func cloneEntity(e Entity) Entity {
	e.Owners = cloneStrings(e.Owners)
	e.Tags = cloneStrings(e.Tags)
	e.Fields = cloneFields(e.Fields)
	e.Sources = cloneStrings(e.Sources)
	e.FieldSources = cloneStringMap(e.FieldSources)
	e.OwnerSources = cloneStringMap(e.OwnerSources)
	e.TagSources = cloneStringMap(e.TagSources)
	return e
}

func cloneStringMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = cloneStrings(v)
	}
	return out
}

func cloneSpec(s EntitySpec) EntitySpec {
	s.Owners = cloneStrings(s.Owners)
	s.Tags = cloneStrings(s.Tags)
	s.Fields = cloneFields(s.Fields)
	return s
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneFields(in []FieldSpec) []FieldSpec {
	if in == nil {
		return nil
	}
	out := make([]FieldSpec, len(in))
	copy(out, in)
	return out
}
