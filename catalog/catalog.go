package catalog

import (
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

// Register folds a source's report into the logical entity identified
// by the report's (name, kind) pair. Reports from any number of
// sources about the same logical entity merge into one record; the
// merged state is a union, never a last-writer overwrite. Same name
// with a different kind names an independent entity (e.g. table
// "orders" vs topic "orders"): the two records, their histories,
// report logs, search results and lineage never interact.
//
//   - A report identical to this source's own last accepted report is
//     rejected with ReasonDuplicateReport; nothing is written, history
//     and timestamps are untouched and no source information piles up.
//   - A report that conflicts with the merged state (field type
//     disagreement, required tightening, field removal, ...) is
//     rejected with ReasonIncompatibleChange and Conflicts
//     attributing each conflict to a source and a field.
//   - Compatible adjustments (changed field description, required
//     relaxed to optional, new optional field, owner/tag/description
//     metadata) are folded into the merged state and a new version is
//     committed whenever the merged state changes.
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

	id := entityID(spec.Name, spec.Kind)
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

	if !exists {
		contribs := map[string]EntitySpec{spec.Source: cloneSpec(spec)}
		merged := mergedSpecFrom(contribs)
		e := buildEntity(id, merged, contribs, 1, now, now)
		c.st.putNew(e, EntityVersion{
			Version:   1,
			Source:    spec.Source,
			Spec:      merged,
			CreatedAt: now,
			Reason:    "initial registration",
		}, SourceReport{Source: spec.Source, Spec: cloneSpec(spec), ReportedAt: now, Version: 1})
		out := cloneEntity(e)
		return &out, nil
	}

	res := mergeReport(specFromEntity(rec.current), rec.contribs, spec)
	switch {
	case res.duplicate:
		return nil, errf(ReasonDuplicateReport,
			"entity %q: report identical to source %q's last report (version %d)",
			id, spec.Source, rec.current.Version)
	case len(res.conflicts) > 0:
		return nil, &CatalogError{
			Reason:    ReasonIncompatibleChange,
			Message:   "entity " + id + ": report from source " + spec.Source + " conflicts with the merged state",
			Conflicts: res.conflicts,
		}
	}

	report := SourceReport{Source: spec.Source, Spec: cloneSpec(spec), ReportedAt: now}
	if !res.changed {
		// The source's contribution changed (so it is recorded and
		// queryable) but the merged state did not: no new version,
		// no timestamp refresh.
		report.Version = rec.current.Version
		c.st.recordReport(rec, report)
		out := cloneEntity(rec.current)
		return &out, nil
	}

	report.Version = rec.current.Version + 1
	contribs := cloneContribs(rec.contribs)
	contribs[spec.Source] = cloneSpec(spec)
	e := buildEntity(id, res.merged, contribs, report.Version, rec.current.CreatedAt, now)
	c.st.applyVersion(rec, e, EntityVersion{
		Version:   e.Version,
		Source:    spec.Source,
		Spec:      res.merged,
		CreatedAt: now,
		Reason:    res.reason,
	}, report)
	out := cloneEntity(e)
	return &out, nil
}

// Get returns a copy of the current merged state of an entity,
// including per-field/owner/tag source attribution.
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
// version first. Each version carries the merged state and the source
// whose report produced it.
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

// Reports returns the accepted-report log of an entity in commit
// order: which source reported what and when. When source is
// non-empty, only that source's reports are returned. Rejected and
// duplicate reports never appear here.
func (c *Catalog) Reports(id, source string) ([]SourceReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.st.get(id)
	if !ok {
		return nil, errf(ReasonEntityNotFound, "entity %q not found", id)
	}
	out := make([]SourceReport, 0, len(rec.reports))
	for _, r := range rec.reports {
		if source != "" && r.Source != source {
			continue
		}
		out = append(out, r)
	}
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

// specFromEntity reconstructs the merged spec view of an entity.
func specFromEntity(e Entity) EntitySpec {
	return EntitySpec{
		Name:        e.Name,
		Kind:        e.Kind,
		Description: e.Description,
		Owners:      cloneStrings(e.Owners),
		Tags:        cloneStrings(e.Tags),
		Fields:      cloneFields(e.Fields),
	}
}

// mergedSpecFrom computes the merged spec of a brand-new entity from
// its first contribution: owners/tags are de-duplicated and sorted so
// the merged state is canonical regardless of report order.
func mergedSpecFrom(contribs map[string]EntitySpec) EntitySpec {
	owners, tags := map[string]struct{}{}, map[string]struct{}{}
	var merged EntitySpec
	for _, cs := range contribs {
		merged.Name = cs.Name
		merged.Kind = cs.Kind
		merged.Description = cs.Description
		merged.Fields = cloneFields(cs.Fields)
		for _, o := range cs.Owners {
			owners[o] = struct{}{}
		}
		for _, t := range cs.Tags {
			tags[t] = struct{}{}
		}
	}
	merged.Owners = sortedKeys(owners)
	merged.Tags = sortedKeys(tags)
	return merged
}

// buildEntity derives the stored entity view from a merged spec and
// the current contributions, computing the source attribution maps.
func buildEntity(id string, merged EntitySpec, contribs map[string]EntitySpec, version int, createdAt, updatedAt time.Time) Entity {
	fieldSrc := map[string]map[string]struct{}{}
	ownerSrc := map[string]map[string]struct{}{}
	tagSrc := map[string]map[string]struct{}{}
	for src, cs := range contribs {
		for _, f := range cs.Fields {
			putAttrib(fieldSrc, f.Name, src)
		}
		for _, o := range cs.Owners {
			putAttrib(ownerSrc, o, src)
		}
		for _, t := range cs.Tags {
			putAttrib(tagSrc, t, src)
		}
	}
	return Entity{
		ID:           id,
		Name:         merged.Name,
		Kind:         merged.Kind,
		Description:  merged.Description,
		Owners:       cloneStrings(merged.Owners),
		Tags:         cloneStrings(merged.Tags),
		Fields:       cloneFields(merged.Fields),
		Sources:      sortedContribSources(contribs),
		FieldSources: flattenAttrib(fieldSrc),
		OwnerSources: flattenAttrib(ownerSrc),
		TagSources:   flattenAttrib(tagSrc),
		Version:      version,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
}

func putAttrib(m map[string]map[string]struct{}, key, src string) {
	set, ok := m[key]
	if !ok {
		set = map[string]struct{}{}
		m[key] = set
	}
	set[src] = struct{}{}
}

func flattenAttrib(m map[string]map[string]struct{}) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, set := range m {
		out[k] = sortedKeys(set)
	}
	return out
}

func sortedContribSources(contribs map[string]EntitySpec) []string {
	set := make(map[string]struct{}, len(contribs))
	for s := range contribs {
		set[s] = struct{}{}
	}
	return sortedKeys(set)
}

func cloneContribs(in map[string]EntitySpec) map[string]EntitySpec {
	out := make(map[string]EntitySpec, len(in))
	for s, spec := range in {
		out[s] = cloneSpec(spec)
	}
	return out
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
	e.FieldSources = cloneAttrib(e.FieldSources)
	e.OwnerSources = cloneAttrib(e.OwnerSources)
	e.TagSources = cloneAttrib(e.TagSources)
	return e
}

func cloneAttrib(in map[string][]string) map[string][]string {
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
	s.RemovedFields = cloneStrings(s.RemovedFields)
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
