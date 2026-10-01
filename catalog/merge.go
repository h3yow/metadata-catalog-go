package catalog

import (
	"sort"
	"strings"
)

// mergeResult classifies the outcome of folding one source's report
// into the current contributions of an entity.
type mergeResult struct {
	// duplicate is true when the report is identical to this source's
	// own last accepted report (order-insensitive).
	duplicate bool
	// conflicts lists every cross-source / evolution conflict; empty
	// means the report can be merged.
	conflicts []Conflict
	// merged is the merged entity spec after applying the report;
	// only valid when conflicts is empty.
	merged EntitySpec
	// changed is true when the merged state or the source attribution
	// differs from before.
	changed bool
	// reason is a human-readable summary used as the version reason.
	reason string
}

// mergeReport folds spec (reported by spec.Source) into the entity's
// current merged state and contributions.
//
//   - currentMerged is the entity's current merged spec; contribs maps
//     source -> that source's last accepted report.
//   - A report identical to this source's own last report is a
//     duplicate: nothing may change.
//   - Fields are unioned across sources. A field a source simply does
//     not mention is never treated as deleted. A field is only lost
//     when its last contributing source drops it, which is rejected as
//     field_removed; explicit RemovedFields entries are always
//     rejected as field_removed as well.
//   - All contributors of a field must agree on its type; a required
//     tightening is rejected; kind must match the entity's kind.
//   - Owners and tags are unioned and de-duplicated across sources.
func mergeReport(currentMerged EntitySpec, contribs map[string]EntitySpec, spec EntitySpec) mergeResult {
	res := mergeResult{}

	// 1. Idempotency: identical re-report from the same source.
	prev, reportedBefore := contribs[spec.Source]
	if reportedBefore && specEquals(prev, spec) {
		res.duplicate = true
		return res
	}

	// 2. Entity identity attributes must agree across sources.
	if currentMerged.Kind != spec.Kind {
		res.conflicts = append(res.conflicts, Conflict{
			Source: spec.Source,
			Field:  "kind",
			Kind:   conflictSemanticChanged,
			Detail: "kind is " + currentMerged.Kind + " but source " + spec.Source +
				" reports " + spec.Kind,
		})
	}

	// 3. Explicit removal requests are always rejected: deletion is a
	//    breaking change no single source may perform unilaterally.
	mergedFields := map[string]FieldSpec{}
	for _, f := range currentMerged.Fields {
		mergedFields[f.Name] = f
	}
	for _, name := range spec.RemovedFields {
		detail := "source " + spec.Source + " explicitly removed the field"
		if _, ok := mergedFields[name]; !ok {
			detail += " (field not present in merged state)"
		}
		res.conflicts = append(res.conflicts, Conflict{
			Source: spec.Source,
			Field:  name,
			Kind:   conflictFieldRemoved,
			Detail: detail,
		})
	}

	// 4. Implicit removal: the source drops a field it contributed
	//    before and no other source contributes it. (Another source
	//    not mentioning the field is never a removal.)
	if reportedBefore {
		incoming := map[string]bool{}
		for _, f := range spec.Fields {
			incoming[f.Name] = true
		}
		for _, of := range prev.Fields {
			if incoming[of.Name] {
				continue
			}
			if contributedByOther(contribs, spec.Source, of.Name) {
				continue
			}
			res.conflicts = append(res.conflicts, Conflict{
				Source: spec.Source,
				Field:  of.Name,
				Kind:   conflictFieldRemoved,
				Detail: "report no longer includes the field and no other source contributes it",
			})
		}
	}

	// 5. Fold incoming fields into the merged field set.
	mergedFieldList := cloneFields(currentMerged.Fields)
	var added []string
	for _, nf := range spec.Fields {
		mf, ok := mergedFields[nf.Name]
		switch {
		case !ok:
			if nf.Required {
				res.conflicts = append(res.conflicts, Conflict{
					Source: spec.Source,
					Field:  nf.Name,
					Kind:   conflictRequiredAdded,
					Detail: "new field is declared required",
				})
				continue
			}
			added = append(added, nf.Name)
			mergedFieldList = append(mergedFieldList, nf)
			mergedFields[nf.Name] = nf
		case nf.Type != mf.Type:
			res.conflicts = append(res.conflicts, Conflict{
				Source: spec.Source,
				Field:  nf.Name,
				Kind:   conflictTypeChanged,
				Detail: "source " + spec.Source + " reports type " + string(nf.Type) +
					" but merged state has " + string(mf.Type),
			})
		case nf.Required && !mf.Required:
			res.conflicts = append(res.conflicts, Conflict{
				Source: spec.Source,
				Field:  nf.Name,
				Kind:   conflictRequiredAdded,
				Detail: "optional field tightened to required",
			})
		}
	}
	if len(res.conflicts) > 0 {
		return res
	}

	// 6. Union owners/tags across all contributions (incoming
	//    replaces this source's previous report).
	owners, tags := map[string]struct{}{}, map[string]struct{}{}
	for src, cs := range contribs {
		if src == spec.Source {
			continue // replaced by the incoming report
		}
		for _, o := range cs.Owners {
			owners[o] = struct{}{}
		}
		for _, t := range cs.Tags {
			tags[t] = struct{}{}
		}
	}
	for _, o := range spec.Owners {
		owners[o] = struct{}{}
	}
	for _, t := range spec.Tags {
		tags[t] = struct{}{}
	}

	merged := EntitySpec{
		Name:   currentMerged.Name,
		Kind:   currentMerged.Kind,
		Fields: mergedFieldList,
	}
	// Description: latest non-empty report wins; empty keeps current.
	if spec.Description != "" {
		merged.Description = spec.Description
	} else {
		merged.Description = currentMerged.Description
	}
	merged.Owners = sortedKeys(owners)
	merged.Tags = sortedKeys(tags)
	res.merged = merged

	// 7. Change classification (merged spec vs. previous merged spec).
	var changedParts []string
	if merged.Description != currentMerged.Description {
		changedParts = append(changedParts, "description")
	}
	if !sameStringSet(merged.Owners, currentMerged.Owners) {
		changedParts = append(changedParts, "owners")
	}
	if !sameStringSet(merged.Tags, currentMerged.Tags) {
		changedParts = append(changedParts, "tags")
	}
	if len(added) > 0 {
		changedParts = append(changedParts, "fields added: "+joinSorted(added))
	}
	// Attribution changes even when the merged spec does not: a new
	// source appears, or a source starts/stops contributing a field,
	// owner or tag name.
	attribChanged := !reportedBefore ||
		!sameStringSet(fieldNameList(prev), fieldNameList(spec)) ||
		!sameStringSet(prev.Owners, spec.Owners) ||
		!sameStringSet(prev.Tags, spec.Tags)
	res.changed = len(changedParts) > 0 || attribChanged
	if res.changed && len(changedParts) == 0 {
		changedParts = append(changedParts, "source "+spec.Source+" attested the current state")
	}
	sort.Strings(changedParts)
	res.reason = strings.Join(changedParts, "; ")
	return res
}

// fieldNameList returns the field names of a spec as a slice.
func fieldNameList(s EntitySpec) []string {
	out := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		out[i] = f.Name
	}
	return out
}

// contributedByOther reports whether any source other than src
// contributes field name.
func contributedByOther(contribs map[string]EntitySpec, src, name string) bool {
	for s, cs := range contribs {
		if s == src {
			continue
		}
		for _, f := range cs.Fields {
			if f.Name == name {
				return true
			}
		}
	}
	return false
}

// specEquals reports whether two specs from the same source are
// semantically identical (order of fields/owners/tags insignificant).
func specEquals(a, b EntitySpec) bool {
	if a.Name != b.Name || a.Source != b.Source || a.Kind != b.Kind ||
		a.Description != b.Description {
		return false
	}
	if !sameStringSet(a.Owners, b.Owners) || !sameStringSet(a.Tags, b.Tags) ||
		!sameStringSet(a.RemovedFields, b.RemovedFields) {
		return false
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	bf := map[string]FieldSpec{}
	for _, f := range b.Fields {
		bf[f.Name] = f
	}
	for _, f := range a.Fields {
		other, ok := bf[f.Name]
		if !ok || other != f {
			return false
		}
	}
	return true
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
