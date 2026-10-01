# 本地元数据目录服务 (metadata-catalog-go)

一套本地运行的元数据目录，用内存存储维护数据**实体、字段、版本与血缘关系**，
解决多来源重复上报、结构演进与关系变更的一致性与可解释性问题。

**同一个逻辑实体可以被任意多个来源分别上报**（业务库、数仓……），目录把它们合并成
一条实体记录，并保留每个字段/负责人/标签的来源归属与每个来源的上报日志。
既可直接作为 Go 库使用，也可以 `catalogd` 常驻进程 + HTTP API 的方式使用。

无外部依赖，`go run` / `go test` 即可验证。

## 1. 数据模型

| 类型 | 说明 |
| --- | --- |
| `EntitySpec` | 一个来源的一次上报：`Name`、`Source`、`Kind`、`Description`、`Owners`、`Tags`、`Fields[]`、`RemovedFields[]`（显式删除请求，一律拒绝）。上报只需携带该来源关心的部分 |
| `FieldSpec` | 字段规格：`Name`、`Type`（string/int/float/bool/date/json）、`Required`、`Description` |
| `Entity` | 目录中实体的**当前合并状态**：稳定 ID、单调递增 `Version`、`Sources[]` 及 `FieldSources`/`OwnerSources`/`TagSources` 归属表（每项是谁贡献的） |
| `EntityVersion` | 一条**不可变**历史：版本号、触发来源、当时的合并 Spec、时间戳、变更原因 |
| `SourceReport` | 一条已接受上报的日志：来源、原文 Spec、上报时间、产生的版本号 |
| `LineageEdge` | 一条有向血缘边：`UpstreamID -> DownstreamID`（生产者 -> 消费者）+ `Relation` |
| `ImpactReport` | 影响分析结果：起点、方向、去重且有序的实体列表、实际经过的边 |

## 2. 多来源归属规则（身份判定）

**实体 ID 只由实体名称决定：`ID = trim(Name)`，与上报来源无关。**

- 任意多个来源上报**同名**实体 → 同一条实体记录，各自的内容按第 3 节规则合并；
- 同名但 **Kind 不一致** → 视为归属冲突，整条上报被拒绝
  （`incompatible_change` / `semantic_changed`，冲突里标注来源），
  **既不强行合并，也不静默拆成两条**；
- 因此真正不同的实体必须使用不同名称（建议带域前缀，如 `risk.orders` / `ads.orders`）；
- 名称不允许为空、不允许含 `/`（保证 HTTP 路径可用）。

这套规则是保守且可解释的：同名同 Kind 才合并；任何不一致都变成
**可定位到具体来源的拒绝**，交给对应团队对齐，而不是目录擅自猜测。

## 3. 多来源合并与冲突判定

`Register(spec)`（以及带乐观锁的 `RegisterAt(spec, expectedVersion)`）的判定顺序：

1. **结构校验**：名称/来源非空、名称不含 `/`、字段不重名、类型合法、
   `RemovedFields` 不与 `Fields` 重叠，否则 `invalid_spec`（不接触任何状态）。
2. **同源重复上报**：与**该来源自己上一次被接受的上报**语义一致
   （字段/Owner/标签顺序无关）→ `duplicate_report`，
   **不新增版本、不改写历史、不刷新时间戳、不在上报日志里堆积**。
3. **冲突检测**：存在任一冲突则**整体拒绝**（该来源本次任何内容都不生效），
   返回 `incompatible_change` 且携带 `Conflicts[]`，每条冲突都标注
   **来源 Source、字段 Field、类别 Kind**：

   | 冲突 kind | 触发条件 |
   | --- | --- |
   | `semantic_changed` | 上报的 Kind 与实体现有 Kind 不一致 |
   | `type_changed` | 某来源上报的字段类型与合并状态中的类型不一致 |
   | `required_added` | 新增必填字段，或把合并状态中的可选字段收紧为必填 |
   | `field_removed` | 显式删除：`RemovedFields` 非空（一律拒绝，删除需多方协调）；或隐式删除：某来源丢弃了**只有它自己贡献**的字段 |

   **"没提到"不等于"删除"**：来源 B 的上报里没有提到来源 A 贡献的字段，
   该字段保留；只有丢弃字段的来源自己、且没有其他来源贡献该字段时，
   才构成隐式删除并被拒绝。若字段仍被其他来源贡献，丢弃方只是停止贡献，
   字段保留、归属表更新。
4. **合并（不是覆盖）**：冲突为空即接受：
   - 字段取**并集**（新增可选字段放行），同名字段类型已在第 3 步保证一致；
   - `Owners` / `Tags` 跨来源取**并集并去重**（排序存储，结果确定）；
   - `Description` 取最近一次非空上报；
   - 合并状态或来源归属有变化 → 提交新版本（version + 1）并写入不可变历史；
     仅来源贡献记录变化而合并状态不变 → 只记录贡献与上报日志，不增版本。

每次接受的上报都追加到**按来源可查的上报日志**（`Reports(id, source)`）：
谁、什么时候、报了什么原文、产生了哪个版本。被拒绝和重复的上报不进日志。

### 错误原因（可区分、可断言）

`invalid_spec` / `entity_not_found` / `incompatible_change` /
`duplicate_report` / `version_conflict` / `duplicate_edge` / `self_edge`，
统一为 `*catalog.CatalogError`，演进/合并失败还可检查其 `Conflicts`
（含来源、字段、类别）。

## 4. 血缘与影响分析的遍历语义

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

## 5. 检索与并发一致性

- `Search(SearchQuery{Keyword, Tag, Owner})` 对非空条件取**交集**；
  关键字小写匹配实体名、描述、kind 与字段名。结果始终来自某个**已提交快照**，
  按实体 ID 排序；删除标签/字段后的检索立即反映最新状态。
- 全库由单把互斥锁保护：每次 `Register`/`AddLineage` 都是"校验 + 合并 + 提交"
  原子完成，读操作绝不会读到半成品状态，并发更新不会互相覆盖、不会丢失。
- 对"读-改-写"场景提供乐观并发控制 `RegisterAt(spec, expectedVersion)`：
  - `-1` 不检查版本（等同 `Register`）；
  - `0` 仅允许新建（实体已存在则失败）；
  - `>0` 仅当当前版本恰好等于该值时才提交，否则返回
    `version_conflict`，调用方重新读取、合并、重试，从而杜绝丢失更新。

## 6. 本地 HTTP 入口（catalogd）

不写 Go 代码也能用。启动常驻进程：

```bash
go run ./cmd/catalogd -addr 127.0.0.1:8080
```

| 方法与路径 | 说明 |
| --- | --- |
| `POST /v1/reports` | 提交上报，body 为 `EntitySpec` JSON；成功返回合并后的 `Entity` |
| `GET /v1/entities?tag=&owner=&keyword=` | 按标签/负责人/关键字检索（条件取交集） |
| `GET /v1/entities/{id}` | 实体当前合并状态（含来源归属表） |
| `GET /v1/entities/{id}/versions` | 版本历史（每条含触发来源与合并状态） |
| `GET /v1/entities/{id}/reports?source=` | 上报日志，可按来源过滤 |
| `GET /v1/entities/{id}/downstream` | 下游影响范围（稳定顺序） |
| `GET /v1/entities/{id}/upstream` | 上游来源范围（稳定顺序） |
| `POST /v1/lineage` | 登记血缘边：`{"upstream_id","downstream_id","relation"}` |

示例：

```bash
# 业务库上报自己关心的部分
curl -X POST localhost:8080/v1/reports -d '{
  "name":"orders","source":"bizdb","kind":"table",
  "owners":["alice"],"tags":["core"],
  "fields":[{"name":"id","type":"int","required":true},
            {"name":"amount","type":"float"}]}'

# 数仓侧上报同一实体的另一部分 -> 合并为一条记录
curl -X POST localhost:8080/v1/reports -d '{
  "name":"orders","source":"warehouse","kind":"table",
  "owners":["bob"],"tags":["curated"],
  "fields":[{"name":"note","type":"string"}]}'

curl localhost:8080/v1/entities/orders            # 合并状态 + 来源归属
curl localhost:8080/v1/entities/orders/versions   # 版本历史
curl 'localhost:8080/v1/entities/orders/reports?source=bizdb'  # bizdb 上报日志
curl 'localhost:8080/v1/entities?tag=core'        # 检索
```

错误统一返回 JSON：`{"reason": "...", "message": "...", "conflicts": [...]}`，
状态码可区分：`400`（invalid_spec / self_edge / 请求体非法）、
`404`（entity_not_found）、`409`（incompatible_change / duplicate_report /
version_conflict / duplicate_edge）。`conflicts` 中含来源、字段、类别，
可直接拿去对齐。

并发保证与 Go API 一致：所有提交在锁内原子完成，任何一次 GET 读到的都是
一份完整已提交状态；同一张血缘图多次查询的影响范围顺序稳定可复现。

## 7. 验证方法

```bash
# 全部单元测试（多来源合并/冲突、重复上报、不兼容演进、环状与多路径血缘、并发）
go test ./... -race -count=1 -v

# 端到端演示（多来源合并、归属、冲突、血缘、CAS）
go run ./cmd/demo

# 本地 HTTP 入口
go run ./cmd/catalogd -addr 127.0.0.1:8080   # 然后用上面 curl 示例验证
```

测试设计要点：

- 失败一律断言具体 `Reason`，合并/演进失败进一步断言具体
  `Conflict{Source, Field, Kind}`；
- 多来源用例覆盖：并集合并与归属表、同源重复上报不堆积不刷新时间、
  跨来源类型/必填/Kind 冲突整体拒绝、显式删除拒绝、
  "没提到不等于删除"、后报不覆盖先报、并发多来源提交不丢数据；
- HTTP 用例覆盖：端到端合并流程、错误状态码与冲突体映射、
  20 个来源并发经 HTTP 提交 + 并发读，校验每次读到的状态自洽
  （字段数 == 版本数）且最终无丢失；
- 血缘测试构造菱形 + 回环图，断言去重、防环、边不重复以及 50 次重复运行顺序一致；
- 每个用例通过 `t.Logf` 打印**输入**与**最终判定依据**，用 `-v` 可直接查看。

## 8. 包结构

```
catalog/
  types.go       数据模型（含来源归属与上报日志）
  errors.go      可区分错误原因与 Conflict 结构（含来源标注）
  store.go       本地内存存储（实体、历史、来源贡献、上报日志、血缘边）
  merge.go       多来源合并与冲突判定（纯函数）
  evolution.go   规格校验；单来源演进判定（merge 的特例）
  catalog.go     Register/RegisterAt/Get/History/Reports/AddLineage/Search
  lineage.go     BFS 影响分析
server/
  server.go      HTTP API（net/http 标准库，无外部依赖）
cmd/catalogd/    本地常驻 HTTP 入口
cmd/demo/        端到端演示
```
