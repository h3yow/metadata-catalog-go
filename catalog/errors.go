package catalog

import "fmt"

// Reason is a machine-readable, distinguishable failure reason.
type Reason string

const (
	// ReasonInvalidSpec means the reported spec is malformed (empty
	// name, duplicate fields, unknown field type, ...).
	ReasonInvalidSpec Reason = "invalid_spec"
	// ReasonEntityNotFound means the referenced entity does not exist.
	ReasonEntityNotFound Reason = "entity_not_found"
	// ReasonIncompatibleChange means the new spec removes fields,
	// changes field semantics or introduces other breaking changes.
	ReasonIncompatibleChange Reason = "incompatible_change"
	// ReasonDuplicateReport means the reported spec is identical to the
	// current state; it is recorded nowhere and history is untouched.
	ReasonDuplicateReport Reason = "duplicate_report"
	// ReasonVersionConflict means a RegisterAt expected-version check
	// failed: another writer committed in between. Callers must
	// re-read, merge and retry; no update was applied.
	ReasonVersionConflict Reason = "version_conflict"
	// ReasonDuplicateEdge means the lineage relation was already
	// registered.
	ReasonDuplicateEdge Reason = "duplicate_edge"
	// ReasonSelfEdge means upstream and downstream are the same entity.
	ReasonSelfEdge Reason = "self_edge"
)

// Conflict describes one concrete difference that makes an evolution
// or a cross-source merge incompatible. It lets callers assert *why*
// a report was rejected and *who* to talk to.
type Conflict struct {
	// Source is the reporting source responsible for the conflict.
	Source string `json:"source,omitempty"`
	Field  string `json:"field"`
	Kind   string `json:"kind"` // "field_removed", "type_changed", "required_added", "semantic_changed"
	Detail string `json:"detail"`
}

// CatalogError is the structured error returned by all catalog
// operations. Errors can be compared by Reason and, for evolution
// failures, inspected via Conflicts.
type CatalogError struct {
	Reason    Reason
	Message   string
	Conflicts []Conflict
}

func (e *CatalogError) Error() string {
	if len(e.Conflicts) == 0 {
		return string(e.Reason) + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s (%d conflict(s))", e.Reason, e.Message, len(e.Conflicts))
}

func errf(r Reason, format string, args ...any) *CatalogError {
	return &CatalogError{Reason: r, Message: fmt.Sprintf(format, args...)}
}

// AsCatalogError extracts a *CatalogError from err.
func AsCatalogError(err error) (*CatalogError, bool) {
	ce, ok := err.(*CatalogError)
	return ce, ok
}
