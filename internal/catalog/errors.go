package catalog

import "fmt"

// RejectReason 以可区分、可断言的方式描述登记/演进被拒绝的原因。
type RejectReason string

const (
	// ReasonDuplicateConflict 表示重复上报但载荷与现有版本不一致，
	// 且差异不属于允许的兼容演进。
	ReasonDuplicateConflict RejectReason = "duplicate_conflict"
	// ReasonFieldRemoved 表示试图删除一个已经登记的字段（不兼容变更）。
	ReasonFieldRemoved RejectReason = "field_removed"
	// ReasonFieldTypeChanged 表示字段的数据类型被改变（不兼容变更）。
	ReasonFieldTypeChanged RejectReason = "field_type_changed"
	// ReasonFieldSemanticChanged 表示字段语义（业务含义）被改变（不兼容变更）。
	ReasonFieldSemanticChanged RejectReason = "field_semantic_changed"
	// ReasonRequiredFieldAdded 表示试图新增一个必填字段（仅允许新增可选字段）。
	ReasonRequiredFieldAdded RejectReason = "required_field_added"
	// ReasonKindChanged 表示实体种类被改变（不兼容变更）。
	ReasonKindChanged RejectReason = "kind_changed"
	// ReasonEntityNotFound 表示引用的实体不存在。
	ReasonEntityNotFound RejectReason = "entity_not_found"
	// ReasonFieldNotFound 表示引用的字段不存在。
	ReasonFieldNotFound RejectReason = "field_not_found"
	// ReasonSelfLoop 表示血缘边的上下游为同一个实体。
	ReasonSelfLoop RejectReason = "self_lineage_loop"
	// ReasonInvalidInput 表示输入本身不合法（缺名称、字段定义非法等）。
	ReasonInvalidInput RejectReason = "invalid_input"
)

// RejectError 携带稳定的机器可读原因与人类可读的说明，
// 方便调用方按 RejectReason 做断言。
type RejectError struct {
	Reason RejectReason
	Detail string
}

func (e *RejectError) Error() string {
	return fmt.Sprintf("catalog rejected: reason=%s detail=%s", e.Reason, e.Detail)
}

func reject(reason RejectReason, format string, args ...any) *RejectError {
	return &RejectError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// AsRejectError 从任意 error 中提取 *RejectError。
func AsRejectError(err error) (*RejectError, bool) {
	if err == nil {
		return nil, false
	}
	if re, ok := err.(*RejectError); ok {
		return re, true
	}
	return nil, false
}
