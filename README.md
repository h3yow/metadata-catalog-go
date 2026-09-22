# metadata-catalog-go

本地运行的元数据目录服务：用本地内存存储维护数据实体、字段、版本与血缘关系，
处理多来源重复上报、结构演进与关系变更，输出一致且可解释的判定。

## 快速开始

```bash
go run ./cmd/server -addr 127.0.0.1:8080
```

## 快速体验

```bash
# 1) 登记实体 -> 201 created
curl -s -X POST localhost:8080/v1/entities -d '{
  "name":"orders","source":"team-a","kind":"table",
  "fields":[
    {"name":"id","type":"int64","semantic":"order.id","optional":false},
    {"name":"amount","type":"decimal","semantic":"order.amount","optional":false}
  ],"tags":["finance"],"owners":["alice"]}'

# 2) 兼容演进：新增可选字段 -> 200 compatible
curl -s -X POST localhost:8080/v1/entities -d '{
  "name":"orders","source":"team-a","kind":"table",
  "fields":[
    {"name":"id","type":"int64","semantic":"order.id","optional":false},
    {"name":"amount","type":"decimal","semantic":"order.amount","optional":false},
    {"name":"coupon","type":"string","semantic":"order.coupon","optional":true}
  ],"tags":["finance"],"owners":["alice"]}'

# 3) 把第 2 步载荷再发一次 -> 200 noop（不升版本、不改写历史）

# 4) 不兼容演进（删字段）-> 400 {"reason":"field_removed",...}
curl -s -X POST localhost:8080/v1/entities -d '{
  "name":"orders","source":"team-a","kind":"table",
  "fields":[{"name":"id","type":"int64","semantic":"order.id","optional":false}],
  "tags":["finance"],"owners":["alice"]}'

# 5) 历史版本与检索
curl -s localhost:8080/v1/entities/orders/history
curl -s 'localhost:8080/v1/search?tag=finance&keyword=coupon'

# 6) 血缘登记与影响分析（重复登记返回 duplicate:true）
curl -s -X POST localhost:8080/v1/entities -d '{"name":"audit","source":"s","kind":"table","fields":[{"name":"id","type":"int64"}]}'
curl -s -X POST localhost:8080/v1/lineage -d '{"upstream":"orders","downstream":"audit","source":"job-1"}'
curl -s localhost:8080/v1/lineage/downstream/orders
```

## 代码结构

```
internal/catalog/   核心领域逻辑：实体登记/版本演进、血缘、检索（无框架依赖）
  entity.go         Register/Get/History + 兼容性判定
  lineage.go        AddLineage/Downstream/Upstream（去环 BFS）
  search.go         Search（标签 AND / 负责人 ANY / 关键字 / kind）
  errors.go         RejectReason 稳定枚举与 RejectError
  types.go          领域类型
internal/server/    薄层 HTTP 适配，reason 与状态码映射
cmd/server/         本地启动入口
docs/design.md      数据模型、判定规则、遍历语义、并发模型、验证方法
```

## 测试

```bash
go test ./...            # 全量单测
go test ./... -race      # 竞态检测
go test ./... -v         # 查看每个用例打印的“输入 => 判定依据”
```

详见 [docs/design.md](docs/design.md)。
