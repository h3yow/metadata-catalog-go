package catalog

import (
	"maps"
	"slices"
	"strings"
)

// AddLineage 登记一条血缘关系：upstream 的产出流向 downstream。
//   - 两端实体必须已登记，且不能相同（ReasonSelfLoop）。
//   - 同一 (upstream, downstream) 关系重复登记时返回 Duplicate=true，
//     既不报错也不覆盖已有边的来源与属性。
func (c *Catalog) AddLineage(upstream, downstream, source string, props map[string]string) (*LineageResult, error) {
	upstream = strings.TrimSpace(upstream)
	downstream = strings.TrimSpace(downstream)
	if upstream == "" || downstream == "" {
		return nil, reject(ReasonInvalidInput, "lineage requires both upstream and downstream names")
	}
	if upstream == downstream {
		return nil, reject(ReasonSelfLoop, "entity %q cannot be its own upstream/downstream", upstream)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entities[upstream]; !ok {
		return nil, reject(ReasonEntityNotFound, "upstream entity %q is not registered", upstream)
	}
	if _, ok := c.entities[downstream]; !ok {
		return nil, reject(ReasonEntityNotFound, "downstream entity %q is not registered", downstream)
	}

	if m := c.outEdges[upstream]; m != nil {
		if _, exists := m[downstream]; exists {
			return &LineageResult{Upstream: upstream, Downstream: downstream, Accepted: false, Duplicate: true}, nil
		}
	}

	edge := &Edge{
		Upstream:   upstream,
		Downstream: downstream,
		Source:     source,
		Properties: maps.Clone(props),
		CreatedAt:  c.now(),
	}
	if edge.Properties == nil {
		edge.Properties = map[string]string{}
	}
	if c.outEdges[upstream] == nil {
		c.outEdges[upstream] = map[string]*Edge{}
	}
	if c.inEdges[downstream] == nil {
		c.inEdges[downstream] = map[string]*Edge{}
	}
	c.outEdges[upstream][downstream] = edge
	c.inEdges[downstream][upstream] = edge
	return &LineageResult{Upstream: upstream, Downstream: downstream, Accepted: true, Duplicate: false}, nil
}

// Downstream 返回 name 的全部传递下游（影响范围）。
// 遍历语义：沿“upstream -> downstream”方向做有向图 BFS，
// 用 visited 集合终止环路，多路径只计一次；起点不包含在结果中。
// 邻接层按名称排序后扩展，最终结果按名称排序，保证可复现。
func (c *Catalog) Downstream(name string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.entities[name]; !ok {
		return nil, reject(ReasonEntityNotFound, "entity %q is not registered", name)
	}
	return traverse(name, c.outEdges), nil
}

// Upstream 返回 name 的全部传递上游（数据来源范围），
// 沿 downstream -> upstream 反向 BFS，去重与排序语义同 Downstream。
func (c *Catalog) Upstream(name string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.entities[name]; !ok {
		return nil, reject(ReasonEntityNotFound, "entity %q is not registered", name)
	}
	return traverse(name, c.inEdges), nil
}

// traverse 自起点对邻接表做去环 BFS，返回排序后的可达节点（不含起点）。
func traverse(start string, adj map[string]map[string]*Edge) []string {
	visited := map[string]struct{}{start: {}}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		nexts := make([]string, 0, len(adj[cur]))
		for n := range adj[cur] {
			if _, seen := visited[n]; !seen {
				nexts = append(nexts, n)
			}
		}
		slices.Sort(nexts)
		for _, n := range nexts {
			visited[n] = struct{}{}
			queue = append(queue, n)
		}
	}
	out := make([]string, 0, len(visited)-1)
	for n := range visited {
		if n != start {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}
