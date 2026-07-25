# Database Epoch v2 与统一 Goose Migrator

状态：Accepted architecture decision（2026-07-23）

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

本文冻结新架构的数据库物理边界、migration所有权和v1到v2升级方式。它替代其他蓝图中
“未来继续使用单一`public` schema和单一migration序列”的旧假设；既有v1 migration文件仍
保持不可改写。

## 1. 决策摘要

1. Embedded与split使用同一个PostgreSQL database contract。
2. IAM、API、Agent、Channel、Memory、Runtime、Model、Media分别拥有独立schema。
3. 每个owner拥有独立migration目录、从`00001`开始的版本序列和schema内版本表。
4. 全仓只有一个统一Migrator；业务进程启动时只验证兼容版本，不自行执行migration。
5. Epoch v2起使用`github.com/pressly/goose/v3` Provider API。
6. v1历史归档到`legacy/v1`，继续由当前`golang-migrate`兼容层识别；只移动源码位置，
   不重命名、不重排、不转换旧文件。
7. 新安装直接应用v2 baseline；旧安装先补齐v1，再执行唯一的v1到v2 bridge。
8. 跨owner写入只允许通过consumer-owned application contract；是否经RPC仅由真实进程边界决定。

## 2. Schema、角色与版本表

| Owner | PostgreSQL schema | Goose version table | 运行角色 |
| --- | --- | --- | --- |
| IAM | `iam` | `iam.goose_db_version` | `memoh_iam` |
| API | `api` | `api.goose_db_version` | `memoh_api` |
| Agent | `agent` | `agent.goose_db_version` | `memoh_agent` |
| Channel | `channel` | `channel.goose_db_version` | `memoh_channel` |
| Memory | `memory` | `memory.goose_db_version` | `memoh_memory` |
| Runtime | `runtime` | `runtime.goose_db_version` | `memoh_runtime` |
| Model | `model` | `model.goose_db_version` | `memoh_model` |
| Media | `media` | `media.goose_db_version` | `memoh_media` |

`memoh_migrate`拥有全部application schema对象，是唯一长期保留DDL和跨schema
upgrade/backfill权限的角色。部署login只负责创建cluster roles以及Goose运行前的schema
bootstrap，并可`SET ROLE memoh_migrate`；不得把该login或migration admin连接注入业务代码。
Embedded可以在同一进程组合多个owner pool，但每个adapter仍接收对应owner的连接/角色。
Split进程默认只持有自己schema及显式只读projection所需权限。

每个owner的`00001_baseline.sql`必须声明并配置自己的schema：`CREATE SCHEMA IF NOT EXISTS`、
`ALTER SCHEMA ... OWNER TO memoh_migrate`、`PUBLIC` revoke、业务role grant以及部署login和
`memoh_migrate`的default privileges全部属于owner baseline。IAM只配置`iam`，不得
替API、Agent等owner创建或授权schema。

所有migration SQL显式限定schema，例如`CREATE TABLE agent.messages`。不能依赖默认
`search_path`决定DDL落点。`public`只作为v1兼容区和PostgreSQL extension入口存在，v2业务表
不得继续新增到`public`。

## 3. Migration资产布局

```text
db/postgres/
  manifest.yaml                  # active owner顺序、期望版本、checksum、依赖
  <owner>/                      # iam/api/agent/channel/memory/runtime/model/media
    migrations/
      00001_baseline.sql
    queries/                    # handwritten SQL source；没有query时不创建
    sqlc.yaml                   # owner-local config；没有query时不创建
  legacy/v1/
    migrations/                  # 原v1文件；编号、文件名和内容永久冻结
    upgrade/to_v2/
      plan.yaml                  # preflight/expand/backfill/verify/cutover顺序与checkpoint
      sql/
```

Migration属于部署资产，物理位置不放入`domains/<owner>/internal`。Owner通过目录、manifest、
CODEOWNERS、对应SQLC target和integration tests表达。每个已发布文件不可修改、删除或重新
排序；修正只能追加下一版本。

`queries`与`sqlc.yaml`和migration同属owner数据库源资产，但生成的Go文件不放在`db`。每个
owner的SQLC `schema`输入只指向自己的`migrations`目录，而不是固定单个`00001`文件。不得追加
手写的跨owner table/view stub；跨owner读模型必须由各owner查询自己的schema，再由上层组合。
非IAM owner不得把IAM migration目录作为输入。
`iam.memoh_current_team_id()`是DDL/RLS运行时依赖，不要求SQLC读取IAM全量schema；
SQLC默认不做严格函数定义检查。Output固定进入
`domains/<owner>/internal/postgres/sqlc`，由owner PostgreSQL adapter封装；domain/use case不得
直接导入generated package。

不建立`epochs/v2`层：根下的owner目录始终表示当前有效schema contract，epoch是数据库状态和
升级协议，不是每次查询、生成或新增migration都要穿过的永久路径。`upgrade`也不是数据owner，
不能与`iam/api/...`并列；它只服务于仍受支持的v1来源，因此收在
`legacy/v1/upgrade/to_v2`。未来若再发生不兼容epoch切换，同样把旧active树归档到对应
`legacy/vN`，而不是让active路径累积版本层级。

根`manifest.yaml`只描述当前active owner streams及其依赖/目标版本；
`legacy/v1/upgrade/to_v2/plan.yaml`只描述从v1进入当前contract的一次性桥接。统一Migrator的
`up/status/verify`读取前者，`upgrade-v2`先读取legacy plan，再以根manifest验证/stamp各owner。
两份计划由同一程序编排，但生命周期和版本状态不混为一个migration stream。

从当前`db/postgres/migrations`归档到`db/postgres/legacy/v1/migrations`必须在epoch cutover
提交中一次完成。该提交只能修改embed/legacy upgrader/CI路径；移动前后逐文件checksum必须
完全一致，文件basename、编号和内容不得变化。已发布的旧binary使用编译时嵌入资产，不受源码
路径变化影响；新binary的legacy upgrader只读取归档路径。

开发阶段可以用timestamp创建候选文件，但进入main前必须固定为该owner的顺序编号。生产
Provider禁止`WithAllowOutofOrder(true)`，也禁止对已发布目录运行会重排编号的`goose fix`。

## 4. 统一 Migrator

统一Migrator为每个owner创建独立Goose Provider：

```text
Provider(platformFS, "iam.goose_db_version")
Provider(apiFS,      "api.goose_db_version")
Provider(agentFS,    "agent.goose_db_version")
Provider(channelFS,  "channel.goose_db_version")
Provider(memoryFS,   "memory.goose_db_version")
Provider(runtimeFS,  "runtime.goose_db_version")
Provider(modelFS,    "model.goose_db_version")
Provider(mediaFS,    "media.goose_db_version")
```

实现必须使用Provider API、独立`fs.FS`和schema-qualified`WithTableName`。不得使用全局
`goose.SetBaseFS`或隐式全局Go migration registry。各owner版本会重复从`00001`开始，因此
Provider必须启用`WithDisableGlobalRegistry(true)`，Go migrations只能显式注册到所属Provider。

Goose Provider在执行owner `00001`前会先检查/创建`<schema>.goose_db_version`，并在session
locker切到`memoh_migrate`后读取该表。因此统一Migrator必须在调用该owner Provider之前执行一次
幂等的`CREATE SCHEMA IF NOT EXISTS <owner>`；非IAM owner还要为部署login的新表/序列预置
授予`memoh_migrate`的default ACL，使Goose能够跨过首次ledger检查。这只是运行顺序所需的
bootstrap，不取代schema所有权：紧随其后的owner baseline仍重复声明该schema并完成ownership、
ledger和最终privilege normalization。v1到v2 bridge不重放这些baseline，因此bridge保留集中
创建schema、移动对象和stamp各owner ledger的步骤。

一次执行顺序由manifest声明，默认：

```text
iam -> api -> model -> media -> agent -> channel -> memory -> runtime
```

Migrator开始时获取repository-wide PostgreSQL advisory lock，全部owner完成并通过verify后
释放。Goose自身的owner级锁不能替代这个全局计划锁。失败时停止后续owner，不启动任何业务
进程；重新运行必须从已记录checkpoint安全继续。

建议CLI contract：

```text
memoh-server migrate up
memoh-server migrate status
memoh-server migrate verify
memoh-server migrate upgrade-v2
memoh-server migrate repair --owner <owner> --version <n>  # 高风险、显式审计
```

不继承`golang-migrate force N`为普通运维命令。Goose没有等价dirty/force模型；repair必须验证
checksum、实际schema和审批token，不能只改版本号。Production不提供“回滚全部migration”；
破坏性回退使用前向修复或恢复备份。

## 5. Embedded 与 Split

Embedded和split的schema、migration文件、checksum和目标版本完全相同，差异只在运行时由谁
持有哪个owner adapter：

- Embedded部署先运行统一Migrator，再启动单体；单体组合多个owner pool。
- Split部署同样先运行一个one-shot Migrator，再启动Server和child services。
- Server、Channel、Memory等进程不在startup hook中调用`Up`。
- 每个binary内置自己的`SchemaCompatibility{Epoch, Min, Max}`并在启动时只读校验。
- 版本不兼容必须fail fast；不得静默降级、自动repair或用运行时分支跳过校验。

同一database instance不允许以下捷径：

- owner直接写其他owner schema；
- 业务代码用跨schema transaction拼接多个owner use case；
- 新增跨owner foreign key来代替contract；
- Embedded直接跨owner写SQL、Split才调用application contract；两种profile必须走同一个业务contract。

跨owner可靠写入使用owner command和outbox/inbox。比如Channel保存
`channel.inbound_events`后，通过final inbound/Turn boundary由Server-local Agent追加`agent.messages`；Channel不直接
写`agent.messages`。Asset blob属于Media，message-asset link及其事务属于Agent。

## 6. v1 到 v2 升级

当前`db/postgres/migrations`和`public.schema_migrations`定义Epoch v1；cutover后源码归档为
`db/postgres/legacy/v1/migrations`，数据库中的版本表保持`public.schema_migrations`。切换步骤：

1. 获取全局advisory lock并进入维护/写入冻结窗口。
2. 用现有`golang-migrate`兼容层将旧实例补到最后一个批准的v1版本。
3. 校验v1 version、dirty状态、文件checksum、RLS/team和关键行数。
4. 执行v2 preflight并创建owner schemas、roles和权限。
5. 以expand/backfill/verify/cutover顺序迁移数据；大表使用checkpoint批次，不使用一个超大事务。
6. 对每个owner验证表、索引、约束、RLS、行数和业务invariant。
7. 通过受控baseline stamp把各owner标记为`00001`；不得通过伪造Goose历史跳过验证。
8. 写入epoch cutover marker，切换业务角色和连接。
9. 启动新binary并执行embedded/split smoke、RPC parity和schema compatibility检查。
10. 兼容窗口结束后，以后续v2 migration删除旧`public`对象和legacy读取路径。

新安装不执行v1历史，直接按manifest应用所有owner的`00001_baseline.sql`及后续版本。新安装
结果与升级结果必须通过schema dump规范化对比；对象、权限、RLS、默认值和索引必须一致。

`golang-migrate`依赖只保留在`legacy/v1` upgrader。等所有受支持旧版本都能先升级到v2，且发布
策略正式结束v1直升支持后，才从主Migrator删除该兼容层。

## 7. Goose 使用约束

Epoch v2固定使用`github.com/pressly/goose/v3`。选择Goose是因为其Provider API、独立version
table、embedded FS、显式Go migration和`slog`更适合多owner编排，不是因为
`golang-migrate`已停止维护。

Goose接入必须满足：

- 使用当前Go toolchain兼容版本，并在`go.mod`固定精确版本；
- 使用`database/sql`的PGX stdlib连接或专用migration连接，不能泄漏到业务pool；
- SQL migration默认在transaction内执行；只有PostgreSQL明确不允许时才能批准`NO TRANSACTION`；
- Go migration接受`context.Context`，支持取消、checkpoint和幂等重试；
- 每次运行输出owner、from/to version、duration和结果，不记录DSN或secret；
- manifest在执行前验证文件集合和checksum，未知、重复或被修改文件直接失败；
- standard Goose version table负责owner版本；额外审计表或custom Store记录checksum与执行身份。

## 8. 实施 Gate

Database Epoch v2位于Mechanical Move之后、owner SQLC/generated cleanup之前。进入cutover前：

1. table/query/transaction owner矩阵全部冻结；
2. domain已不依赖broad Queries和旧SQLC row；
3. consumer-owned application contract可承载全部跨owner写入；
4. owner schema、role、RLS和migration manifest完成评审；
5. v1到v2 bridge具有fresh、upgrade、resume、failure-injection和rollback/restore测试；
6. 新装baseline与升级后schema dump一致；
7. Embedded与split在同一schema versions上通过全量测试；
8. 旧`public`路径的删除有独立usage/telemetry Gate。

本Gate不能与机械目录Move、RPC wire change或业务行为修改混在同一review提交中。
