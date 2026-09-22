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
	return nil
}

// conflict kinds emitted by diffSpecs.
const (
	conflictFieldRemoved    = "field_removed"
	conflictTypeChanged     = "type_changed"
	conflictRequiredAdded   = "required_added"
	conflictSemanticChanged = "semantic_changed"
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

// diffSpecs compares the current spec with a newly reported one.
//
// Compatibility rules:
//   - adding an optional field is compatible;
//   - removing a field is incompatible (field_removed);
//   - changing a field's type is incompatible (type_changed);
//   - adding a required field, or tightening an optional field to
//     required, is incompatible (required_added), because existing
//     producers/consumers cannot satisfy it;
//   - changing the entity kind is incompatible (semantic_changed);
//   - description / owners / tags changes are always compatible.
//
// Ordering inside fields, owners and tags is insignificant.
func diffSpecs(old, new EntitySpec) evolutionResult {
	res := evolutionResult{}

	oldFields := map[string]FieldSpec{}
	for _, f := range old.Fields {
		oldFields[f.Name] = f
	}
	newFields := map[string]FieldSpec{}
	for _, f := range new.Fields {
		newFields[f.Name] = f
	}

	var added, removed, changed []string

	// Fields present before: removal and modification.
	for name, of := range oldFields {
		nf, ok := newFields[name]
		switch {
		case !ok:
			res.conflicts = append(res.conflicts, Conflict{
				Field:  name,
				Kind:   conflictFieldRemoved,
				Detail: "field existed in the previous version and was removed",
			})
			removed = append(removed, name)
		case nf.Type != of.Type:
			res.conflicts = append(res.conflicts, Conflict{
				Field:  name,
				Kind:   conflictTypeChanged,
				Detail: "type changed from " + string(of.Type) + " to " + string(nf.Type),
			})
		case !of.Required && nf.Required:
			res.conflicts = append(res.conflicts, Conflict{
				Field:  name,
				Kind:   conflictRequiredAdded,
				Detail: "optional field tightened to required",
			})
		}
	}

	// Fields present now: additions.
	for name, nf := range newFields {
		if _, ok := oldFields[name]; !ok {
			if nf.Required {
				res.conflicts = append(res.conflicts, Conflict{
					Field:  name,
					Kind:   conflictRequiredAdded,
					Detail: "new field is declared required",
				})
			} else {
				added = append(added, name)
			}
		}
	}

	// Entity-level semantic change.
	if old.Kind != new.Kind {
		res.conflicts = append(res.conflicts, Conflict{
			Field:  "kind",
			Kind:   conflictSemanticChanged,
			Detail: "kind changed from " + old.Kind + " to " + new.Kind,
		})
	}

	// Compatible metadata changes.
	if old.Description != new.Description {
		changed = append(changed, "description")
	}
	if !sameStringSet(old.Owners, new.Owners) {
		changed = append(changed, "owners")
	}
	if !sameStringSet(old.Tags, new.Tags) {
		changed = append(changed, "tags")
	}
	if len(added) > 0 {
		changed = append(changed, "fields added: "+joinSorted(added))
	}
	if len(removed) > 0 {
		changed = append(changed, "fields removed: "+joinSorted(removed))
	}

	res.changed = len(changed) > 0 || len(res.conflicts) > 0
	res.duplicate = !res.changed
	sort.Strings(changed)
	res.reason = strings.Join(changed, "; ")
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
