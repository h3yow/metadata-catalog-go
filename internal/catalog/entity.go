package catalog

import (
	"maps"
	"slices"
	"strings"
)

// ---------- 输入校验与规范化 ----------

func validateSpec(spec EntitySpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return reject(ReasonInvalidInput, "entity name is required")
	}
	seen := map[string]struct{}{}
	for _, f := range spec.Fields {
		if strings.TrimSpace(f.Name) == "" {
			return reject(ReasonInvalidInput, "entity %s has a field with empty name", spec.Name)
		}
		if strings.TrimSpace(f.Type) == "" {
			return reject(ReasonInvalidInput, "field %s of entity %s requires a type", f.Name, spec.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return reject(ReasonInvalidInput, "entity %s declares field %s more than once in one report", spec.Name, f.Name)
		}
		seen[f.Name] = struct{}{}
	}
	return nil
}

// normalizeSpec 复制并规范化载荷：字段按名称排序，标签/负责人去重排序，属性复制。
func normalizeSpec(spec EntitySpec) EntitySpec {
	out := spec
	out.Fields = append([]FieldSpec(nil), spec.Fields...)
	slices.SortFunc(out.Fields, func(a, b FieldSpec) int { return strings.Compare(a.Name, b.Name) })
	out.Tags = dedupSorted(spec.Tags)
	out.Owners = dedupSorted(spec.Owners)
	out.Properties = maps.Clone(spec.Properties)
	if out.Properties == nil {
		out.Properties = map[string]string{}
	}
	return out
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := append([]string(nil), in...)
	slices.Sort(out)
	return slices.Compact(out)
}

// ---------- 登记 / 演进 ----------

// Register 登记或演进一个实体。判定规则：
//   - 实体不存在：创建版本 1。
//   - 规范化后完全相同的载荷重复上报：noop，不新增版本、不改写历史。
//   - 新增字段：仅当字段 optional 时属于兼容演进，版本 +1；
//     新增必填字段以 ReasonRequiredFieldAdded 拒绝。
//   - 删除已登记字段：ReasonFieldRemoved 拒绝。
//   - 字段类型变化：ReasonFieldTypeChanged 拒绝。
//   - 字段语义标识变化：ReasonFieldSemanticChanged 拒绝。
//   - 实体 kind 变化：ReasonKindChanged 拒绝。
//
// 标签/负责人按并集合并、属性新增键；历史版本只追加、永不重写。
func (c *Catalog) Register(spec EntitySpec) (*RegisterResult, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	spec = normalizeSpec(spec)

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	st, ok := c.entities[spec.Name]
	if !ok {
		ent := Entity{
			Name:       spec.Name,
			Kind:       spec.Kind,
			Version:    1,
			Sources:    []string{spec.Source},
			Tags:       slices.Clone(spec.Tags),
			Owners:     slices.Clone(spec.Owners),
			Properties: maps.Clone(spec.Properties),
			CreatedAt:  now,
			UpdatedAt:  now,
			Fields:     make([]Field, 0, len(spec.Fields)),
		}
		for _, f := range spec.Fields {
			ent.Fields = append(ent.Fields, Field{FieldSpec: f, SinceVersion: 1, UpdatedAt: now})
		}
		c.entities[spec.Name] = &entityState{
			current:  ent,
			versions: []EntityVersion{{Version: 1, Source: spec.Source, Spec: cloneSpec(spec), CreatedAt: now}},
		}
		return &RegisterResult{
			Name: spec.Name, Version: 1, Change: ChangeCreated,
			FirstSource: spec.Source,
		}, nil
	}

	// 实体已存在：先做全部不兼容检查，任一失败则整体拒绝（不产生部分写入）。
	if st.current.Kind != spec.Kind {
		return nil, reject(ReasonKindChanged,
			"entity %s kind changed: %q -> %q", spec.Name, st.current.Kind, spec.Kind)
	}
	curFields := map[string]Field{}
	for _, f := range st.current.Fields {
		curFields[f.Name] = f
	}
	inFields := map[string]FieldSpec{}
	for _, f := range spec.Fields {
		inFields[f.Name] = f
	}
	for name := range curFields {
		if _, present := inFields[name]; !present {
			return nil, reject(ReasonFieldRemoved,
				"entity %s report drops registered field %q; removing fields is incompatible", spec.Name, name)
		}
	}
	for name, in := range inFields {
		cur, exists := curFields[name]
		if !exists {
			if !in.Optional {
				return nil, reject(ReasonRequiredFieldAdded,
					"entity %s adds required field %q; only optional fields can be added compatibly", spec.Name, name)
			}
			continue
		}
		if cur.Type != in.Type {
			return nil, reject(ReasonFieldTypeChanged,
				"entity %s field %q type changed: %q -> %q", spec.Name, name, cur.Type, in.Type)
		}
		if cur.Semantic != in.Semantic {
			return nil, reject(ReasonFieldSemanticChanged,
				"entity %s field %q semantic changed: %q -> %q", spec.Name, name, cur.Semantic, in.Semantic)
		}
	}

	// 全部检查通过：收集新增字段、合并元数据。
	var added []FieldSpec
	for _, f := range spec.Fields {
		if _, ok := curFields[f.Name]; !ok {
			added = append(added, f)
		}
	}
	schemaChanged := len(added) > 0
	metaChanged := mergeMetadata(&st.current, spec)
	knownSource := containsString(st.current.Sources, spec.Source)
	appendUniqueString(&st.current.Sources, spec.Source)

	if !schemaChanged && !metaChanged {
		// 完全重复上报：不动版本与历史。
		return &RegisterResult{
			Name: spec.Name, Version: st.current.Version, Change: ChangeNoop,
			FirstSource: st.current.Sources[0], RepeatReport: knownSource,
		}, nil
	}

	// 兼容演进：任何 canonical 状态变化都使版本 +1，保证历史版本号唯一。
	var changes []FieldChange
	nextVersion := st.current.Version + 1
	for _, f := range added {
		st.current.Fields = append(st.current.Fields,
			Field{FieldSpec: f, SinceVersion: nextVersion, UpdatedAt: now})
		changes = append(changes, FieldChange{Field: f.Name, Kind: "added", What: "optional field added"})
	}
	slices.SortFunc(st.current.Fields, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	st.current.Version = nextVersion
	st.current.UpdatedAt = now

	st.versions = append(st.versions, EntityVersion{
		Version:   nextVersion,
		Source:    spec.Source,
		Spec:      snapshotSpec(&st.current),
		CreatedAt: now,
	})

	return &RegisterResult{
		Name: spec.Name, Version: nextVersion, Change: ChangeCompatible,
		Changed: changes, FirstSource: st.current.Sources[0], RepeatReport: knownSource,
	}, nil
}

// appendUniqueString 追加不存在的来源，返回是否发生追加。
func appendUniqueString(list *[]string, v string) bool {
	for _, x := range *list {
		if x == v {
			return false
		}
	}
	*list = append(*list, v)
	return true
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// mergeMetadata 合并标签/负责人/属性，返回是否有元数据变化。
func mergeMetadata(cur *Entity, spec EntitySpec) bool {
	changed := false
	mergeIntoSorted(&cur.Tags, spec.Tags, &changed)
	mergeIntoSorted(&cur.Owners, spec.Owners, &changed)
	if cur.Properties == nil {
		cur.Properties = map[string]string{}
	}
	for k, v := range spec.Properties {
		if existing, ok := cur.Properties[k]; !ok {
			cur.Properties[k] = v
			changed = true
		} else if existing != v {
			cur.Properties[k] = v
			changed = true
		}
	}
	return changed
}

// mergeIntoSorted 把 b 并入已排序去重的 a，结果仍排序去重。
func mergeIntoSorted(a *[]string, b []string, changed *bool) {
	for _, v := range b {
		if !slices.Contains(*a, v) {
			*a = append(*a, v)
			*changed = true
		}
	}
	slices.Sort(*a)
}

// snapshotSpec 从当前实体状态重建规范化载荷，供历史快照使用。
func snapshotSpec(cur *Entity) EntitySpec {
	spec := EntitySpec{
		Name:       cur.Name,
		Kind:       cur.Kind,
		Tags:       slices.Clone(cur.Tags),
		Owners:     slices.Clone(cur.Owners),
		Properties: maps.Clone(cur.Properties),
		Fields:     make([]FieldSpec, 0, len(cur.Fields)),
	}
	for _, f := range cur.Fields {
		spec.Fields = append(spec.Fields, f.FieldSpec)
	}
	return spec
}

func cloneSpec(s EntitySpec) EntitySpec {
	return EntitySpec{
		Name:       s.Name,
		Source:     s.Source,
		Kind:       s.Kind,
		Fields:     append([]FieldSpec(nil), s.Fields...),
		Tags:       slices.Clone(s.Tags),
		Owners:     slices.Clone(s.Owners),
		Properties: maps.Clone(s.Properties),
	}
}

// ---------- 查询 ----------

// Get 返回实体当前快照的深拷贝；不存在返回 ReasonEntityNotFound。
func (c *Catalog) Get(name string) (*Entity, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.entities[name]
	if !ok {
		return nil, reject(ReasonEntityNotFound, "entity %q is not registered", name)
	}
	ent := cloneEntity(&st.current)
	return &ent, nil
}

// History 返回实体历史版本的深拷贝（版本号升序）。
func (c *Catalog) History(name string) ([]EntityVersion, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.entities[name]
	if !ok {
		return nil, reject(ReasonEntityNotFound, "entity %q is not registered", name)
	}
	out := make([]EntityVersion, len(st.versions))
	for i := range st.versions {
		vv := st.versions[i]
		vv.Spec = cloneSpec(vv.Spec)
		out[i] = vv
	}
	return out, nil
}

func cloneEntity(e *Entity) Entity {
	cp := *e
	cp.Fields = append([]Field(nil), e.Fields...)
	cp.Tags = slices.Clone(e.Tags)
	cp.Owners = slices.Clone(e.Owners)
	cp.Sources = slices.Clone(e.Sources)
	cp.Properties = maps.Clone(e.Properties)
	return cp
}
