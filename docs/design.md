# 元数据目录设计说明

## 1. 数据模型

| 概念 | 标识 | 关键字段 | 说明 |
| --- | --- | --- | --- |
| 实体 Entity | `name`（逻辑名，全局唯一） | `kind`、`version`、`fields`、`tags`、`owners`、`properties`、`sources` | 一次登记产生的当前快照；检索返回当前状态的深拷贝 |
| 字段 Field | 实体名 + `field.name` | `type`、`semantic`、`optional`、`sinceVersion` | 字段类型与语义标识是判定兼容性的核心 |
| 版本 EntityVersion | `version`（实体内单调递增） | 登记时的完整规范化载荷、来源、时间戳 | 只追加、永不重写；删除/类型变更等被拒绝的上报不产生版本 |
| 血缘 Edge | `(upstream, downstream)` 有序对 | `source`、`properties`、`createdAt` | 同一有序对只保留首次登记的边 |

- **来源（source）**：每次上报都带 `source`。同一实体被多个来源上报时，来源按首次出现顺序并入 `sources`，不作为实体身份的一部分，也不会因为来源不同而产生重复实体。
- **载荷规范化**：登记前字段按名称排序、标签/负责人去重排序、属性复制。规范化仅用于比较与存储，保证“字段顺序不同”不被误判为变更。

## 2. 登记与演进的判定规则

对 `Register(spec)` 的判定顺序固定，任一检查失败则**整体拒绝、零写入**（不会留下半次演进）：

1. 输入校验：实体名非空；同一载荷内字段名唯一且类型非空。否则 `invalid_input`。
2. 实体不存在 → 创建 `version=1`，判定 `created`。
3. `kind` 与现版本不同 → `kind_changed`（不兼容）。
4. 现版本字段在新载荷中缺失 → `field_removed`（不兼容，删字段会破坏既有消费者）。
5. 新增字段若 `optional=false` → `required_field_added`（不兼容；只有新增**可选**字段才允许）。
6. 同名字段 `type` 变化 → `field_type_changed`（不兼容）。
7. 同名字段 `semantic`（业务语义标识）变化 → `field_semantic_changed`（不兼容；字段还在但含义变了是最危险的隐性破坏）。
8. 全部检查通过后比较规范化状态：
   - 字段集合与元数据均无变化 → `noop`：不升版本、不追加历史；仅在来源是首次出现时并入 `sources`。
   - 有新增可选字段，或标签/负责人/属性出现并集增量 → `compatible`：`version+1`，追加一条不可变历史版本。

**重复上报**因此天然幂等：同一载荷上报 N 次，结果是 1 个实体、1 条历史，其余 N-1 次全部 `noop`，历史不会被反复改写。

标签与负责人取**并集**（不允许通过新上报抹掉旧标签），属性按键新增/更新；这些都是兼容性元数据变化。

### 拒绝原因枚举（可断言）

`invalid_input` / `kind_changed` / `field_removed` / `required_field_added` /
`field_type_changed` / `field_semantic_changed` / `entity_not_found` /
`field_not_found` / `self_lineage_loop`（另有保留枚举 `duplicate_conflict`，当前所有冲突都能归入更具体的字段级原因）。Go API 通过 `RejectError.Reason`、HTTP 通过错误体 `reason` 字段暴露。

## 3. 血缘与影响分析的遍历语义

- 边是有向的：`upstream -> downstream` 表示上游产出流入下游。
- 登记约束：两端实体必须存在（否则 `entity_not_found`）；禁止自环 `self_lineage_loop`。
- **重复登记识别**：相同 `(upstream, downstream)` 再次登记时返回 `duplicate=true, accepted=false`，既不报错也不覆盖首次登记边的 `source/properties`。
- **遍历**：`Downstream(name)` 沿正向、`Upstream(name)` 沿反向做 **BFS**。
  - `visited` 集合在节点入队时即标记，因此**环状关系不会无限递归**，环上回边被直接截断；
  - **多路径（菱形等）只计一次**，起点本身不计入结果；
  - 每层邻接节点按名称排序后扩展，最终结果按名称排序 → 对同一图状态，结果稳定、可复现，与 map 迭代的随机顺序无关。
- 查询的是调用时刻的图快照（读锁内完成）。

## 4. 并发一致性

`Catalog` 内部用一把 `sync.RWMutex`：

- 所有写操作（Register / AddLineage）持写锁，且“校验 + 判定 + 写入”在同一临界区内原子完成 → 不存在两个上报同时判定为 `created`，也不存在丢失更新；
- 读操作（Get / History / Search / Downstream / Upstream）持读锁并在锁内完成深拷贝 → 调用方永远不会看到半次演进（例如版本号已升但字段未加完）或被后续写入改写的切片/map；
- 因此检索结果与当前最新状态一致，结论不会自相矛盾。`go test -race` 下验证。

## 5. HTTP 接口

| 方法与路径 | 含义 |
| --- | --- |
| `POST /v1/entities` | 登记/演进实体；`created`→201，`noop/compatible`→200，拒绝→4xx |
| `GET /v1/entities/{name}` | 当前快照 |
| `GET /v1/entities/{name}/history` | 历史版本（版本号升序） |
| `GET /v1/search?keyword=&tag=&owner=&kind=` | 检索；tag 可重复传（AND），owner 命中任一 |
| `POST /v1/lineage` | 登记血缘，重复时返回 `duplicate:true` |
| `GET /v1/lineage/downstream/{name}` / `upstream/{name}` | 影响范围 / 来源范围 |

错误体统一为 `{"reason":"<稳定枚举>","detail":"<说明>"}`：业务拒绝为 400，不存在为 404，JSON/字段非法为 422。

## 6. 本地验证

```bash
go test ./...                 # 全部单测
go test ./... -race           # 含并发竞态检测
go test ./... -v              # 查看每个用例打印的“输入 => 判定依据”
go run ./cmd/server -addr 127.0.0.1:8080
```

测试覆盖：重复上报（同/异来源）、新增可选字段、新增必填字段、删字段、改类型、改语义、改 kind、重复血缘、自环、环状 + 多路径血缘、菱形去重、关键字/标签/负责人检索、50 路并发重复上报、30 路并发无丢失更新、100 次累积演进期间的读一致性。
