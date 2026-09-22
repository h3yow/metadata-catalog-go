package catalog

import "time"

// FieldSpec 是调用方上报的单个字段定义。
type FieldSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"`     // 逻辑数据类型，如 string / int64
	Semantic string `json:"semantic"` // 业务语义标识，如 "user.id"
	Optional bool   `json:"optional"`
}

// Field 是字段在目录中的当前状态。
type Field struct {
	FieldSpec
	SinceVersion int       `json:"sinceVersion"` // 该定义首次出现的版本
	UpdatedAt    time.Time `json:"updatedAt"`
}

// EntitySpec 是调用方上报/登记实体时的完整载荷。
type EntitySpec struct {
	Name       string            `json:"name"`   // 逻辑名，实体身份
	Source     string            `json:"source"` // 上报来源标识
	Kind       string            `json:"kind"`   // 实体种类，如 table / topic
	Fields     []FieldSpec       `json:"fields"`
	Tags       []string          `json:"tags"`
	Owners     []string          `json:"owners"`
	Properties map[string]string `json:"properties"`
}

// ChangeKind 描述一次版本演进的结果类型。
type ChangeKind string

const (
	ChangeCreated    ChangeKind = "created"    // 首次登记
	ChangeNoop       ChangeKind = "noop"       // 完全重复上报，无任何变化
	ChangeCompatible ChangeKind = "compatible" // 兼容演进（如新增可选字段）
)

// FieldChange 描述单个字段层面的差异。
type FieldChange struct {
	Field string `json:"field"`
	Kind  string `json:"kind"` // added / changed
	What  string `json:"what"` // 具体差异说明
}

// RegisterResult 是一次登记的判定结果。
type RegisterResult struct {
	Name         string        `json:"name"`
	Version      int           `json:"version"`      // 判定后实体的当前版本
	Change       ChangeKind    `json:"change"`       // 本次上报的判定
	Changed      []FieldChange `json:"changed"`      // 兼容变更明细
	FirstSource  string        `json:"firstSource"`  // 首次登记来源
	RepeatReport bool          `json:"repeatReport"` // 是否为重复来源的再次上报
}

// Entity 是实体在目录中的当前快照（检索返回它）。
type Entity struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Version    int               `json:"version"`
	Sources    []string          `json:"sources"` // 去重后的全部上报来源（有序）
	Fields     []Field           `json:"fields"`  // 按名称排序
	Tags       []string          `json:"tags"`    // 去重有序
	Owners     []string          `json:"owners"`  // 去重有序
	Properties map[string]string `json:"properties"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// EntityVersion 保留一次历史版本的不可变快照。
type EntityVersion struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"` // 产生该版本的上报来源
	Spec      EntitySpec `json:"spec"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Edge 是一条血缘关系：Upstream 产出的数据流向 Downstream。
type Edge struct {
	Upstream   string            `json:"upstream"`
	Downstream string            `json:"downstream"`
	Source     string            `json:"source"` // 登记该关系的来源
	Properties map[string]string `json:"properties"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// LineageResult 是血缘登记的判定结果。
type LineageResult struct {
	Upstream   string `json:"upstream"`
	Downstream string `json:"downstream"`
	Accepted   bool   `json:"accepted"`
	Duplicate  bool   `json:"duplicate"` // 已存在同一关系
}

// Query 描述实体检索条件（各条件之间为 AND）。
type Query struct {
	Keyword string   // 关键字，匹配名称/字段名/属性值（大小写不敏感）
	Tags    []string // 实体须包含全部给定标签
	Owners  []string // 负责人命中任一即可
	Kind    string   // 可选，按种类过滤
}
