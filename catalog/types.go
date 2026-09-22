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

// EntitySpec is the full specification of an entity as reported by a
// producer (a source at a point in time).
type EntitySpec struct {
	Name        string      `json:"name"`
	Source      string      `json:"source"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
}

// Entity is the catalog's current view of a registered entity.
type Entity struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Source      string      `json:"source"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Owners      []string    `json:"owners"`
	Tags        []string    `json:"tags"`
	Fields      []FieldSpec `json:"fields"`
	Version     int         `json:"version"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// EntityVersion is one immutable point in the version history of an
// entity.
type EntityVersion struct {
	Version   int        `json:"version"`
	Spec      EntitySpec `json:"spec"`
	CreatedAt time.Time  `json:"created_at"`
	Reason    string     `json:"reason"`
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
