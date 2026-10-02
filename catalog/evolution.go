package catalog

import (
	"sort"
	"strings"
)

// validFieldTypes is the set of accepted FieldType values.
var validFieldTypes = map[FieldType]bool{
	TypeString: true,
	TypeInt:    true,
	TypeFloat:  true,
	TypeBool:   true,
	TypeDate:   true,
	TypeJSON:   true,
}

// validateSpec checks a reported spec for structural problems. It does
// not look at stored state.
func validateSpec(spec EntitySpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return errf(ReasonInvalidSpec, "entity name must not be empty")
	}
	if strings.Contains(spec.Name, "/") {
		return errf(ReasonInvalidSpec, "entity name %q must not contain '/'", spec.Name)
	}
	if strings.Contains(spec.Name, ":") {
		return errf(ReasonInvalidSpec, "entity name %q must not contain ':'", spec.Name)
	}
	if strings.Contains(spec.Kind, ":") {
		return errf(ReasonInvalidSpec, "entity kind %q must not contain ':'", spec.Kind)
	}
	if strings.TrimSpace(spec.Source) == "" {
		return errf(ReasonInvalidSpec, "entity source must not be empty")
	}
	seen := map[string]bool{}
	for _, f := range spec.Fields {
		if strings.TrimSpace(f.Name) == "" {
			return errf(ReasonInvalidSpec, "entity %q has a field with empty name", spec.Name)
		}
		if !validFieldTypes[f.Type] {
			return errf(ReasonInvalidSpec, "field %q has unknown type %q", f.Name, f.Type)
		}
		if seen[f.Name] {
			return errf(ReasonInvalidSpec, "entity %q declares field %q more than once", spec.Name, f.Name)
		}
		seen[f.Name] = true
	}
	for _, name := range spec.RemovedFields {
		if strings.TrimSpace(name) == "" {
			return errf(ReasonInvalidSpec, "entity %q has a removed field with empty name", spec.Name)
		}
		if seen[name] {
			return errf(ReasonInvalidSpec,
				"entity %q both declares and removes field %q", spec.Name, name)
		}
	}
	return nil
}

// conflict kinds emitted by mergeReport and diffSpecs.
const (
	conflictFieldRemoved  = "field_removed"
	conflictTypeChanged   = "type_changed"
	conflictRequiredAdded = "required_added"
)

// evolutionResult classifies a new report relative to the stored spec.
type evolutionResult struct {
	// duplicate is true when the report describes exactly the current
	// state (order of fields/owners/tags ignored).
	duplicate bool
	// conflicts lists every incompatible difference; empty means the
	// evolution is compatible.
	conflicts []Conflict
	// changed is true when at least one compatible attribute differs.
	changed bool
	// reason is a human-readable summary used as the version reason.
	reason string
}

// diffSpecs compares the current spec with a newly reported one from
// the same source. It is the single-source special case of the
// multi-source merge (see mergeReport): the current spec is treated
// as this source's only contribution.
//
// Compatibility rules:
//   - adding an optional field is compatible;
//   - removing a field is incompatible (field_removed);
//   - changing a field's type is incompatible (type_changed);
//   - adding a required field, or tightening an optional field to
//     required, is incompatible (required_added), because existing
//     producers/consumers cannot satisfy it;
//   - relaxing a required field to optional is compatible, as is
//     updating a field's description;
//   - entity / field description and owners / tags changes are always
//     compatible.
//
// The entity kind is never compared here: kind is part of the entity
// identity, so two specs with different kinds belong to different
// entities and never meet in one merge.
//
// Ordering inside fields, owners and tags is insignificant.
func diffSpecs(old, new EntitySpec) evolutionResult {
	mr := mergeReport(old, map[string]EntitySpec{new.Source: old}, new)
	res := evolutionResult{
		duplicate: mr.duplicate,
		conflicts: mr.conflicts,
		changed:   mr.changed || len(mr.conflicts) > 0,
		reason:    mr.reason,
	}
	res.duplicate = !res.changed
	return res
}

// sameStringSet reports whether two slices contain the same strings,
// ignoring order and duplicates.
func sameStringSet(a, b []string) bool {
	ca := cleanSet(a)
	cb := cleanSet(b)
	if len(ca) != len(cb) {
		return false
	}
	for s := range ca {
		if _, ok := cb[s]; !ok {
			return false
		}
	}
	return true
}

func cleanSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out[t] = struct{}{}
		}
	}
	return out
}

func joinSorted(items []string) string {
	sort.Strings(items)
	return strings.Join(items, ",")
}
