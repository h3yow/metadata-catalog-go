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
// producer (a source at a point in time). A report is *partial*: it
// only covers the fields/owners/tags that this source cares about.
// The catalog merges reports from all sources of the same logical
// entity into one record.
type EntitySpec struct {
	Name        string      `json:"name"`
	Source      string      `json:"source"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
	// DropFields explicitly asks to remove fields from the entity.
	// Field removal is a breaking change, so any non-empty DropFields
	// rejects the whole report with a field_removed conflict. A field
	// that is merely absent from Fields is NOT treated as dropped
	// ("not mentioned" != "deleted").
	DropFields []string `json:"drop_fields,omitempty"`
}

// Entity is the catalog's current view of a registered entity: the
// merged state across all contributing sources.
type Entity struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
	// Sources lists every source that currently contributes to this
	// entity, sorted alphabetically.
	Sources []string `json:"sources"`
	// FieldSources maps a field name to the sources that report it.
	FieldSources map[string][]string `json:"field_sources,omitempty"`
	// OwnerSources maps an owner to the sources that report it.
	OwnerSources map[string][]string `json:"owner_sources,omitempty"`
	// TagSources maps a tag to the sources that report it.
	TagSources map[string][]string `json:"tag_sources,omitempty"`

	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EntityVersion is one immutable point in the version history of an
// entity.
type EntityVersion struct {
	Version int `json:"version"`
	// Source is the source whose report produced this version.
	Source string `json:"source"`
	// Spec is the merged state right after this version was committed.
	Spec      EntitySpec `json:"spec"`
	CreatedAt time.Time  `json:"created_at"`
	Reason    string     `json:"reason"`
}

// SourceContribution is the last accepted report of one source for
// one entity, together with the time it was committed. Re-reporting
// identical content is rejected as a duplicate and neither rewrites
// the contribution nor refreshes ReportedAt.
type SourceContribution struct {
	Source     string     `json:"source"`
	Spec       EntitySpec `json:"spec"`
	ReportedAt time.Time  `json:"reported_at"`
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
