// Package catalog implements a local metadata catalog service.
//
// It maintains data entities, their fields, version history and lineage
// relations in local in-process storage.
package catalog

import "time"

// FieldType is the data type of a field.
type FieldType string

// Supported field types.
const (
	TypeString FieldType = "string"
	TypeInt    FieldType = "int"
	TypeFloat  FieldType = "float"
	TypeBool   FieldType = "bool"
	TypeDate   FieldType = "date"
	TypeJSON   FieldType = "json"
)

// FieldSpec is the declared specification of a single field as reported
// by a producer.
type FieldSpec struct {
	Name        string    `json:"name"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Description string    `json:"description"`
}

// EntitySpec is the specification of an entity as reported by one
// producer (a source at a point in time). A report only needs to
// carry the part of the entity the source cares about; reports from
// all sources are merged into a single logical entity.
type EntitySpec struct {
	Name        string      `json:"name"`
	Source      string      `json:"source"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
	// RemovedFields explicitly names fields the source wants gone.
	// Any non-empty entry is rejected with a field_removed conflict:
	// removal is a breaking change and must be coordinated, a single
	// source cannot unilaterally delete. Simply omitting a field from
	// Fields is NOT a removal.
	RemovedFields []string `json:"removed_fields,omitempty"`
}

// Entity is the catalog's current view of a registered entity. It is
// the merged state folded from the reports of all contributing
// sources; the attribution maps record which source(s) contributed
// each field, owner and tag.
type Entity struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
	// Sources lists every source that currently contributes to this
	// entity, sorted.
	Sources []string `json:"sources"`
	// FieldSources maps field name -> contributing sources (sorted).
	FieldSources map[string][]string `json:"field_sources,omitempty"`
	// OwnerSources maps owner -> sources that listed it (sorted).
	OwnerSources map[string][]string `json:"owner_sources,omitempty"`
	// TagSources maps tag -> sources that listed it (sorted).
	TagSources map[string][]string `json:"tag_sources,omitempty"`
	Version     int         `json:"version"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// EntityVersion is one immutable point in the version history of an
// entity. Spec is the merged state after the triggering report was
// applied; Source identifies which source's report produced it.
type EntityVersion struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"`
	Spec      EntitySpec `json:"spec"`
	CreatedAt time.Time  `json:"created_at"`
	Reason    string     `json:"reason"`
}

// SourceReport is one accepted report in an entity's per-source
// report log. Rejected and duplicate reports are never logged, so the
// log never piles up with identical re-reports.
type SourceReport struct {
	Source     string     `json:"source"`
	Spec       EntitySpec `json:"spec"`
	ReportedAt time.Time  `json:"reported_at"`
	// Version is the entity version this report produced.
	Version int `json:"version"`
}

// LineageEdge represents one directed producer -> consumer dependency.
type LineageEdge struct {
	UpstreamID   string    `json:"upstream_id"`
	DownstreamID string    `json:"downstream_id"`
	Relation     string    `json:"relation"`
	RegisteredAt time.Time `json:"registered_at"`
}

// ImpactReport is the result of an impact / lineage analysis starting
// from one entity.
type ImpactReport struct {
	// Root is the entity ID from which the analysis started.
	Root string `json:"root"`
	// Direction is "downstream" or "upstream".
	Direction string `json:"direction"`
	// EntityIDs is the ordered list of affected entities. The root is
	// not included. The order is deterministic (BFS, ties broken by
	// entity ID) so the same graph always yields the same report.
	EntityIDs []string `json:"entity_ids"`
	// Edges are the traversed edges in traversal order; each edge is
	// reported at most once even when the graph contains cycles or
	// multiple paths.
	Edges []LineageEdge `json:"edges"`
}

// SearchQuery filters entities by current-state attributes.
type SearchQuery struct {
	Keyword string
	Tag     string
	Owner   string
}
