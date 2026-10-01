package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// Multi-source merge semantics
//
// Every report is a *partial* view: a source only declares the
// fields/owners/tags it cares about. The catalog keeps the last
// accepted report of each source (its "contribution") and derives the
// entity's current state as the union of all contributions:
//
//   - fields: union by name; a field disappears only when its last
//     contributing source stops reporting it, which is a breaking
//     change and is rejected (field_removed). A source that simply
//     does not mention a field never deletes it for the others
//     ("not mentioned" != "deleted"). An *explicit* DropFields
//     request is always rejected as field_removed.
//   - a field's type must agree across all sources; disagreement is
//     rejected (type_changed). Tightening optional -> required is
//     rejected (required_added); new fields must be optional.
//   - owners/tags: union of every source's latest sets, deduplicated.
//   - entity/field descriptions: picked deterministically from the
//     lexicographically smallest contributing source with a
//     non-empty description, so the merged state does not depend on
//     report order.
//
// Re-reporting content identical to the source's stored contribution
// is a duplicate: nothing is written, no version is added and no
// timestamp is refreshed.

// mergeOutcome is the verdict of applying one report to an entity
// record (nil record = entity does not exist yet).
type mergeOutcome struct {
	// duplicate: the report equals the source's stored contribution.
	duplicate bool
	// invalid: a structural problem only detectable against state
	// (e.g. dropping a field the entity does not have).
	invalid *CatalogError
	// conflicts: every incompatible difference; empty = acceptable.
	conflicts []Conflict
	// cross is true when at least one conflict involves contributions
	// of sources other than the reporter (-> source_conflict rather
	// than incompatible_change).
	cross bool
	// entity is the merged state to commit (Version/CreatedAt/
	// UpdatedAt are filled in by the caller).
	entity Entity
	// mergedSpec is the merged spec stored in the version history.
	mergedSpec EntitySpec
	// reason summarises the merge for the version history.
	reason string
}

// fieldView aggregates one field across contributions.
type fieldView struct {
	spec    FieldSpec // type is the agreed type; Required is the OR
	sources []string  // contributing sources, sorted
}

// mergeReport computes how report `spec` from source spec.Source
// would change the entity record `rec` (nil when the entity is new).
// It performs no writes.
func mergeReport(rec *entityRecord, spec EntitySpec) mergeOutcome {
	if rec == nil {
		if len(spec.DropFields) > 0 {
			return mergeOutcome{invalid: errf(ReasonInvalidSpec,
				"entity %q: source %q cannot drop fields %v: the entity does not exist yet",
				entityID(spec.Kind, spec.Name), spec.Source, spec.DropFields)}
		}
		contribs := map[string]EntitySpec{spec.Source: spec}
		e, merged := buildMerged(entityID(spec.Kind, spec.Name), spec, contribs)
		return mergeOutcome{
			entity:     e,
			mergedSpec: merged,
			reason:     "initial registration by source " + fmt.Sprintf("%q", spec.Source),
		}
	}

	// Idempotency: identical re-report by the same source.
	if stored, ok := rec.sources[spec.Source]; ok && sameReport(stored.Spec, spec) {
		return mergeOutcome{duplicate: true}
	}

	cur := aggregateFields(storedContribs(rec))
	var out mergeOutcome
	cross := false

	// Explicit drops are always breaking.
	for _, d := range spec.DropFields {
		view, ok := cur[d]
		if !ok {
			return mergeOutcome{invalid: errf(ReasonInvalidSpec,
				"entity %q: source %q cannot drop field %q: not part of the entity",
				rec.current.ID, spec.Source, d)}
		}
		if onlySource(view.sources, spec.Source) {
			out.conflicts = append(out.conflicts, Conflict{
				Source: spec.Source,
				Field:  d,
				Kind:   conflictFieldRemoved,
				Detail: fmt.Sprintf("source %q explicitly requested to drop the field", spec.Source),
			})
		} else {
			cross = true
			out.conflicts = append(out.conflicts, Conflict{
				Source: spec.Source,
				Field:  d,
				Kind:   conflictFieldRemoved,
				Detail: fmt.Sprintf("source %q explicitly requested to drop the field, still reported by %s",
					spec.Source, quoteList(otherSources(view.sources, spec.Source))),
			})
		}
	}

	// The report's fields must agree with the current merged state.
	for _, f := range spec.Fields {
		view, ok := cur[f.Name]
		others := otherSources(view.sources, spec.Source)
		switch {
		case !ok:
			if f.Required {
				out.conflicts = append(out.conflicts, Conflict{
					Source: spec.Source,
					Field:  f.Name,
					Kind:   conflictRequiredAdded,
					Detail: "new field is declared required",
				})
			}
		case view.spec.Type != f.Type:
			c := Conflict{
				Source: spec.Source,
				Field:  f.Name,
				Kind:   conflictTypeChanged,
				Detail: fmt.Sprintf("type %s reported by source %q conflicts with type %s from source(s) %s",
					f.Type, spec.Source, view.spec.Type, quoteList(others)),
			}
			if len(others) > 0 {
				cross = true
			}
			out.conflicts = append(out.conflicts, c)
		case !view.spec.Required && f.Required:
			c := Conflict{
				Source: spec.Source,
				Field:  f.Name,
				Kind:   conflictRequiredAdded,
				Detail: "optional field tightened to required",
			}
			if len(others) > 0 {
				cross = true
			}
			out.conflicts = append(out.conflicts, c)
		}
	}

	// Omission by the last contributing source removes the field from
	// the merged state: a breaking change.
	after := contributionsAfter(rec, spec)
	afterFields := aggregateFields(after)
	for name, view := range cur {
		if _, ok := afterFields[name]; !ok {
			out.conflicts = append(out.conflicts, Conflict{
				Source: spec.Source,
				Field:  name,
				Kind:   conflictFieldRemoved,
				Detail: fmt.Sprintf("field was only reported by source(s) %s and the new report from %q no longer includes it",
					quoteList(view.sources), spec.Source),
			})
		}
	}

	if len(out.conflicts) > 0 {
		out.cross = cross
		sortConflicts(out.conflicts)
		return out
	}

	e, merged := buildMerged(rec.current.ID, spec, after)
	e.Name = rec.current.Name
	e.Kind = rec.current.Kind
	out.entity = e
	out.mergedSpec = merged
	out.reason = mergeReason(rec.current, e, spec.Source)
	return out
}

// storedContribs flattens the stored per-source contributions of rec.
func storedContribs(rec *entityRecord) map[string]EntitySpec {
	out := make(map[string]EntitySpec, len(rec.sources))
	for s, c := range rec.sources {
		out[s] = c.Spec
	}
	return out
}

// contributionsAfter returns the prospective contribution map once
// the report replaces the reporter's previous contribution.
func contributionsAfter(rec *entityRecord, spec EntitySpec) map[string]EntitySpec {
	out := storedContribs(rec)
	out[spec.Source] = spec
	return out
}

// aggregateFields unions the fields of all contributions. Stored
// contributions are mutually consistent (enforced at accept time), so
// the first type seen wins and Required is the OR.
func aggregateFields(contribs map[string]EntitySpec) map[string]fieldView {
	type acc struct {
		spec    FieldSpec
		sources map[string]bool
	}
	tmp := map[string]*acc{}
	for src, spec := range contribs {
		for _, f := range spec.Fields {
			a, ok := tmp[f.Name]
			if !ok {
				a = &acc{spec: FieldSpec{Name: f.Name, Type: f.Type}, sources: map[string]bool{}}
				tmp[f.Name] = a
			}
			a.spec.Required = a.spec.Required || f.Required
			a.sources[src] = true
		}
	}
	out := make(map[string]fieldView, len(tmp))
	for name, a := range tmp {
		out[name] = fieldView{spec: a.spec, sources: sortedKeys(a.sources)}
	}
	return out
}

// buildMerged derives the merged entity and merged spec from the
// prospective contributions. Deterministic: every pick that involves
// multiple sources uses the lexicographically smallest source.
func buildMerged(id string, report EntitySpec, contribs map[string]EntitySpec) (Entity, EntitySpec) {
	sources := make([]string, 0, len(contribs))
	for s := range contribs {
		sources = append(sources, s)
	}
	sort.Strings(sources)

	// Fields: union, sorted by name for a stable shape.
	views := aggregateFields(contribs)
	fieldNames := make([]string, 0, len(views))
	for name := range views {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	fields := make([]FieldSpec, 0, len(fieldNames))
	fieldSources := map[string][]string{}
	for _, name := range fieldNames {
		v := views[name]
		f := v.spec
		f.Description = fieldDescription(name, sources, contribs)
		fields = append(fields, f)
		fieldSources[name] = v.sources
	}

	owners, ownerSources := unionAttributed(sources, contribs, func(s EntitySpec) []string { return s.Owners })
	tags, tagSources := unionAttributed(sources, contribs, func(s EntitySpec) []string { return s.Tags })

	e := Entity{
		ID:           id,
		Name:         report.Name,
		Kind:         report.Kind,
		Description:  entityDescription(sources, contribs),
		Owners:       owners,
		Tags:         tags,
		Fields:       fields,
		Sources:      sources,
		FieldSources: fieldSources,
		OwnerSources: ownerSources,
		TagSources:   tagSources,
	}
	merged := EntitySpec{
		Name:        e.Name,
		Kind:        e.Kind,
		Description: e.Description,
		Owners:      cloneStrings(owners),
		Tags:        cloneStrings(tags),
		Fields:      cloneFields(fields),
	}
	return e, merged
}

// fieldDescription picks the description of one field
// deterministically: smallest source name with a non-empty entry.
func fieldDescription(field string, sources []string, contribs map[string]EntitySpec) string {
	for _, s := range sources {
		for _, f := range contribs[s].Fields {
			if f.Name == field && f.Description != "" {
				return f.Description
			}
		}
	}
	return ""
}

// entityDescription picks the entity description deterministically.
func entityDescription(sources []string, contribs map[string]EntitySpec) string {
	for _, s := range sources {
		if d := contribs[s].Description; d != "" {
			return d
		}
	}
	return ""
}

// unionAttributed merges string lists (owners/tags) across sources:
// the union, deduplicated and sorted, plus per-value provenance.
func unionAttributed(sources []string, contribs map[string]EntitySpec, pick func(EntitySpec) []string) ([]string, map[string][]string) {
	values := map[string]bool{}
	attr := map[string]map[string]bool{}
	for _, s := range sources {
		for _, v := range pick(contribs[s]) {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			values[v] = true
			if attr[v] == nil {
				attr[v] = map[string]bool{}
			}
			attr[v][s] = true
		}
	}
	list := sortedKeys(values)
	prov := map[string][]string{}
	for v, srcs := range attr {
		prov[v] = sortedKeys(srcs)
	}
	return list, prov
}

// mergeReason summarises the delta between the previous merged state
// and the new one for the version history.
func mergeReason(prev, next Entity, source string) string {
	var changes []string
	if !sameStringSet(prev.Sources, next.Sources) {
		changes = append(changes, "sources: "+joinSorted(next.Sources))
	}
	added, removed := diffStrings(fieldNamesOf(prev.Fields), fieldNamesOf(next.Fields))
	if len(added) > 0 {
		changes = append(changes, "fields added: "+joinSorted(added))
	}
	if len(removed) > 0 {
		changes = append(changes, "fields removed: "+joinSorted(removed))
	}
	if prev.Description != next.Description {
		changes = append(changes, "description")
	}
	if !sameStringSet(prev.Owners, next.Owners) {
		changes = append(changes, "owners: "+joinSorted(next.Owners))
	}
	if !sameStringSet(prev.Tags, next.Tags) {
		changes = append(changes, "tags: "+joinSorted(next.Tags))
	}
	if len(changes) == 0 {
		return fmt.Sprintf("source %q: new contributor, merged state unchanged", source)
	}
	return fmt.Sprintf("source %q: %s", source, strings.Join(changes, "; "))
}

// sameReport reports whether a new report is semantically identical
// to the source's stored contribution: field set (with type,
// required, description), owner/tag sets and description, ignoring
// order. Any DropFields make it different by definition.
func sameReport(stored, report EntitySpec) bool {
	if len(report.DropFields) > 0 {
		return false
	}
	if stored.Description != report.Description {
		return false
	}
	if !sameStringSet(stored.Owners, report.Owners) || !sameStringSet(stored.Tags, report.Tags) {
		return false
	}
	if len(stored.Fields) != len(report.Fields) {
		return false
	}
	byName := make(map[string]FieldSpec, len(stored.Fields))
	for _, f := range stored.Fields {
		byName[f.Name] = f
	}
	for _, f := range report.Fields {
		sf, ok := byName[f.Name]
		if !ok || sf != f {
			return false
		}
	}
	return true
}

func onlySource(sources []string, s string) bool {
	return len(sources) == 1 && sources[0] == s
}

// otherSources returns the sources except s, preserving order.
func otherSources(sources []string, s string) []string {
	out := make([]string, 0, len(sources))
	for _, v := range sources {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fieldNamesOf(fields []FieldSpec) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Name
	}
	return out
}

// diffStrings returns the elements only in next (added) and only in
// prev (removed), as sets.
func diffStrings(prev, next []string) (added, removed []string) {
	p, n := cleanSet(prev), cleanSet(next)
	for s := range n {
		if _, ok := p[s]; !ok {
			added = append(added, s)
		}
	}
	for s := range p {
		if _, ok := n[s]; !ok {
			removed = append(removed, s)
		}
	}
	return added, removed
}

func sortConflicts(cs []Conflict) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Field != cs[j].Field {
			return cs[i].Field < cs[j].Field
		}
		return cs[i].Kind < cs[j].Kind
	})
}
