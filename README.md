# 本地元数据目录服务 (metadata-catalog-go)

一套本地（进程内）运行的元数据目录，用内存存储维护数据**实体、字段、版本与血缘关系**，
解决多来源重复上报、结构演进与关系变更的一致性与可解释性问题。

同一个逻辑实体可以被多个来源（业务库、数仓……）分别上报自己关心的那部分字段/负责人/标签，
目录把它们合并成**一条**实体记录，并保留每个属性的来源归属。

无外部依赖，`go run` / `go test` 即可验证；也可以 `go run ./cmd/server` 起一个本地 HTTP
服务，不写 Go 代码直接用 curl 操作。

## 1. 数据模型

| 类型 | 说明 |
| --- | --- |
| `EntitySpec` | 某个来源的一次上报：`Name`、`Source`、`Kind`、`Description`、`Owners`、`Tags`、`Fields[]`、`DropFields[]`（显式删除请求） |
| `FieldSpec` | 字段规格：`Name`、`Type`（string/int/float/bool/date/json）、`Required`、`Description` |
| `Entity` | 目录中实体的**当前合并状态**：稳定 ID、`Sources[]`、`FieldSources`/`OwnerSources`/`TagSources` 归属表、单调递增 `Version`、创建/更新时间 |
| `SourceContribution` | 某个来源最近一次被接受的上报内容及提交时间（按来源回查） |
| `EntityVersion` | 一条**不可变**历史：版本号、触发来源、当时的合并 Spec、时间戳、变更原因 |
| `LineageEdge` | 一条有向血缘边：`UpstreamID -> DownstreamID`（生产者 -> 消费者）+ `Relation` |
| `ImpactReport` | 影响分析结果：起点、方向、去重且有序的实体列表、实际经过的边 |

### 实体归属规则（同一实体的判定）

**实体 ID = `kind + "/" + name`，二者都先去首尾空白再转小写。**

- 只有 kind 和（归一化后的）名字都相同的两份上报，才指向同一条实体记录——与来自哪个
  来源无关，所以多来源上报天然合并为一条记录。
- 同名但不同 kind（例如表 `users` 和消息主题 `users`）**不会**被强行合并，各自独立。
- 规则是纯函数、可解释：给定任意一份上报，都能手工算出它会落到哪条记录上。
- 血缘边以 `UpstreamID + NUL + DownstreamID` 为唯一键。

## 2. 多来源合并规则

每个来源在目录里保存自己**最近一次被接受的上报**（contribution）。实体的当前状态是所有
来源 contribution 的**并集**，而不是后报覆盖先报：

- **字段**：按名字取并集。某来源没提某个字段，**不等于删除**——其他来源贡献的字段原样
  保留；字段只有在"最后一个贡献它的来源也不再报它"时才会从合并状态消失，而这属于破坏性
  变更，会被拒绝（见下表 `field_removed`）。显式删除只能走 `DropFields`，同样被拒绝。
- **字段类型**：所有来源必须一致；`Required` 取各来源的 OR。
- **负责人 / 标签**：各来源最新值取**并集并去重**；某来源重报只影响它自己那一份，
  不会覆盖其他来源贡献的负责人/标签。
- **描述（实体级/字段级）**：多个来源不一致时，确定性地取**来源名字典序最小**且非空的
  那份，保证合并结果与上报顺序无关。
- **归属**：合并状态里的每个字段、负责人、标签都记录贡献来源
  （`FieldSources`/`OwnerSources`/`TagSources`）；`Contributions(id)` 可按来源回查
  "它报了什么、什么时候报的"。

### 重复上报（幂等）

某来源重报与自己已存 contribution **语义相同**的内容（字段/负责人/标签顺序无关），返回
`duplicate_report`：**不新增版本、不改写历史、不刷新任何时间戳**，来源信息也不会重复堆积。

### 来源之间的兼容与冲突判定

只要存在任一冲突，**整份上报被拒绝**（已合并状态不变），错误携带 `Conflicts[]`，
每条冲突都带 `Source`（触发来源）、`Field`、`Kind`、`Detail`（指明与哪个来源冲突）：

| 冲突 kind | 触发条件 |
| --- | --- |
| `type_changed` | 上报的字段类型与当前合并类型不一致（可能来自其他来源，也可能是自己改口） |
| `required_added` | 新增必填字段，或把可选字段收紧为必填 |
| `field_removed` | 显式 `DropFields`；或自己是某字段最后的贡献者而本次不再报它 |
| `semantic_changed` | kind 变化（kind 属于实体身份，仅作为纯函数判定保留） |

错误原因按"是否牵扯其他来源"区分，方便直接找对应的人对齐：

- `incompatible_change`：只与自己来源的历史贡献冲突（如单方面改类型、漏掉只有自己报的字段）；
- `source_conflict`：与其他来源的贡献打架（如 A 说 `id:int`、B 说 `id:string`，
  或 B 显式要求删掉 A 还在报的字段）。

兼容、会被放行的合并：新增**可选**字段、改描述、增删自己来源的 owner/tag、
新来源加入（即使只重复别人已报的内容，也会记为新贡献者并更新归属）。

### 版本与历史

每次接受都追加一条不可变 `EntityVersion`（记录触发来源、合并后的完整 Spec、原因），
`CreatedAt` 始终保留首次登记时间；`Version` 单调递增。

### 错误原因（可区分、可断言）

`invalid_spec` / `entity_not_found` / `incompatible_change` / `source_conflict` /
`duplicate_report` / `version_conflict` / `duplicate_edge` / `self_edge`，
统一为 `*catalog.CatalogError`，冲突失败还可检查其 `Conflicts`（含来源归属）。

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
  按实体 ID 排序。
- 全库由单把互斥锁保护：每次 `Register`/`AddLineage` 都是"校验 + 合并 + 提交"原子完成，
  读操作绝不会读到半成品状态，并发更新不会互相覆盖，两次查询不会自相矛盾。
- 对"读-改-写"场景提供乐观并发控制 `RegisterAt(spec, expectedVersion)`：
  - `-1` 不检查版本（等同 `Register`）；
  - `0` 仅允许新建（实体已存在则失败）；
  - `>0` 仅当当前版本恰好等于该值时才提交，否则返回
    `version_conflict`，调用方重新读取、合并、重试，从而杜绝丢失更新。

## 5. 本地 HTTP 入口

不写 Go 代码也能用全部能力：

```bash
go run ./cmd/server            # 默认监听 127.0.0.1:8080，-addr 可改
```

| 方法与路径 | 说明 |
| --- | --- |
| `POST /api/entities` | 提交上报（body 为 `EntitySpec` JSON，可带 `expected_version` 做 CAS） |
| `GET /api/entities?tag=&owner=&keyword=` | 按标签/负责人/关键字检索 |
| `GET /api/entities/{kind}/{name}` | 实体当前合并状态（含来源归属） |
| `GET /api/entities/{kind}/{name}/history` | 版本历史（含每版触发来源） |
| `GET /api/entities/{kind}/{name}/sources` | 各来源贡献回查（报了什么、什么时候） |
| `GET /api/entities/{kind}/{name}/downstream` | 下游影响范围（顺序稳定可复现） |
| `GET /api/entities/{kind}/{name}/upstream` | 上游来源分析 |
| `POST /api/lineage` | 登记血缘边 `{"upstream_id","downstream_id","relation"}` |

示例：

```bash
# 业务库与数仓分别上报同一逻辑实体 table/orders
curl -X POST localhost:8080/api/entities -d '{"name":"orders","source":"bizdb","kind":"table",
  "owners":["finance"],"fields":[{"name":"id","type":"int","required":true}]}'
curl -X POST localhost:8080/api/entities -d '{"name":"orders","source":"warehouse","kind":"table",
  "tags":["core"],"fields":[{"name":"id","type":"int","required":true},{"name":"amount","type":"float"}]}'

curl localhost:8080/api/entities/table/orders           # 合并状态 + 归属
curl localhost:8080/api/entities/table/orders/sources   # 按来源回查
curl 'localhost:8080/api/entities?tag=core'             # 检索
```

错误统一为 JSON `{"reason","message","conflicts"}`，状态码映射：
`invalid_spec`/`self_edge`→400，`entity_not_found`→404，
`duplicate_report`/`version_conflict`/`duplicate_edge`→409，
`incompatible_change`/`source_conflict`→422。

## 6. 验证方法

```bash
# 全部单元测试（多来源合并/冲突 + 原有的重复上报、不兼容演进、环状/多路径血缘、并发更新）
go test ./... -race -count=1 -v

# 端到端演示（多来源合并、冲突拒绝、环状血缘、CAS）
go run ./cmd/demo

# 本地 HTTP 服务（见第 5 节 curl 示例）
go run ./cmd/server
```

测试设计要点：

- 失败一律断言具体 `Reason`，冲突失败进一步断言具体 `Conflict{Source, Field, Kind}`；
- 多来源用例覆盖：并集合并与归属表、按来源回查、同源重复上报不刷新时间、
  跨来源类型冲突（`source_conflict`）、显式删除拒绝、"没提到≠删除"、
  后报不覆盖先报、同名不同 kind 不合并、16 来源并发注册不丢数据；
- HTTP 用例覆盖：完整多来源流程、冲突/重复/404 的状态码与错误体、
  检索与血缘影响分析、12 来源并发经 HTTP 提交不丢数据且读不到半截状态；
- 血缘测试构造菱形 + 回环图，断言去重、防环、边不重复以及重复运行顺序一致；
- 并发测试：20 个 goroutine 基于 CAS 各加一个字段，最终版本与字段数精确可校验；
  另有 1 写 4 读持续运行，配合 `-race` 检查快照自洽、版本不回退。

## 7. 包结构

```
catalog/
  types.go       数据模型（含来源归属、SourceContribution）
  errors.go      可区分错误原因与 Conflict 结构（含来源归属）
  store.go       本地内存存储（实体、历史、血缘边、邻接表、身份规则）
  merge.go       多来源合并引擎（并集、归属、冲突判定，纯计算）
  evolution.go   规格校验与单源兼容判定（mergeReport 的适配器）
  catalog.go     Register/RegisterAt/Get/History/Contributions/AddLineage/Search
  lineage.go     BFS 影响分析
  http.go        HTTP/JSON 入口（NewHTTPHandler）
cmd/demo/main.go    端到端演示
cmd/server/main.go  本地常驻 HTTP 服务
```
