## 2026-10-18 3.0 源码候选落点

已按下文确认门实现；UpdateSchedule显式接收current与replacement，在同TxStore事务内对原id/version做CAS，缺失和changed形成各自durable结果，不自动rebase。零next经nullableInstant写SQL NULL。canonical 3.0 mutation保旧字段集，仅新增条件nullable next；HTTP现有mapper已支持该形状，无第二转换协议。当前尚未固定发行给BFF或正常激活，最终隔离门见CURRENT。

## 2026-10-18 同触发源编辑：已确认实施门

用户已确认下文候选B，A不实施。现application在原receipt检查后为replace读取当前schedule；只有两边非零run_at精确相同才保原next，exhausted强制保status与null next（包括clock回拨），active/paused过due不挪now。create、不同run_at或recurring转once仍要求future。PG UPDATE以读取的id/version围栏，冲突不自动重读重试成成功；claim清除、最新opaque定义和receipt同事务，旧occurrence/outbox/receipt不重写。NextDueAt按nullableInstant绑定，禁止零Go时间写成year0001。

位置仍为现application、TxStore与PG adapter；不新增表/服务/业务周期。mutation ACK仅演进next的nullable条件，不强加required lifecycle status给旧ACK；完整正式契约/manifest/parity配对后实施，不提前正常激活。验证沿现once PG用例覆盖同源耗尽、原due/misfire、clock回拨、Planner/replace两胜序、并发replace及delete/recreate ABA、取消和原receipt重放。下文待决措辞保留为历史，不再等待重复确认。

## 2026-10-18 到期 once 编辑：放置比较与待决门

当前2.0已批准行为仍是create/replace都需未来instant（receipt先重放）；本段只记录真实400诊断后的候选，不静默覆盖该决定或提前实施。

候选A让BFF在Scheduler已terminal时保留旧callback定义并结算新管理command，代价是产品最新revision与Scheduler正文分离，需要精确旧outbox来源、连续编辑/恢复/current绑定链。候选B在现Scheduler replace事务内识别同一非零run_at并保留消费位置，更新允许定义/opaque正文；改变触发源和新建仍严格future，exhausted保持exhausted/null，active过due仍按原misfire处理，不挪到now也不先宣告已执行。B更符合现通用owner且不增加表/服务/来源链，但需先获得产品语义确认并过正式contract门。

后继若确认B：receipt检查后读取原schedule，更新以原id/version围栏，避免planner耗尽或delete/recreate ABA被旧读覆盖；更新/claim清除/revision与receipt同事务。已物化occurrence/outbox及旧receipt保持原字节。exhausted优先于请求paused标志，BFF配对解释最终态，不造paused/exhausted二重状态。future同instant编辑与clock回拨亦不得复活已耗尽触发。CAS冲突明确错误，不把新读结果当旧命令成功。

写区仅本owner现application/PG/必要port、HTTP响应、canonical contract/artifact及原相关测试，再固定producer交付BFF；原3设计面配对后才实施。现mutation ACK非空next需要正式演进，禁止仅Go输出null而绕过SDK。验证需真实PG覆盖同instant exhausted零新增occurrence、past active misfire、paused/resume、Planner/replace两胜序、并发replace/ABA、取消与丢ACK回放、不同past instant拒绝；当前仅paused一次诊断，不声称这些未来检查已通过。

## 2026-10-18 通用一次性调度（目标契约 2.0.0，隔离实施）

沿既定边界实现，不新增进程/依赖/业务周期类型。Schedule 以既有 rule 或新绝对 UTC run_at 二选一表达触发源；BFF 负责自然语言/本地日历、MCP工具参数与产品归属，Scheduler 不接收用户token、工具名或连接凭据。MCP工具只应复用BFF任务用例；该新协议入口及定时Run所选MCP上下文仍属后继，不在本片假称已接入。

当前 once 的首次 create/replace 只接受未来毫秒精度 instant；静态校验在receipt前，随时间变化的未来校验必须在同key原receipt检查之后，保证到点后原ACK仍能重放。pause不丢原 due，resume一次性任务仍以原run_at为due，过期按既有misfire政策处理；resume使用已读version做CAS，防止覆盖并发replace/planner。已exhausted的pause/resume返回持久化409 schedule_exhausted，不复活；显式replace+新key可建立未来触发，原occurrence身份/历史不变。

once到点最多物化一条occurrence；按时触发，错过时skip产生可观察skipped，fire_once/catch_up_bounded均最多一条dispatch。occurrence/outbox、status=exhausted、next_due_at=NULL、claim清空和version递增在同一本地事务；仍受原version+claim CAS保护，失败整体回滚。exhausted仅表示不再生成触发，不表示目标或Agent执行成功；已物化outbox继续既有重试，pause/delete也不冒充已派发执行取消。旧receipt、occurrence与outbox不迁移、不清空。

新增RunAt仍用领域time.Time，零值只在内部表示缺失；SQL nullable值在adapter转换，HTTP明确缺字段或null：recurring返回schedule、once返回run_at，current exhausted必须next_due_at=null。原recurring命令digest与历史ACK序列化保持原样；新once digest加入run_at。只改现domain/application/PG/HTTP及canonical契约，不新增通用调度抽象。正常库不执行DDL/重载；fresh schema与真实PG并发、回滚、重建、重放、有限重试由独占fixture验证。

## 2026-10-18 通用周期收敛：当前状态读取切片

既定边界：BFF 拥有用户选择的多星期、月内多日期及本地日期；Scheduler 只解释通用 recurrence/绝对一次性 instant，不引入项目、会话、Skills 或业务周期枚举。完整 once/exhausted 后继实施，本切片不提前声明其存在。

当前 mutation receipt 是不可变首次结果，不能充当实时 next/status。增加同资源 GET，由现 HTTP handler → application.Service.Get → 同一 Store transaction 的 tenant/name GetSchedule 读取当前行；复用既有 port，不增加 query bus、服务、缓存或轮询器。GET 沿用 service token、可信 tenant 和 request ID；不经过 Execute，不获取 command identity 锁、不写 receipt。取消传递到现数据库事务，缺失或其他租户同为 404，存储失败不冒充缺失。当前读取和后续更新不具有跨请求快照保证。

写区为本 owner application/HTTP、canonical OpenAPI、对应测试与三设计面。现 Schema、mutation wire/历史 receipt、planner/dispatcher 不变；GET 只承诺当前 active/paused 与非空 next，有限调度的 nullable next/status 演进另行配对。验证包含 HTTP 验证/隔离/取消、contract parity，以及独占 PG 上 planner 推进、pause、原 receipt 精确重放和 GET 前后四表事实不变。不重载正常 Scheduler、不启用正常任务/模型/扣费。

## R45-WIN09：fixture 阻断返修，catalog 未到行为门（2026-10-01）

Root 自有 PG 首轮实际 24 顶层 **18 PASS / 6 FAIL，1.388s**；六个新资源用例均在完整四表 beforeSnapshot 因 occurrence 无 sentinel 失败，未进入 catalog/function/type 目标断言，不计结构行为 RED。日志 `/tmp/kokoro-scheduler-catalog-r45-root-resource.log`，Root 报告 owned database 已回收。

本卡只在新增测试的五处 fixture 入口补现 `seedNamespaceBoundaryFacts` 的 base/neighbor 调用（共同 helper 覆盖 function/type 两例）；原 17 全文、snapshot 断言、bootstrap 原候选及 SQL/Store/runtime 不变。静态检查已通过，实际后置结果待 Root 重跑；下方全部正文保持。

## R44-WIN09：具名 catalog 入口候选，待 Root 真实门（2026-10-01）

Root 已报告 D0 独立审 0P0/0P1/0P2 并授权本卡；本节更新当前实现阶段，下方 R43/namespace/历史原文保持。仅现 bootstrap、现 integration 与四文档小前缀变更，未新增文件或目录。

- 现 bootstrap 实现 `ReadSchemaCatalog(ctx, pool, target) (SchemaCatalog, error)` 与纯 `CompareSchemaCatalog(actual, reference) []SchemaCatalogDifference`；内部结构类型只在 owner adapter/tests 使用，不改变网络 ABI、Store/Runtime/CLI 或 installer 原事务。
- Reader 使用同一 pgx transaction 的只读 Repeatable Read 快照，最长 15 秒；事务内实际核对目标 search_path、UTC、read-only 与 isolation。局部 GUC 在结束时恢复，有界 rollback，driver 诊断不回显 SQL/连接信息。无参考 DDL、自动修复或业务行读取。
- 结构来自真实 catalog：表/列/default/constraint/index、composite/array 类型及关联身份。仅身份字段归 SELF，OID 解析为逻辑身份；反解表达式/literal 不改写。未知 owner 对象失败，自动 UNIQUE recheck trigger 则按 owning constraint 逻辑身份采集其语义，而不比较带 OID 的物理生成名。
- reference 仍仅在 Root-owned fixture 由现 installer＋`database.Schema` 创建。原 installer 后置检查及 runtime ready 保持轻量；完整比较是显式 fresh/test 验证门，不暗中升级 Ping。
- 本轮无资源编译、vet、build 已退出 0；真实 PG 正例/漂移矩阵和独立源码审待 Root，未宣称结构验收或实际 RED/GREEN。

## R43-WIN09-fullcatalog D0：只读结构门设计，待 Root 审查（2026-10-01）

本节为当前 fullcatalog 方案；只增补文档，不授予源码、测试、SQL、generated、依赖、进程或资源操作权限。基线为 owner `main / 9a4effd150ab739acaed2579faa94618bb26f772`，Root 任务卡基线 `14c70d794b69428ef012c07ca38804b62f77b5cc`。下方 namespace 设计与历史正文逐字保留，阶段状态以本节及 CURRENT 的 R43 节为准。

### 放置门与当前事实

| 项 | 结论 |
| --- | --- |
| Owner | Scheduler 的 bootstrap 结构验证；不新增业务事实 owner 或 writer。Root 唯一执行真实资源、集成与提交。 |
| 当前事实 | `database/schema.go` 通过唯一 `//go:embed schema.sql` 发布 `database.Schema`；现 `bootstrap.go` 的 `ApplySchemaToEmptyDatabase` 使用该值事务安装。不可变 `config.DatabaseTarget`、namespace 对象占用判断、安装锁和轻量 readiness 已存在；结构 catalog reader/comparator 尚不存在。既有 20 路径工作树冻结，其他变更不接收。 |
| 目标职责 | 对显式目标采集只读结构快照，供 Root-owned fresh/integration gate 与同源参考比较；不修复漂移，不读取业务行，不公开新的 HTTP/RPC。 |
| 目录比较 | 采用现 `internal/adapters/postgres/bootstrap.go`：安装与安装结构验证同一冷路径职责、复用 pgx/目标类型。现 `store.go` 也可获得 session，但承担业务持久化及轻量 ready，淘汰以免完整 catalog 穿入健康热路径；`config.go` 是纯 URL 规则，亦不承载数据库采集。 |
| 粒度 | 扩展现 bootstrap 文件中的内部结构/函数，无新文件、单文件子目录、模块、command 或进程；测试扩展现 `test/integration/postgres_test.go`，不是新建测试中心。 |
| 依赖 | adapter 内使用 pgx、config 目标及已有 canonical loader；Application/Domain 不接收 catalog SQL、pgx 或快照类型。Root fixture 可调用获批内部验证入口与现 installer，不另造 SQL loader。 |
| 数据/API | canonical 四表、无 FK、现事务/tenant/幂等及业务状态机不变；无 cache、业务数据读写、机器 contract/generated 或部署 role 变化。内部验证边界见 API_CONTRACT R43；结构语义见 DATA_MODEL R43。 |
| 删除项 | 本阶段无代码删除；拒第二 editable expected catalog、DDL 副本、默认 namespace、自动修复、兼容 alias 和 runtime reference DDL。 |
| 验证 | D0 仅文档原文后缀与 20 路径哈希检查。后继经 Root 授权执行 gofmt、`go vet -p 1 ./...`、`go test -p 1 -count=1 -timeout=90s ./... -v`、`go build -p 1 ./...`；真实 catalog/fixture gate 由 Root 在独占资源运行，不将资源 skip 当通过。 |

### 采集与参考生命周期

- Reader 显式接收既有 pool 与不可变目标；在同一 acquired session 开只读 Repeatable Read 事务，事务内确认目标存在，设置局部目标 search_path 与 UTC，参数化 catalog 查询。各查询共享一致快照；有界 context、错误退出及有界 rollback/release，绝不 CREATE/ALTER/DROP 或创建参考。现 installer 的 Read Committed＋事务锁不因此改成 Repeatable Read。
- 结构采集完整覆盖当前 canonical DDL 的表/列/default/CHECK/PK/UNIQUE/索引及 namespace 对象 inventory；其字段、逻辑身份归一和不支持对象的失败规则只在 DATA_MODEL 本节解释。catalog profile 不是 PostgreSQL 运维状态或 production ACL 的镜像。
- 参考只有一个来源：Root 在自有独占 fixture database 内以已有 parser 解析 reference 目标，再调用现 `ApplySchemaToEmptyDatabase`，由 `database.Schema` 安装唯一 canonical SQL；随后使用同一 reader 采集 reference 与 actual。不得硬编码另一组 expected 表/字段/索引或读取 SQL 后另写 parser。两个 schema 同一 server/version/session policy，server_version_num 由 Root 记录。
- 现 integration 的 `namespaceBoundaryFactsSnapshot` 是邻居业务行事实证据，不是结构 catalog oracle。它继续保护邻居；新增结构快照另有职责，不能以同表数或行快照充当 schema 完整性。
- 正常 runtime startup/ready 保持现 namespace/UTC/四表轻量验证和 Runtime/Redis 条件；fullcatalog 是显式验证门，不在 runtime 构建参考，也不把变更后的四表仍可 ready 当 fullcatalog PASS。

### 后继阶段门

D0 文档审查后才冻结可调用内部边界及 RED 计划。当前无 reader/comparator；引用不存在符号导致编译失败不算行为 RED。Root 须裁定先交付可编译、真实只读采集基线再加入比较/漂移断言的精确源码授权，或另一可执行行为接入方案；不使用空 stub、伪造快照或生产测试钩子填门。现 installer 的 function-only/type-only 边界测试独立可写，但可能已 GREEN，不能冒称已有缺陷。具体案例与 fixture 回收责任见 DATA_MODEL R43。实现、真实 RED/GREEN 和独立审查尚未发生。

## R40-WIN09-schema：单库 owner namespace cutover，待真实复验（2026-10-01）

状态：Root 已通过四文档门及 R1/R2/R3 真实 RED，并授权当前 owner 唯一 writer 的精确源码/URL 切换；本次工作树基线仍为 `main / 9a4effd150ab739acaed2579faa94618bb26f772`，未提交。唯一 parser、显式安装和 readiness 已实施并通过纯 Go 门，真实 PG/source-process/镜像及完整 catalog 尚未复验，不宣称发布或运行验收。下方旧正文逐字保留；其中专用 database、隐式 namespace 和仅连接 ping 叙述描述旧实现，其他业务设计继续有效。

### 放置与依赖

- Owner 仍为 Scheduler 的 Schedule/Occurrence/receipt/outbox；开发目标是一个 PostgreSQL database、一套现有 role/credential、各 owner 独立 schema，不增加部署角色、服务或数据 owner。
- 比较现 `internal/config/config.go` 与 PostgreSQL adapter：选前者扩展唯一纯数据库 URL 解析，避免将部署配置规则混入 adapter 生命周期；不新建普通文件、目录或依赖。返回不可变 `config.DatabaseTarget`（私有字段，`DriverURL()`/`SchemaName()` 只读方法），runtime 与 installer 接收同一目标；不向 Application/Domain 暴露 pgx 类型。
- `config.Load` 与 `cmd/db-apply-schema/main.go` 共同调用该解析函数。安装入口不调用完整 runtime 配置加载，不要求 Redis、目标 allowlist 或 timer 配置。`cmd/scheduler/main.go` 与安装入口不再裸传原始 URL、不维护第二 parser。
- 当前仅扩展现 `internal/adapters/postgres/bootstrap.go` 的显式安装与 `store.go` 的连接/readiness 职责；业务 repository、四表和状态机保持。只读完整 catalog 能力尚未实施：后继先冻结参考/漂移断言并由 Root 真 RED 后实现，不能用当前 ready 代替。

### 唯一连接规则

- 必须在 `TrimSpace` 前拒绝原始 Unicode 控制字符（包含 CR/LF/TAB/NUL/DEL），不得由 URL parser 或 trimming 洗掉后接受。
- 输入 URL 的 `schema` selector 恰一个，值符合 `^[a-z][a-z0-9_]{0,62}$`；拒空值、重复、列表、`public` 与 `pg_` 前缀。无默认 schema、别名或 public fallback。
- 严格处理 query 解码错误；拒 `options`、`search_path`、输入 `timezone` 及这些保留参数的大小写变体等驱动覆盖入口。selector 的大小写变体不得成为第二入口。
- 消费并删除输入 `schema`，输出 driver URL 显式选择唯一目标 `search_path` 与 UTC；普通 TLS/连接参数保持。parser 和驱动失败诊断不得回显原 URL、密码或配置对象。

### 安装与 catalog

- Bootstrap 显式接收已解析的目标 namespace，不从 `current_schema()` 推导 owner 或锁身份。事务锁 identity 固定为 database＋目标 schema，邻居 namespace 不参与空白判断或 DDL。
- 在安装事务内取锁、确认目标 namespace 的对象边界；目标缺失时仅创建该目标，已存在且为空时直接安装。非空包括 table、view、materialized view、sequence、type、function 等用户对象，不能仅查 `pg_tables`。安装唯一 embedded canonical SQL；任何创建、DDL 或后置检查失败均整事务回滚，无自动修复、清库或重放兼容路径。
- 本 owner 的 catalog 能力只读采集目标表/列/类型/default/约束/索引并稳定排序，不保存第二可编辑期望清单。Root 真实测试在自有 reference namespace 安装同一 canonical SQL，再对目标快照做结构比较；仅归一 namespace/OID 身份，不删除 default、约束或索引定义，也不盲替换 SQL 字面量。正常 runtime 不创建参考 namespace、不运行参考 DDL。
- 同目标并发安装必须由该锁收敛且不得产生部分安装；第二次非空安装拒绝。不同目标不共享全库安装锁；失败只回滚本次目标事务，邻居事实不变。

### Startup/readiness 与阶段门

- Runtime startup 与每次 readiness 验证显式目标 namespace、实际 UTC session 和四张事实表；仅 server ping/public 连通性不构成 ready。目标不存在或事实表缺失即失败，不创建、搜索邻居或 fallback；既有 Runtime/Redis 条件保留。此轻量检查不冒充完整 catalog drift 门。
- 三个先 RED 点分别为现 `internal/config/config_test.go` 的 URL 规则、现 `test/integration/postgres_test.go` 的目标仅含 view 时拒安装、同一 integration 文件的目标缺失但邻居具备同名表时 `Store.Ping` 失败/ready 503。配套同库邻居 sentinel、并发锁/回滚、catalog reference 与已安装 ready 200 正例，真实资源只由 Root 执行。
- 已完成文档审查与 Root 真实 RED，当前 source/URL cutover 待独立审查及 Root 真实 PG/full Go 门。两生产入口、smoke/fixture 与获批 CI/release 生产 URL 已统一显式 selector，测试 base URL 不强加生产 namespace。原隔离/cleanup、R1 与 R2/R3 核心断言保持；完整 catalog、并发/回滚及对象类别扩展真实门未通过前不提交或发布半切换。
# kokoro-scheduler 技术设计

## 1. Owner 与原则

`kokoro-scheduler` 拥有通用 Schedule、Occurrence、command receipt、dispatch outbox、retry 与 claim。它不拥有 BFF `ScheduledTask`、IAM 权限事实或目标 command 的业务结果。

架构锁定为：

```text
trusted caller -> HTTP transport -> Application command transaction -> PostgreSQL
                                                            |
gocron/v2 fixed wakeup -> Application cycle -> due planner -> occurrence + outbox
                                            -> dispatcher -> target HTTP
                                                   |
                                            optional Redis DB 7 lease
```

PostgreSQL 是可恢复事实源。gocron/v2 是可替换 `Wakeup` adapter，不读取 schedule rule，也不保存任何执行历史。Redis 删除或不可用不会删除事实；配置 Redis 时协调失败会 defer durable outbox，而不是绕过 lease。

## 2. 分层与 ports

```text
cmd/scheduler/                 composition root
internal/domain/               pure model/state/policy
internal/application/          transaction use cases and recovery
internal/ports/                Store/TxStore, Clock, Recurrence, Wakeup, Lease, Target
internal/adapters/postgres/    durable store and claims
internal/adapters/recurrence/  recurrence calculation
internal/adapters/gocron/      process wakeup only
internal/adapters/redis/       optional coordination lease
internal/adapters/httpclient/  outbound dispatch
internal/transport/http/       inbound control boundary
```

依赖固定为 `transport -> application -> domain`、`application -> ports`、`adapters -> ports/domain`。Domain 仅依赖 Go 标准库；Application 不 import concrete adapter。

## 3. Command 事务与 tenant

Transport 从受信 `X-Kokoro-Tenant-Id` 取得 tenant，验证 service Bearer、request ID、idempotency key、path 和严格 JSON。Application 再验证 tenant、command identity、Schedule 及 recurrence。

每个 mutation 在一个 PostgreSQL transaction 内执行：

1. 对 `(tenant, scope, idempotency key)` 获取 transaction advisory lock；
2. 查 durable receipt；同 request digest 返回原 receipt，不同 digest 返回 conflict；
3. create/replace/delete/pause/resume tenant-scoped Schedule；
4. 写 receipt 后提交。

因此请求在进程重启后仍可精确 replay。POST 已存在和目标不存在也是可 replay 的持久化 command result。PUT 是完整替换，不保留旧字段兼容路径。

## 4. Due planning 与原子 outbox

每个 wakeup cycle 先运行 planner，再运行 dispatcher。启动时 Application 立即触发一次 cycle，不等待第一个 timer tick。

Planner：

1. 在短事务中按 `(next_due_at, id)` 选择 active due Schedule；
2. 使用 `FOR UPDATE SKIP LOCKED` 写 worker/expiry claim；
3. 按 Schedule 的 recurrence 与 misfire policy 生成 plan；
4. 在单一事务中检查 open occurrence、插入 Occurrence、插入 dispatch outbox、推进 `next_due_at` 并释放 claim。

`UNIQUE (tenant_id, schedule_id, scheduled_at)` 和 `UNIQUE (tenant_id, occurrence_id)` 是重复防线。若并发 update/delete 使 Schedule version/claim 失效，整个 materialization transaction 回滚。

## 5. Recurrence、timezone 与 DST

Schedule 持久化原始本地 rule 与独立 IANA timezone；API 和 occurrence 只传 RFC3339 UTC instant。

支持：

- 五字段 `minute hour day-of-month month day-of-week`；
- `*`、`,`、闭区间 `-`、step `/`；`N/step` 从 N 延续到字段最大值；
- `JAN`–`DEC`、`SUN`–`SAT`，Sunday 0/7；
- `@yearly`/`@annually`/`@monthly`/`@weekly`/`@daily`/`@midnight`/`@hourly`；
- `@every DURATION`，1 秒至 366 天、毫秒精度。

DOM/DOW：两者都受限时 OR；一方为 `*` 或等价 `*/1` 时由另一方筛选。保存前检查字段范围和 selected month 中是否存在可用日期。cron 以 UTC minute 递增并映射到 IANA location 判断，因此 DST gap 自然不产生 instant，DST fold 会产生两个不同 UTC instant。搜索 horizon 为 10 年。

`@every` 不按 wall clock 对齐；以已持久化 due instant 为 anchor，downtime 后用整数倍推进。

## 6. Misfire 与 overlap

- `skip`：记录一个 `skipped/SCHEDULER_MISFIRE_SKIPPED` occurrence，然后推进至 now 之后；
- `fire_once`：为最早 due instant 建立一个 dispatch，随后推进至 now 之后；
- `catch_up_bounded`：按时间顺序最多建立 `catch_up_limit` 个 plan；仍有 backlog 时再写一个 `SCHEDULER_MISFIRE_BOUND_EXCEEDED` skipped occurrence，并推进至 now 之后。

`overlap_policy=forbid` 会查询同 tenant/schedule 的 pending/dispatching/retrying occurrence。已有 open occurrence 时，新 plan 变为 `skipped/SCHEDULER_OVERLAP_BLOCKED`。这是 durable、可查询 SQL 的 outcome，不依赖 timer 的进程内 still-running 行为。`allow` 为每个唯一 due instant 建立独立 outbox。

## 7. Dispatch、retry 与 recovery

Dispatcher 在 PostgreSQL transaction 中：

1. 将 expired final-attempt claim 转为 outbox/occurrence failed；
2. 用 `FOR UPDATE SKIP LOCKED` claim ready 或 expired outbox；
3. 如配置 Redis，取得 tenant+occurrence 派生的短期 lease；
4. 在 transaction 内将 attempt 加一并标记 occurrence dispatching；
5. 使用 cancellable context 和 overall timeout 调 target；
6. 在 transaction 内持久化 success、retry 或 permanent failure。

可重试：HTTP 408/425/429/5xx、DNS/连接/读取网络错误、dispatch timeout。永久：其他非 2xx、target policy/invalid payload、caller cancellation。延迟为 capped exponential full jitter，并同时受 `max_attempts`、`max_backoff_seconds` 和 `max_retry_window_seconds` 限制。

投递 identity 由 tenant、schedule id/name 和 UTC scheduled instant 派生；每次重试/崩溃恢复保持相同 request/idempotency/trace identity。语义是 durable at-least-once，不是 exactly-once。

## 8. Lifecycle

- startup：配置校验 -> PostgreSQL connect/ping -> 可选 Redis DB 7 ping -> adapter 装配 -> wakeup start -> immediate cycle -> HTTP serve；
- readiness：Runtime accepting 且 PostgreSQL ping 成功；配置 Redis 时还要求 Redis ping；
- shutdown：先关闭 readiness/HTTP，取消 Runtime context，停止 gocron wakeup，并在 10 秒 deadline 内等待 in-flight cycle；
- crash：PostgreSQL claim expiry 后由其他 cycle 重领；attempt 已耗尽但未提交结果时写 `SCHEDULER_DISPATCH_RECOVERY_EXHAUSTED`。
