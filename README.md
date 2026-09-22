# 本地元数据目录服务 (metadata-catalog-go)

一套本地（进程内）运行的元数据目录，用内存存储维护数据**实体、字段、版本与血缘关系**，
解决多来源重复上报、结构演进与关系变更的一致性与可解释性问题。

无外部依赖，`go run` / `go test` 即可验证。

## 1. 数据模型

| 类型 | 说明 |
| --- | --- |
| `EntitySpec` | 上报方提交的完整实体规格：`Name`、`Source`、`Kind`、`Description`、`Owners`、`Tags`、`Fields[]` |
| `FieldSpec` | 字段规格：`Name`、`Type`（string/int/float/bool/date/json）、`Required`、`Description` |
| `Entity` | 目录中实体的**当前状态**，含稳定 ID、单调递增的 `Version`、创建/更新时间 |
| `EntityVersion` | 一条**不可变**历史：版本号、当时的完整 Spec、时间戳、变更原因 |
| `LineageEdge` | 一条有向血缘边：`UpstreamID -> DownstreamID`（生产者 -> 消费者）+ `Relation` |
| `ImpactReport` | 影响分析结果：起点、方向、去重且有序的实体列表、实际经过的边 |

### 稳定身份与幂等

- 实体 ID 由 `Source + "/" + Name` 确定性生成。同一 (source, name) 永远指向同一实体，
  因此重复上报不会产生新记录。
- 血缘边以 `UpstreamID + NUL + DownstreamID` 为唯一键。

## 2. 登记与版本演进规则

`Register(spec)`（以及带乐观锁的 `RegisterAt(spec, expectedVersion)`）的判定顺序：

1. **结构校验**：名称/来源非空、字段不重名、类型合法，否则
   `invalid_spec`（不接触任何状态）。
2. **重复上报**：新规格与当前版本语义完全一致（字段/Owner/标签的**顺序无关**），
   返回 `duplicate_report`，**不新增版本、不改写历史、不更新时间戳**。
3. **不兼容演进**：只要存在下列任一冲突，整体拒绝，返回
   `incompatible_change` 且携带可断言的 `Conflicts[]`，当前版本保持不变：

   | 冲突 kind | 触发条件 |
   | --- | --- |
   | `field_removed` | 旧版本中的字段在新规格中消失 |
   | `type_changed` | 同名字段类型变化（语义变更） |
   | `required_added` | 新增必填字段，或可选字段被收紧为必填（旧生产/消费方无法满足） |
   | `semantic_changed` | 实体 `Kind` 发生变化 |

4. **兼容演进**：冲突为空即接受，产生新版本（version + 1）。包括：
   - 新增**可选**字段；
   - 修改描述；
   - 增加/删除 owner、tag；
   - 字段、owner、标签的顺序调整不视为变更（避免仅因顺序不同而反复写历史）。

每次接受都向不可变历史追加一条 `EntityVersion`，`CreatedAt` 始终保留首次登记时间。

### 错误原因（可区分、可断言）

`invalid_spec` / `entity_not_found` / `incompatible_change` /
`duplicate_report` / `version_conflict` / `duplicate_edge` / `self_edge`，
统一为 `*catalog.CatalogError`，演进失败还可检查其 `Conflicts`。

## 3. 血缘与影响分析的遍历语义

- 边方向为**生产者 -> 消费者**。`Downstream(id)` 沿边向下（变更影响面），
  `Upstream(id)` 沿边向上（上游来源）。
- 采用**迭代式广度优先遍历 (BFS)**：
  - 起点在遍历前即标记为已访问，因此**环**（包括回到起点的环）不会重复入队，
    也没有递归，最坏情况只访问实体数次；
  - **多路径**汇合到同一实体时，该实体只报告一次（先到先得），每条不同的边报告一次；
  - 每个节点的邻居按实体 ID 排序后展开，使同一张图无论 Go map 迭代顺序如何，
    都得到**稳定、可复现**的实体顺序；
  - 结果中的 `EntityIDs` 不含起点本身。
- `AddLineage`：自环返回 `self_edge`；端点不存在返回 `entity_not_found`；
  同一对 (up, down) 重复登记返回 `duplicate_edge`，**识别而非静默覆盖**。

## 4. 检索与并发一致性

- `Search(SearchQuery{Keyword, Tag, Owner})` 对非空条件取**交集**；
  关键字小写匹配实体名、描述、kind 与字段名。结果始终来自某个**已提交快照**，
  按实体 ID 排序；删除标签/字段后的检索立即反映最新状态。
- 全库由单把互斥锁保护：每次 `Register`/`AddLineage` 都是"校验 + 提交"原子完成，
  读操作绝不会读到半成品状态，并发更新不会互相覆盖。
- 对"读-改-写"场景提供乐观并发控制 `RegisterAt(spec, expectedVersion)`：
  - `-1` 不检查版本（等同 `Register`）；
  - `0` 仅允许新建（实体已存在则失败）；
  - `>0` 仅当当前版本恰好等于该值时才提交，否则返回
    `version_conflict`，调用方重新读取、合并、重试，从而杜绝丢失更新。

## 5. 验证方法

```bash
# 全部单元测试（含重复上报、不兼容演进、环状/多路径血缘、并发更新）
go test ./... -race -count=1 -v

# 端到端演示
go run ./cmd/demo
```

测试设计要点：

- 失败一律断言具体 `Reason`，演进失败进一步断言具体 `Conflict{Field, Kind}`；
- 每个用例通过 `t.Logf` 打印**输入**与**最终判定依据**（duplicate/changed/conflicts/reason），
  用 `-v` 可直接查看；
- 血缘测试构造菱形 + 回环图，断言去重、防环、边不重复以及 50 次重复运行顺序一致；
- 并发测试：20 个 goroutine 基于 CAS 各加一个字段，最终版本与字段数精确可校验；
  另有 1 写 4 读持续运行，配合 `-race` 检查快照自洽、版本不回退。

## 6. 包结构

```
catalog/
  types.go       数据模型
  errors.go      可区分错误原因与 Conflict 结构
  store.go       本地内存存储（实体、历史、血缘边、邻接表）
  evolution.go   规格校验与兼容/冲突判定（纯函数）
  catalog.go     Register/RegisterAt/Get/History/AddLineage/Search
  lineage.go     BFS 影响分析
cmd/demo/main.go 端到端演示
```
