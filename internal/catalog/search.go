package catalog

import (
	"slices"
	"strings"
)

// Search 按 Query 检索实体（各条件之间为 AND）：
//   - Tags：实体必须包含全部给定标签；
//   - Owners：负责人命中任一即可；
//   - Kind：非空时要求完全相等；
//   - Keyword：大小写不敏感地匹配实体名、字段名、字段语义标识与属性值。
//
// 检索在 RLock 下对当前快照进行，返回的均为深拷贝并按名称排序，
// 因此与调用时刻的最新状态一致，且不会暴露后续写入造成的中间状态。
func (c *Catalog) Search(q Query) []Entity {
	c.mu.RLock()
	defer c.mu.RUnlock()

	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	out := make([]Entity, 0)
	for _, st := range c.entities {
		e := st.current
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if !containsAll(e.Tags, q.Tags) {
			continue
		}
		if len(q.Owners) > 0 && !containsAny(e.Owners, q.Owners) {
			continue
		}
		if kw != "" && !keywordHits(&e, kw) {
			continue
		}
		out = append(out, cloneEntity(&e))
	}
	slices.SortFunc(out, func(a, b Entity) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

func containsAny(have, any []string) bool {
	for _, w := range any {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}

func keywordHits(e *Entity, kw string) bool {
	if strings.Contains(strings.ToLower(e.Name), kw) {
		return true
	}
	for _, f := range e.Fields {
		if strings.Contains(strings.ToLower(f.Name), kw) ||
			strings.Contains(strings.ToLower(f.Semantic), kw) {
			return true
		}
	}
	for _, v := range e.Properties {
		if strings.Contains(strings.ToLower(v), kw) {
			return true
		}
	}
	return false
}
