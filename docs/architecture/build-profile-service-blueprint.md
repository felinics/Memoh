# Build Profile 与 Business Owner 重构蓝图

状态：Direction Update Accepted；详细Move账本仍为Discussion Draft。目标 owner 根目录为
`domains/`（见 `process-boundary-rpc-decision.md`）；Channel根contract现状路径为 `domains/channel`。

用途：供团队讨论目录边界、编译产物、接口形态和迁移顺序。本文件不是已批准 spec，
也不是可以直接执行的批量 `git mv` 清单。

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

覆盖状态：81/81 个 `internal` 一级目录、115/115 个 Handler 文件、40/40 个
composition/release primary资产均已有唯一主审账本。2026-07-23已批准
`process-boundary-rpc-decision.md`：七个名称是业务owner，不是七个部署单元；首期只拆
Channel，command/Agent/Model/Memory/Runtime/Media业务留Server。旧账本中与该决策冲突的
per-owner `local/grpc` 和command迁Channel目标均被取代。最终inbound/Turn typed schema、
Asset/add-assets delivery AD1、D11/D12、跨owner AssetLink失败恢复和发布形态仍待闭合。

相关文档：

- `docs/architecture/process-boundary-rpc-decision.md`
- `docs/architecture/internal-module-map.md`
- `docs/superpowers/specs/2026-07-17-channel-boundary-design.md`
- `docs/architecture/migration/README.md`
- `docs/architecture/migration/api-http.md`
- `docs/architecture/migration/api-identity-bot.md`
- `docs/architecture/migration/agent-chat-turn.md`
- `docs/architecture/migration/channel.md`
- `docs/architecture/migration/memory-model.md`
- `docs/architecture/migration/runtime-media.md`
- `docs/architecture/migration/persistence-iam.md`
- `docs/architecture/migration/database-epoch-v2.md`
- `docs/architecture/migration/composition-release.md`
- `docs/architecture/migration/cross-module-review.md`

## 1. 当前目录是否已经整理完成

没有。

最新 main 已完成一部分运行边界建设，但尚未完成全局目录重构：

- 已建立 `cmd/channel`、`internal/rpc/channel/turn`、内部 gRPC 和架构守卫；
- 已将原 `cmd/agent/app.go` 的装配拆到 `cmd/internal/core` 与
  `cmd/internal/channel`；
- Channel/Pipeline 已禁止直接调用 Agent/Conversation，只能通过 Turn port；
- Workspace 已统一当前 runtime contract；
- 81 个 `internal` 一级目录仍大面积平铺；
- `handlers`、`conversation/flow`、`agent/tools`、`channel/inbound`、
  `db/store.Queries` 仍是跨域热点；
- `cmd/agent` 已通过 `profile_embedded.go` / `profile_split.go` 在编译期选择拓扑；
  `internal_rpc.shared_secret` 不再承担运行时 profile switch。
- `docker/Dockerfile.server` 和双进程 devenv 的 `.air.toml` 已显式使用 `-tags split`。
- `scripts/release.sh` 同时生成兼容文件名的 embedded `memoh-server`、显式
  `memoh-server-split` 和 `memoh-channel`，并写出 profile manifest/checksum；GitHub
  release workflow仍不调用该脚本，正式发布接线仍需单独批准。

本蓝图描述的是从这个“部分完成”的 main 迁移到目标结构的过程。

## 2. 目标与非目标

### 2.1 目标

1. 用目录和 Go import graph 建立真正的模块隔离。
2. 用一个全局 build tag 选择 embedded 或 split Server 构建产物。
3. Split Server只排除Channel implementation；API、Agent、Memory、Model、Runtime、Media始终
   编译在Server内。
4. Embedded直接调用Channel concrete，split通过Channel boundary RPC client实现同一
   consumer-owned port。
5. 将大模块组织为可独立测试的业务owner；只有真实独立进程才获得transport package。
6. Package leaf 使用单个清晰单词，避免 `providertemplates`、`channelruntime`、
   `grpctransport` 一类粘连命名。
7. 保持当前 Channel/Turn 已批准的数据所有权和依赖规则。
8. 用Database Epoch v2把逻辑owner落实为独立PostgreSQL schema、migration stream和运行角色。

### 2.2 非目标

- 本阶段不拆多个 `go.mod`；先保持单仓库、单 Go module。
- 不在一次提交中同时完成目录移动、业务拆分、SQL 迁移和协议变更。
- 不为每个子服务增加独立 build tag。
- 不通过 `common`、`utils`、`helpers` 汇总无 owner 代码。
- 不为未来可能拆分的owner预造standalone binary、`local`或`grpc`目录。

## 3. 核心设计决策

### 3.1 Build tag 只选择 composition profile

只定义一个 tag：`split`。

```text
default / !split  -> embedded profile
split             -> split Server profile
```

用于选择部署拓扑的 `split` build constraint 只允许出现在 `cmd/agent` 的 profile
文件中，不散落到业务包：

```text
cmd/agent/
  main.go
  common.go
  profile_embedded.go    //go:build !split
  profile_split.go       //go:build split
```

这条规则不限制已有的 OS/平台 build constraints，例如 Unix/Windows、Apple 或
container backend 的条件编译。Build tag 不是模块边界。模块边界由目录、`internal`
可见性、consumer-owned interface 和架构测试实现。

### 3.2 两种模式是两个构建产物

```bash
# Embedded：单体，包含 local implementation
go build -o memoh ./cmd/agent

# Split Server：只包含 contract + RPC client
go build -tags split -o memoh-server ./cmd/agent

# 唯一首期独立子服务
go build -o memoh-channel ./cmd/channel
```

一个二进制无法同时满足“运行时自由切换”和“不编译子服务实现”。仓库和发布物
同时支持两种模式，但每个具体二进制的拓扑在编译时确定。

### 3.3 只使用一个全局 split profile

禁止引入：

```text
split_channel
split_memory
split_agent
```

否则 N 个设想中的模块会产生 `2^N` 个构建组合。首期`split`的唯一含义是：Channel使用
独立进程，Server不编译Channel implementation。未来任何新child必须另立部署决策，而不是
自动套用本蓝图。

### 3.4 `domains/` 是代码所有权边界，不是部署清单

使用复数目录 `domains/`，所有路径小写：

```text
domains/channel
domains/memory
```

`domains/` 只表示代码所有权与领域隔离（Go package / import 边界），**不是** Go modules、
部署单元或进程。首期进程仍只有 Server 与 Channel；七个 owner 不自动获得 binary 或 RPC。
禁止在每个 owner 内复制 `controller/service/repository/domain` 四层模板，也禁止复制
`contract/local/grpc/internal` 部署模板。

现状事实：Channel根contract已迁入 `domains/channel`；Media根contract已迁入
`domains/media`。后续机械 Move 的目标 owner 根目录统一为 `domains/<owner>`。

### 3.5 Transport只跟随真实进程边界

```text
domains/<owner>/
  *.go                    # 业务逻辑、稳定value/error、consumer ports
  internal/               # 确需编译隔离的owner concrete/provider/postgres

internal/rpc/channel/
  client/                 # split process-boundary adapter
  server/                 # process host adapter
  pb/                     # final typed Server <-> Channel wire
```

Embedded composition直接构造owner concrete。Split Server只把Channel concrete替换为
`internal/rpc/channel/client`，并继续直接构造其余Server owner。非Channel owner没有第二种
transport实现，因此不建立`local` wrapper或RPC parity。

### 3.6 接口由 consumer 定义

不定义一个包含所有能力的 `Service` 巨型接口。每个 consumer 定义自己需要的
1-3 method port，例如 Turn 启动、Memory 读取、Message 发送、Runtime 查询。

Channel concrete 和 Channel RPC client同时实现跨边界consumer port；Server内部consumer直接
使用owner concrete。构造函数接受接口并返回具体struct。
所有执行I/O或可能阻塞的port以`context.Context`为首参并显式返回`error`；deadline由
consumer/caller决定，adapter只做local/wire映射，不偷偷增加另一套业务超时语义。

### 3.7 数据 owner 与物理 schema

各domain可以共享同一个PostgreSQL instance/database，但Epoch v2分别使用`platform`、
`api`、`agent`、`channel`、`memory`、`runtime`、`model`、`media` schema。Query、record、
transaction、schema role和写入规则必须有唯一owner：

| 数据语义 | Owner |
| --- | --- |
| User、Team、Membership、Bot/ACL/manager 配置 | API |
| Turn、Session、主动 Message、Compaction、全部 Message-Asset link transaction | Agent |
| 入站观察事件、被动 Message row insert、Channel route/identity、discuss cursor | Channel |
| Memory item、索引、provider 状态 | Memory |
| Workspace、Container、Network、Remote Runtime、container lifecycle/version event | Runtime |
| Model、Provider、Template、Capability | Model |
| Asset blob、Content hash、catalog、存储引用 | Media |

Consumer-owned store interface 放在 owner domain 内，SQLC 仅出现在该 domain 的
PostgreSQL adapter。禁止继续扩展 `db/store.Queries`。

手写migration/query SQL和owner SQLC配置放在`db/postgres/<owner>/{migrations,queries}`及
`db/postgres/<owner>/sqlc.yaml`；生成的Go package放在
`domains/<owner>/internal/postgres/sqlc`。`db`不承载可import的Go package，Store interface
与消费它的domain/use-case同包，具体PostgreSQL实现位于owner adapter。

每个owner拥有独立Goose migration序列和`<schema>.goose_db_version`，但全仓只有一个统一
Migrator。Embedded/split使用完全相同的schema contract；业务进程只验证版本，不执行migration。

### 3.8 Store boundary 先于目录移动

“先重构 Store”指先改变依赖方向和事务所有权，不是先全仓重写 SQLC 或搬迁生成文件：

1. consumer 在自己的 domain/use-case package 定义最小 Reader/Writer/Store；
2. owner PostgreSQL adapter 暂时基于现有 `internal/db/postgres/sqlc` 实现该 contract；
3. consumer 切换到窄接口，并从 `dbstore.Queries` 删除对应 method；
4. owner import graph 闭合后再移动业务 package；
5. 最后拆 query statement、建立 per-owner sqlc target、重新生成并删除旧 output。

禁止先把 `db/store` 重命名为另一个全局 Store，也禁止先移动 domain package 后继续从新路径
import broad Queries。Message、Session、HistoryTurn、Replacement、AssetLink、Compaction、
UserInput、ToolApproval和RuntimeFence是Agent transaction cluster，必须按原子用例/锁顺序
整体设计，不能拆成多个repository后在service层临时拼事务。

### 3.9 `internal` 的层级规则

根 `internal/` 只提供 repository-wide、无业务 owner 的应用基础设施；它只能阻止仓库外部
import，不能隔离同一 Go module 内的 Agent、Channel、Memory 等 domain。

| 位置 | 允许内容 |
| --- | --- |
| `domains/<name>/` | owner业务逻辑、稳定value/error和consumer-owned port |
| `domains/<name>/internal` | owner私有provider、postgres adapter、owner SQLC等concrete |
| `internal/rpc/channel` | 唯一首期Server/Channel process-boundary transport/wire |
| 根 `internal/` | owner database pools、统一Goose Migrator、operator config、logger、version、arch guards |
| `db/`、`conf/` | v1历史、v2 owner migrations、SQL/config source和部署资产；不是Go package namespace |

`cmd/**` 和其他owner不得直接导入 `domains/<name>/internal`；Server/Channel composition root
分别保留在 `cmd/agent` 与 `cmd/channel`，通过 owner 公开 constructor 或明确的公开 composition
入口构造。禁止建立目标 `internal/composition`、长期 `cmd/internal/process` / shared wiring 巨包、
`internal/platform/store`、`domains/shared`、`pkg/common` 或新的全局 Repository。

普通constructor优先；只有owner内部FX子图确实较大时，才按需建立
`domains/<name>/compose`薄入口。它可以导入同owner `internal`，但不得选择build profile、启动进程、
承载业务policy或成为新的composition root。`internal/rpc/channel`只能导入公开domain contract与
generated pb；protobuf类型不得进入domain。

目标树中的root `internal/oauth`、`tenant`、`timezone`均是`Keep/Decide`，不是因为“多个模块
都在用”就自动成为platform。它们必须逐个证明没有明确业务owner；认证principal/tenant
vocabulary若属于API Identity contract，应迁回owner，timezone只有保持纯值且无profile policy时
才可留在root internal。

## 4. 目标目录蓝图

```text
Memoh/
  cmd/
    agent/                 # Server composition root（embedded/split profile）
      main.go
      common.go
      commands.go
      http.go
      profile_embedded.go
      profile_split.go
    channel/               # Channel composition root
    bridge/

  domains/
    api/
      http/
      internal/
        identity/
          account/
          auth/
          link/
          member/
        bot/
          access/
          backup/
          setting/

    agent/
      turn.go
      message.go               # public stream/continuation values only
      internal/
        engine/
        chat/
          conversation/
          flow/
          message/
            event/
            convert/
          session/
            runtime/
            fence/
          compaction/
        acp/
          client/
          profile/
          feedback/
        tool/
        extension/
          hook/
          skill/
          plugin/
        automation/
          schedule/
          heartbeat/

    channel/
      channel.go
      internal/
        inbound/
        outbound/
        observe/
        discuss/
        route/
        identity/
        access/
        email/
        webhook/
          tunnel/
        adapter/

    memory/
      memory.go
      internal/
        provider/
        registry/
        recall/
        formation/
        segment/
        index/
        migrate/
        store/
          fs/
          wiki/

    runtime/
      runtime.go
      bridge/                    # Workspace data-plane; cmd/bridge must import it
        client/
        server/
        pb/
      internal/
        container/
          apple/
          containerd/
          docker/
        workspace/
        network/
        display/
        client/

    model/
      model.go
      capability/                # cmd/synccaps consumer; pure public catalog values
      internal/
        catalog/
        provider/
          copilot/
        template/
        fetch/
        search/
        audio/
        video/
        copilot/

    media/
      media.go
      attachment/                # cross-domain pure values
      internal/
        asset/
        storage/

  internal/
    arch/                  # repo-wide architecture guards
    rpc/
      channel/
        client/            # split Server/Channel process-boundary adapter
        server/
        pb/                # final typed contract only
    config/                # operator configuration only
    database/              # pool, migration runner; no domain query interface
    logger/
    health/
    oauth/
      client/
      context/
    tenant/                # pure tenant identity vocabulary if still required
    timezone/              # pure value helper if owner remains shared
    version/

  db/
    postgres/
      manifest.yaml        # active owner migration plan
      <owner>/              # platform/api/agent/channel/memory/runtime/model/media
        migrations/
        queries/
        sqlc.yaml
      legacy/v1/
        migrations/        # 原Epoch v1文件；编号/文件名/内容冻结
        upgrade/to_v2/     # 独立plan的一次性bridge；不是owner stream
```

图中只有`internal/rpc/channel`是首期控制面RPC。Memory/Model/Runtime/Media保持Server-local，
Channel不得直接调用它们；command、skill、speech和file业务由Server收到typed inbound后本地
执行。旧目标树中的per-owner `grpc/local`与Channel command目录已被
`process-boundary-rpc-decision.md`取代。

## 5. 编译装配蓝图

### 5.1 Common composition

`cmd/agent` 是 Server composition root；`cmd/channel` 是 Channel composition root。
`cmd/agent/common.go`装配API、Agent、Memory、Model、Runtime、Media、日志、配置和Server HTTP。
这些owner不随profile改变，也不注册为Channel反向RPC。Common不得importChannel concrete/
internal。Split profile可以装配`internal/rpc/channel/server`的Server-side inbound handler；
owner constructor应逐步下沉到各 domain 包公开的 composition 入口，`common.go`只组合，不枚举业务policy。
禁止把 wiring 沉淀为长期目标 `internal/composition` 或 `cmd/internal/process` / shared 巨包；
现状 `cmd/internal/*` 分区可过渡存在，但不得演化为新的共享 composition 层。

### 5.2 Embedded profile

```go
//go:build !split

package main

import (
    "github.com/memohai/memoh/domains/channel"

    "github.com/memohai/memoh/internal/config"
    "go.uber.org/fx"
)

func profileOptions(cfg config.Config) fx.Option {
    return channel.Module(cfg.Channel)
}
```

`channel.Module`仅示意公开composition入口，不要求实际API必须叫Module。Embedded直接构造
Channel concrete，不经过名为`local`的adapter层。

### 5.3 Split profile

```go
//go:build split

package main

import (
    channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
    channelserver "github.com/memohai/memoh/internal/rpc/channel/server"

    "github.com/memohai/memoh/internal/config"
    "go.uber.org/fx"
)

func profileOptions(cfg config.Config) fx.Option {
    return fx.Options(
        channelclient.Module(cfg.InternalRPC),
        channelserver.ServerInboundModule(cfg.InternalRPC),
    )
}
```

示例只表达import方向和双向边界：Server既调用Channel capability，也接收Channel inbound/Turn。
实际接口应从现有consumer提炼，不因为蓝图而预先制造空接口或统一Module abstraction。

### 5.4 配置语义

- `internal_rpc.shared_secret` 只表示 RPC 认证配置，不再决定进程拓扑。
- Embedded 构建不需要 RPC target；若提供 split-only 配置应给出明确 warning/error。
- Split Server只校验Channel target、secret、deadline和keepalive。
- 配置与 build profile 不匹配时 fail fast，禁止静默回退到 local implementation。
- Bare-metal 默认发布 embedded binary；Docker split topology 使用 split Server binary。

## 6. Main 已经完成的迁移

这些不是本蓝图的待办：

| 原位置/行为 | main 当前状态 | 说明 |
| --- | --- | --- |
| `cmd/agent/app.go` 巨型装配 | `cmd/internal/core/providers.go` + `cmd/internal/channel/providers.go` | 已按 composition concern 拆文件，但尚未形成稳定 domain 边界 |
| `conversation/flow/assistant_output.go` | `internal/rpc/channel/turn/assistant_output.go` | 已移动到 Turn contract 侧 |
| `conversation/flow/user_header.go` | `internal/rpc/channel/turn/user_header.go` | 已移动到 Turn contract 侧 |
| Channel 直接调用 `flow.Runner` | `channel/inbound -> agent/turn.Service` | 已完成依赖反转 |
| Pipeline 直接持有 Agent/Resolver | `pipeline -> agent/turn.Service` | 已完成依赖反转 |
| 单进程 Channel wiring | 新增 `cmd/channel` 与 RPC | 已成为正式 split 部署路径 |
| 仅靠约定保护边界 | `internal/arch/arch_test.go` | 已有机械守卫 |
| Workspace `LocalService`/`RuntimeRouter` | 当前统一 workspace contract | 旧实现已删除，不再迁移 |

## 7. 现有一级包迁移台账

说明：

- **Move**：owner 已明确，可在 contract 稳定后做机械移动。
- **Split**：当前 package 包含多个 owner，必须先拆代码。
- **Keep**：目标阶段保留位置或只改 basename。
- **Delete**：职责迁出后删除原 package，不建立同名转发层。

| 现路径 `internal/*` | 目标位置 | 类型 | 迁移说明 |
| --- | --- | --- | --- |
| `accounts` | `domains/api/internal/identity/account` | Split | 拆 principal/profile/credential 与 team membership；移除 Model 校验职责 |
| `acl` | API Bot Access + `domains/channel/internal/access` | Split | API 唯一写 ACL/default/manager 配置；Channel 只运行判定并读取 snapshot/port |
| `acpagent` | `domains/agent/internal/acp/runtime` | Move | ACP session pool/runtime |
| `acpclient` | `domains/agent/internal/acp/client` | Move | 进程与协议 client |
| `acpfeedback` | `domains/agent` root error detail + `internal/acp/runtime` | Split | Channel 所需反馈进入 Agent contract；内部错误分类不公开 |
| `acpprofile` | `domains/agent` root snapshot + `internal/acp/profile` | Split | 完整 profile 实现内收，只公开跨 domain 所需只读 snapshot |
| `agent` | `domains/agent/internal/engine` + `domains/agent` contract | Split | Engine/background/tools/turn 分开；Turn 细分见后文 |
| `agentpayload` | `domains/agent/internal/engine/payload` | Move | 当前无跨 domain consumer；修复复合包名并内收 |
| `apperror` | 各 `http`/`grpc` transport error mapper | Delete | Transport 状态映射不得作为共享 domain error |
| `arch` | `internal/arch` | Keep | 扩展 build profile、SQLC 和 domain import guard |
| `attachment` | `domains/media/attachment`（纯 wire）；data mount 归 `domains/runtime`；media access path 归 `domains/media` | Split | 跨 domain 纯值公开；业务包不依赖 containerfs |
| `audio` | `domains/model/internal/audio` | Split | Provider/model 归 Model；Channel transcription 走 port |
| `auth` | `domains/api/internal/identity/auth` | Split | JWT/token/session middleware 与 transport binding 分开 |
| `boot` | `internal/config` + `domains/runtime` composition | Split | Operator detection 与 Server-local Runtime construction 分开；不建立 `local` wrapper |
| `botbackup` | `domains/api/internal/bot/backup` | Split | 跨域读取改用窄 export/import port 后再移动 |
| `bots` | `domains/api/internal/bot` | Split | Bot registry owner 为 API；Agent 只消费 execution snapshot/reader |
| `capabilities` | `domains/model/capability` | Split/Move | `cmd/synccaps` 需合法导入公开 catalog；运行实现可留 Model internal |
| `channel` | `domains/channel` + `domains/channel/internal/*` | Split | 根 contract、store、gateway、outbound、adapter 分拆 |
| `channelaccess` | `domains/api/internal/bot/access` + `domains/api/internal/identity/link` | Split | Manage权限归Bot Access；link code/binding归Identity；Channel只消费判定结果或窄reader |
| `command` | `domains/agent/command` | Split | parsing、catalog、execution和result projection均留Server；Channel只提交typed inbound/interaction |
| `commandsyntax` | `domains/agent/command/syntax` | Move | Server-owned command parser/value；Channel只做平台payload normalization |
| `compaction` | `domains/agent/chat/compaction` | Move | Turn 历史产物归 Agent |
| `config` | `internal/config` | Split | 按 API/Channel/Agent/Memory/Runtime config 文件拆分，保持单 package 也可 |
| `container` | `domains/runtime/internal/container` | Move | adapter 下沉到 apple/containerd/docker |
| `contextfrag` | `domains/agent/internal/chat/context/fragment` | Move | 修复复合包名 |
| `contextlimit` | `domains/agent/internal/chat/context/limit` | Move | Tool/context 输出限制 |
| `conversation` | `domains/agent/chat/conversation` | Split | CRUD/value 与 UI conversion、Turn adapter 分开 |
| `copilot` | `domains/model/internal/provider/copilot` | Move | Provider integration |
| `db` | `internal/database` + 各 domain `internal/postgres` | Split/Delete | Pool/migration runner 保留；Queries/record/store 按 owner 下沉 |
| `decision` | `domains/agent/internal/decision` | Move | 进程内 waiter 后续再定义远程恢复 contract |
| `display` | `domains/runtime/internal/display` | Move | Workspace display/runtime capability |
| `email` | `domains/channel/internal/email` | Split | Provider/outbox/manager/OAuth store 按 Channel owner 重组 |
| `embedded` | `domains/api/http/static` | Move | Web static/embed surface |
| `fetchproviders` | `domains/model/internal/fetch` | Move | Provider 配置与 client 分开 |
| `handlers` | `domains/api/http/*` + Channel-owned HTTP | Split/Delete | 67 个文件按 owner 拆；详细映射见第 8 节 |
| `healthcheck` | `internal/health` + 各 domain health adapter | Split | 共享结果 contract 保留，checker 跟随 owner |
| `heartbeat` | `domains/agent/internal/automation/heartbeat` | Move | Agent automation |
| `historyfrag` | `domains/agent/chat/history/fragment` | Move | History context fragment |
| `hooks` | `domains/agent/internal/extension/hook` | Split | 执行逻辑归 Agent，Workspace 通过 port 回调 |
| `httpx` | `domains/api/http` | Move/Delete | 小 Echo helper 就近放置，不创建通用 helper 包 |
| `i18n` | `domains/agent/command/i18n` + Channel provider locale | Split | 业务command文案留Server；仅平台投递/错误文案跟随Channel adapter |
| `identity` | Channel identity + API Bot identity | Split/Delete | 当前同时校验 Channel/Bot ID 且依赖 Container error，原包应消失 |
| `logger` | `internal/logger` | Keep | 仅基础日志构造，不承载 domain policy |
| `mcp` | `domains/agent/mcp` | Split | Connection/tool gateway/OAuth transport 分开 |
| `media` | `domains/media` + `domains/media/asset` | Split | 根 contract 与 content-addressed 实现分开；cmd 经公开 constructors 装配 |
| `memory` | `domains/memory` + `domains/memory/internal/*` | Split | Provider SPI/Registry/CRUD/store 按子目录拆 |
| `message` | `domains/agent/chat/message` + Channel passive store | Split | Turn message 归 Agent；入站被动 message writer 归 Channel |
| `messageconv` | `domains/agent/chat/message/convert` | Move | 修复复合包名 |
| `messaging` | `domains/channel` sender contract + Agent tool adapter | Split/Delete | 保留消费方所需小接口，Executor 移到调用方 adapter |
| `models` | `domains/model` + `domains/model/internal/catalog` | Split | CRUD、client、probe、wire types 分开；移除 Channel 反向依赖 |
| `network` | `domains/runtime/internal/network` | Move | Overlay/provider 跟随 Runtime |
| `oauthclients` | `internal/oauth/client` | Move | 共享纯 client 配置；修复复合包名 |
| `oauthctx` | `internal/oauth/context` | Move | 共享纯 context value；修复复合包名 |
| `pipeline` | `domains/channel/internal/{observe,discuss}` | Split | 依照已批准 spec，入站观察投影与 Discuss trigger 按职责拆分 |
| `plugins` | `domains/agent/internal/extension/plugin` | Split | 执行归 Agent，管理 HTTP 归 API |
| `policy` | `domains/api/internal/bot/access` | Move | Guest/Bot access policy |
| `providers` | `domains/model/internal/provider` | Split | Catalog、credential、OAuth/client construction 分开 |
| `providertemplates` | `domains/model/internal/template` | Split | 先切 Store interface，禁止 SQLC 泄漏 |
| `prune` | `domains/agent/chat/text/prune` | Move | Agent/Chat text policy |
| `registry` | `domains/model/internal/template` | Split/Delete | YAML template catalog/sync 归 Template；运行 model catalog 保持独立 |
| `rpc` | `internal/rpc/channel/{client,server,pb}` + minimal shared auth | Split/Delete | 只保留真实Server/Channel边界；serverruntime行为并入final inbound/Turn后由Server本地owner执行 |
| `runtimefence` | `domains/agent/chat/session/fence` | Move | 修复复合包名并跟随 Session transaction |
| `schedule` | `domains/agent/internal/automation/schedule` | Move | Agent automation |
| `searchproviders` | `domains/model/internal/search` | Move | 修复复合包名 |
| `server` | `domains/api/http/server` | Move | Echo runtime、middleware、shutdown |
| `session` | `domains/agent/chat/session` | Split | CRUD、route、ACP、hook、fence 拆职责后移动 |
| `sessionruntime` | `domains/agent/chat/session/runtime` | Decide | 当前未进入生产依赖图；先决定接入、保留或删除 |
| `settings` | API Bot setting + Agent/Runtime consumers | Split/Delete | Network/ACL/ACP side effect 移到应用协调器 |
| `skills` | `domains/agent/internal/extension/skill` | Split | Runtime activation 归 Agent；配置管理 HTTP 归 API |
| `slash` | `domains/agent/command/slash` | Move | 业务command classifier留Server；Channel只标准化平台interaction |
| `storage` | `domains/media/storage` ports + `domains/media/internal/storage` providers + Runtime adapter | Split | 公开 ports 归 Media；concrete providers 为 owner-private；container access 由 Runtime adapter 实现 |
| `team` | `internal/tenant` 或 API tenancy contract | Decide | `DefaultTeamID` 仅 composition/DB 使用，业务 contract 显式携带 TeamID |
| `textutil` | 主要 consumer 的 `text` package | Split/Delete | 按实际调用方分配，禁止建立共享 utils |
| `timezone` | `internal/timezone` | Keep/Decide | 若保持纯 value helper 可共享，否则归 Identity/Profile |
| `toolapproval` | `domains/agent/tool/approval` | Move | Turn control |
| `userinput` | Agent root continuation contract + `domains/agent/tool/input` | Split | Answer value公开；waiter/execution内收 |
| `userruntime` | `domains/runtime/internal/client` | Move | Remote user runtime；修复复合包名 |
| `version` | `internal/version` | Keep | Build metadata |
| `video` | `domains/model/internal/video` | Move | Model capability |
| `webhooktunnel` | `domains/channel/internal/webhook/tunnel` | Move | Channel runtime owner 已明确 |
| `workspace` | `domains/runtime` + `domains/runtime/internal/workspace` | Split | Public runtime contract 与 Manager/Bridge/Postgres adapter 分开 |

## 8. 巨型 package 与关键文件拆分

### 8.1 `internal/handlers`

| 当前文件/文件族 | 目标 | 操作 |
| --- | --- | --- |
| `auth.go` | `domains/api/http/auth` | 只保留 HTTP bind/status mapping |
| `users.go` 的 User/Profile/Password | `domains/api/http/account` | 从 UsersHandler 拆出 AccountHandler |
| `users.go` 的 Bot CRUD/Create Stream | `domains/api/http/bot` | Bot 生命周期先移到 API application use case |
| `users.go` 的 Bot Channel config/send | `domains/api/http/channel` | 依赖 Channel runtime port，不直接依赖实现 |
| `message*.go`、`session*.go`、`local_channel.go` | `domains/api/http/chat` | Web/CLI local transport 明确归 API/Chat，不编入独立 Channel 进程 |
| `containerd*.go`、`display.go`、`workspace.go`、`user_runtime.go` | `domains/api/http/runtime` | 先抽 RPC 也使用的 Runtime/Skill port |
| `models.go`、`providers.go`、`provider_templates.go`、fetch/search/audio/video | `domains/api/http/model` | 只调用 Model ports |
| `memory*.go` | `domains/api/http/memory` | 直接调用Server-local Memory application port；不建立RPC parity |
| `acp*.go`、`mcp*.go`、tool approval | `domains/api/http/agent` | 管理面 transport，执行归 Agent |
| `email_*` 管理 API | `domains/api/http/email` | 管理 API 留 Server；外部 webhook endpoint 归 Channel |
| Channel webhook、Weixin QR、Email webhook、Public media | `domains/channel/internal/http` | 只在 Channel standalone/embedded host 编译 |
| `ping.go`、`swagger.go`、`error.go` | `domains/api/http/system` | System transport |

`internal/rpc/serverruntime` 当前直接使用 `handlers.ContainerdHandler`。第一步必须先抽
`SkillResolver`/Runtime tool port，使 HTTP 和 RPC 都依赖 application port，再移动文件。

### 8.2 Chat / Agent

| 当前代码 | 目标 | 前置工作 |
| --- | --- | --- |
| `conversation/flow/*` | `domains/agent/chat/flow` | 将 Email/Heartbeat/Identity/Memory gateway 换成 consumer ports |
| `conversation/uimessage_*.go` | `domains/agent/chat/message/convert` | 与 Conversation CRUD 分离 |
| `message/service.go` | Message store、History turn、Replacement、Asset writer | 先冻结事务边界再拆文件/package |
| `session/service.go` | Session store、route、ACP policy、hook coordinator | 先定义小接口再移动 |
| `agent/tools/*` | Agent tool contract + 各 capability adapter | Message/Memory/Runtime/Model adapter 跟随对应 owner |
| `agent/turn` | `domains/agent` 根 contract | 盘点现有行为后直接冻结唯一最终typed contract；不保留旧wire语义或兼容层 |
| `agent/turn/inprocess` | `domains/agent/internal/turn` 或 `domains/agent` 同包文件 | 并入Server-local Agent实现；不为单一实现保留 `local` wrapper |
| `agent/turn/grpctransport` | `internal/rpc/channel/{client,server}` | 仅保留Channel -> Server final inbound/Turn边界；当前wrapper不机械Move |
| `agent/turn/turnpb` | `internal/rpc/channel/pb` | 用最终typed inbound/Turn proto替换；不移动旧generated code |

### 8.3 Channel

| 当前代码 | 目标 | 前置工作 |
| --- | --- | --- |
| `channel/adapters/*` | `domains/channel/internal/adapter/*` | 更新 contract import，行为不变 |
| `channel/inbound/*` | `domains/channel/internal/inbound` | 将 Session/ACL/Command/Audio 依赖换成 ports |
| `channel/route/*` | `domains/channel/internal/route` | 移除对 Conversation concrete type 的现有豁免 |
| `channel/identities/*` | `domains/channel/internal/identity` | 归并 identity validator |
| Channel root outbound files | `domains/channel/internal/outbound` | 先拆 Manager method 与纯 chunk/render 函数 |
| `email/*` | `domains/channel/internal/email` | 保持 outbox/manager 事务语义 |
| `webhooktunnel/*` | `domains/channel/internal/webhook/tunnel` | 机械移动 |
| `cmd/internal/channel/*` | `cmd/channel` composition root + `domains/channel` 公开入口；process transport 归 `internal/rpc/channel/server` | 业务逻辑进 domain；进程装配留 cmd；禁止抽到 `internal/composition` 或长期 process/shared wiring 巨包 |

### 8.4 Memory

| 当前代码 | 目标 | 前置工作 |
| --- | --- | --- |
| `memory/adapters/provider.go` | `domains/memory/memory.go` 或 consumer port | 冻结最小 contract |
| `memory/adapters/registry.go` | `domains/memory/internal/registry` | 移除 singleton team fallback；与 provider SPI 分开 |
| `memory/adapters/service.go` | `domains/memory/internal/registry` + `postgres` | 切 Memory-owned Store 与 admin lifecycle |
| `memory/adapters/{builtin,mem0,openviking}` | `domains/memory/internal/provider/*` | 适配新 contract |
| `memory/memllm` | `domains/memory/internal/formation` | 覆盖 extract/decide/apply；Model client 通过 port 注入 |
| `memory/storefs` | `domains/memory/internal/store/fs` | 修复复合包名 |
| `memory/wikistore` | `domains/memory/internal/store/wiki` | 修复复合包名 |
| Agent memory tool | `domains/agent` consumer port | 两种Server profile都直接注入Memory concrete；无Memory RPC client |

### 8.5 Persistence

| 当前代码 | 目标 | 操作 |
| --- | --- | --- |
| `internal/db/db.go`、migration runner | `internal/database` | owner pools + 统一Goose Migrator；legacy upgrader暂留golang-migrate |
| `internal/db/store/queries.go` | 无 | 每迁移一个 consumer 就删除对应 method，最终删除 |
| `internal/db/store/contracts.go` | 各 consumer domain | Account/Bot/Message/Memory/Runtime contract 分别下沉 |
| `internal/db/postgres/sqlc` | 迁移期保持 | 第一阶段不搬生成路径，先阻止新增泄漏 |
| `internal/db/postgres/store/*` | 各 domain `internal/postgres` | 按 owner 移实现 |
| `db/postgres/queries/*` | `db/postgres/<owner>/queries/*` | 按statement owner拆分；同一旧文件可拆到多个owner，不按basename整体归属 |
| 根`sqlc.yaml` | `db/postgres/<owner>/sqlc.yaml` | 拆成owner-local config；统一命令可编排，但配置和output ownership独立 |
| `internal/db/postgres/sqlc/*` | `domains/<owner>/internal/postgres/sqlc/*` | 从owner query重新生成，不手工移动；仅owner postgres adapter可导入 |
| `db/postgres/migrations/*` | `db/postgres/legacy/v1/migrations/*` | epoch cutover时仅移动源码位置；basename/编号/内容/checksum冻结，不转换为Goose |
| 无 | `db/postgres/<owner>/migrations/*` | active owner从`00001`独立编号；统一Migrator按根`manifest.yaml`执行 |
| 无 | `db/postgres/legacy/v1/upgrade/to_v2/*` | v1 bridge；不与owner目录并列，不拥有schema/version table |

### 8.6 现有嵌套子包直接映射

| 现路径 | 目标路径 | 说明 |
| --- | --- | --- |
| `agent/background` | `domains/agent/internal/engine/background` | Agent-owned execution task |
| `agent/event` | Agent root `message.go` + `internal/engine/event.go` | Split；只公开 Channel/RPC 真正消费的 wire 字段 |
| `agent/sessionmode` | `domains/agent/chat/session/mode` | 修复复合包名 |
| `agent/tools` | `domains/agent/tool` + capability adapters | 先拆 contract/implementation |
| `agent/tools/internal/toolname` | `domains/agent/tool/name` | 单词 leaf |
| `agent/tools/internal/toolset` | `domains/agent/tool/set` | 单词 leaf |
| `agent/turn` | `domains/agent` 根 contract | 当前实现只作行为/字段盘点；直接形成唯一最终typed contract |
| `agent/turn/inprocess` | `domains/agent/internal/turn` | Server-local implementation；embedded/split均编译 |
| `agent/turn/grpctransport` | `internal/rpc/channel/client` + `server` | 仅用于Channel -> Server final inbound/Turn边界，不迁移旧wrapper |
| `agent/turn/turnpb` | `internal/rpc/channel/pb` | 最终typed protobuf重新生成；旧output删除 |
| `audio/adapter/edge` | `domains/model/internal/audio/edge` | Adapter 跟随 Audio owner |
| `botbackup/secure` | `domains/api/internal/bot/backup/secure` | 加密归 Backup owner |
| `channel/adapters/*` | `domains/channel/internal/adapter/*` | 平台 adapter 保持一平台一包 |
| `channel/common` | 无统一替代 | 按实际 owner 拆到 inbound/outbound/adapter，删除 common |
| `channel/identities` | `domains/channel/internal/identity` | Basename 与 package 统一 |
| `channel/inbound` | `domains/channel/internal/inbound` | 先切 Session/Command/Audio ports |
| `channel/publicmedia` | `domains/channel/internal/http/media` | Channel-owned public endpoint |
| `channel/route` | `domains/channel/internal/route` | 移除 Conversation import exemption |
| `channel/channeltest` | `domains/channel/internal/test/store` | 多个 Channel package 共用的编译型 test store；leaf为单词，不导入生产代码 |
| `channel/partsfixture` | `domains/channel/internal/test/fixture` | 多包共用 canonical parts fixture；仅测试消费 |
| `container/{apple,containerd,docker}` | `domains/runtime/internal/container/*` | Backend adapters |
| `container/provider` | `domains/runtime` composition | Server-local backend selection/wiring；不建立 `local` wrapper |
| `db/dbtest` | `internal/database/test` 或消费方 test helper | 仅测试可见 |
| `db/pgvector` | `domains/memory/internal/postgres/vector` | Vector schema/index 归 Memory |
| `db/postgres/store` | 各 domain `internal/postgres` | 按 owner 拆 adapter |
| `db/postgres/sqlc` | 迁移期保持 | 先阻止新增 domain import，再按 query owner 迁移 |
| `email/adapters/{generic,gmail,mailgun}` | `domains/channel/internal/email/{generic,gmail,mailgun}` | Channel runtime provider |
| `embedded/web` | `domains/api/http/static` | Embedded Web assets |
| `healthcheck/checkers/channel` | `domains/channel/internal/health` | Checker 跟随 owner |
| `healthcheck/checkers/mcp` | `domains/agent/internal/health/mcp` | Agent integration checker |
| `healthcheck/checkers/model` | `domains/model/internal/health` | Model checker |
| `i18n/locales` | `domains/channel/internal/i18n/locales` | Embedded locale assets |
| `mcp/sources/federation` | `domains/agent/mcp/federation` | MCP source implementation |
| `memory/adapters/*` | `domains/memory/internal/{provider,registry}/*` | SPI、registry、service 先拆职责 |
| `memory/memllm` | `domains/memory/internal/formation` | LLM extract/decide/apply |
| `memory/storefs` | `domains/memory/internal/store/fs` | 单词 leaf |
| `memory/wikistore` | `domains/memory/internal/store/wiki` | 单词 leaf |
| `message/event` | `domains/agent/chat/message/event` | Message event hub |
| `network/overlay` | `domains/runtime/internal/network/overlay` | Runtime network adapter |
| `rpc/channelruntime` | `internal/rpc/channel/{client,server}` | 只保留真实Server -> Channel capability并按transport角色拆分 |
| `rpc/serverruntime` | `internal/rpc/channel/{client,server}` final inbound/Turn | Command/Skill/Audio/Runtime行为留Server；不按owner拆RPC |
| `rpc/runtime` | 无 | Generic method dispatcher最终删除 |
| `rpc/runtimepb` | 无 | Generic protobuf由唯一Channel boundary typed contract取代 |
| `storage/providers/containerfs` | `domains/media/internal/storage/container` + Runtime port | 不直接依赖 Runtime implementation；cmd 经 asset.NewContainerFallbackService |
| `storage/providers/localfs` | `domains/media/internal/storage/local` | Local storage adapter；cmd 经 asset.NewLocalService |
| `storage/providers/fallback` | `domains/media/internal/storage/fallback` | Composition adapter（asset 内装配） |
| `workspace/bridge` | `domains/runtime/bridge/client`（已迁入） | Workspace data-plane client；与未来 Runtime control-plane RPC 分离 |
| `workspace/bridgepb` | `domains/runtime/bridge/pb`（已迁入） | Generated protobuf |
| `workspace/bridgesvc` | `domains/runtime/bridge/server`（已迁入） | `cmd/bridge` 必须能导入的公开 composition package |

## 9. 总体实施顺序

### Phase 0：冻结契约与基线

- 批准七个业务owner、Server/Channel两个首期进程和allowed-import。
- 记录 embedded 当前行为和 split 当前行为。
- 建立 `go list -deps ./cmd/agent` 依赖快照。
- 记录现有 `dbstore.Queries`、旧 SQLC 和跨 domain import consumer 清单；架构守卫禁止新增，
  exemption 只减不增。
- 冻结 query/table/statement owner 与 transaction/lock ordering，不移动文件。
- 决定 Web/CLI local Channel 永久归 API/Chat，而不是 Channel service。
- 决定 `sessionruntime` 保留、接入或删除。
- 冻结 passive Message insert 成功但 Agent AssetLink 失败时的 durable outbox、幂等键、
  重试、补偿和永久失败语义；在此之前不得拆这条事务路径。

### Phase 1：第一个 Store vertical slice

- 以Channel external identity facts和Channel-owned bot config/Matrix cursor statements为首个
  低风险slice，先补characterization/adapter integration test。明确排除API-owned
  `channel_identity_bindings.sql`与尚待批准的`user_channel_bindings` delivery preference。
- 在 consumer package 定义 typed store contract；domain value 不暴露 SQLC、`pgtype` 或泛化
  `Record/Input/map[string]any`。
- 在旧 SQLC 上实现 Channel-owned PostgreSQL adapter，切换 wiring。
- 每切一组 consumer 就从 broad Queries/contracts 删除对应 method/type。
- PostgreSQL characterization至少覆盖external identity稳定upsert/唯一性、bot config
  CRUD/disabled、Matrix cursor `updated_at`以及tenant/RLS；`//go:build ignore`测试不算Gate证据。
- 此阶段不移动 generated SQLC，不批量移动 Channel 目录。

### Phase 2：冻结并重构 Agent transaction cluster

- 统一审计 Message、Session、HistoryTurn、Replacement、AssetLink、Compaction、UserInput、
  ToolApproval 与 RuntimeFence 的事务/锁顺序。
- 优先定义原子 use-case store method；确需 transaction callback 时只传 Agent-owned aggregate
  store，不再传全局 `dbstore.Queries`。
- 删除 pool-less `InTx` 的“直接执行 callback”假事务语义，测试必须显式使用 fake transaction
  或真实 PostgreSQL transaction。
- 本 Phase 的 contract/test工作可与 Phase 1 分文件并行，跨 owner transaction实现必须在
  AssetLink恢复语义批准后合并。

### Phase 3：闭合 Channel persistence 与跨服务 ports

- 将 Channel identity、config、route、email、event、passive message store逐组下沉。
- API-owned Link/Access/Bot/Account 与 Agent-owned Session/AssetLink 只能通过窄 port访问。
- Command executor、Skill、Model、Memory、Runtime和Media业务固定留Server；Channel只准备标准化
  inbound/control value，不建立owner RPC。
- Web/CLI local transport 移入 API/Chat，删除 `ServerLocalModule`/local-first shim。
- 此时 Channel business graph 不再依赖 broad Queries、旧 Handler concrete type或其他 domain
  implementation；SQLC仍可暂时位于旧 generated package。

### Phase 4：Channel boundary Typed RPC

- 为Server/Channel真实进程边界批准唯一最终method/message、identity/team、deadline owner、status/detail、
  idempotency、payload limit、stream backpressure/half-close 和错误契约。
- RPC尚未发布，不设计mixed-version、dual-register、legacy fallback、v1/v2并存或协议迁移；
  Server/Channel首发必须使用同一build/version与同一份generated contract，原子升级并同时回滚。
  mismatch、service未注册或method `Unimplemented`均为部署错误；未闭合capability直接阻断切换。
- 新增`internal/rpc/channel/{client,server,pb}`，只承载Channel Admin/Delivery/Status/Email和
  final typed inbound/Turn boundary。
- 不新增API/Agent/Memory/Model/Runtime/Media owner grpc/local。Channel -> Server只提交标准化
  inbound/control，Server内部使用本地Go调用。
- 从`cmd/internal/channel`把 Channel 业务 concrete 下沉到 `domains/channel` 公开入口；
  composition root 仍留在 `cmd/agent` / `cmd/channel`。不建立长期`domains/channel/local`
  wrapper、目标 `internal/composition` 或长期 `cmd/internal/process`/shared wiring 巨包，
  也不把command executor迁进Channel。
- Embedded direct与split Channel RPC使用同一consumer contract/fixture；此阶段仍不批量移动外部adapter。

### Phase 5：引入 build profile 与发布产物

- 将 `cmd/agent/module.go` 的运行时选择拆为两个 tagged profile文件。
- Embedded profile直接导入Channel composition；split profile导入`internal/rpc/channel`的client
  及Server-side inbound handler。
- `internal_rpc.shared_secret` 从 topology switch改为 transport config；配置不匹配fail fast。
- 更新 Docker/Air/Compose/CI/release，生成明确的 embedded、split Server和Channel产物。
- dependency guard证明split Server不包含Channel `internal`、旧Channel implementation或外部adapter。

### Phase 6：机械移动

- 在依赖方向已经正确后，将 Channel adapter、gateway、outbound、email、webhook/http 等实现
  机械移动到 `domains/channel/internal`；Move提交不混合行为变化。
- 每批只移动单一 owner、已有行为测试覆盖且 import 方向已经闭合的 package family。

### Phase 7：Database Epoch v2

- 冻结所有table/query/transaction owner后，由统一Goose Migrator编排owner顺序并在每个
  Provider前bootstrap该owner schema；角色由Platform cluster bootstrap创建，schema ownership、
  privileges和独立`goose_db_version`由各owner baseline闭合。v1到v2 bridge保留集中创建和stamp。
- Embedded与split应用同一manifest；业务进程只做schema compatibility检查。
- Fresh v2 baseline、v1 upgrade、checkpoint resume和schema dump parity全部通过后才允许cutover。

### Phase 8：Generated cleanup

- 按 statement owner 拆 query，建立 per-owner sqlc target并重新生成，不手搬 generated文件。
- 删除旧 `dbstore.Queries` method、旧 SQLC output、空 package与临时 facade。

### Phase 9：整理 Memory owner

- Store/registry/transaction boundary先行，移动到`domains/memory`并清理generated output。
- Memory继续Server-local；本轮不建立`cmd/memory`、typed RPC或修改split profile。

### Phase 10：Runtime、Model、Media、API/Agent owner整理

- 每个owner遵循Store boundary -> Move -> Database Epoch v2 owner cutover -> generated cleanup；
  只有另行批准真实进程边界时才插入RPC/profile Gate。
- Model/Media/Runtime/API/Agent保持Server-local，禁止预造RPC、`local`或standalone binary。
- `cmd/agent`改名、API/Agent独立进程和多`go.mod`均另立spec，不扩大Channel/Memory首轮范围。

## 10. Allowed-import 与编译守卫

必须机械执行：

```text
cmd/agent common
  !-> domains/*/internal
  may -> domains/{api,agent,memory,model,runtime,media}
  !-> domains/channel/internal

cmd/agent split profile
  may -> internal/rpc/channel/{client,server,pb}

domains/<a>/internal
  !-> domains/<b>/internal

domain/service code
  !-> handlers/server/fx
  !-> db/postgres/sqlc        # 迁移期 exemption 只减不增
```

Split dependency 验收：

```bash
go list -deps -tags split ./cmd/agent >/dev/null
! go list -deps -tags split ./cmd/agent \
  | rg 'domains/channel/internal|internal/channel|internal/email|internal/webhooktunnel'
```

第一条先证明 package graph 可解析；第二条必须无匹配输出。实际 CI 脚本还应启用
`pipefail`，避免把 `go list` 失败误判为“没有 forbidden dependency”。注意
`go test -tags split ./...` 会枚举并编译仓库中的所有 package，
它不能证明 Split Server binary 没有依赖子服务实现。

## 11. CI 与验收矩阵

以下是对应迁移Phase完成后的目标命令；`domains/*`尚未创建时，不得提前把package命令加入
main CI。本轮没有`cmd/memory`、`cmd/runtime`等child命令。

```bash
# Package graph
go list ./...

# Embedded Server
go build ./cmd/agent
go test ./cmd/agent ./domains/... ./internal/arch

# Split Server
go build -tags split ./cmd/agent
go test -tags split ./cmd/agent ./internal/arch

# Child process
go build ./cmd/channel

# Compile isolation
./scripts/check-split-deps.sh

# Protocol and behavior parity
go test ./domains/channel/... ./internal/rpc/channel/...

# Repository quality gates
mise run lint
go test ./...
```

每个Channel boundary RPC stream必须验证context cancellation、deadline、尾事件drain、错误身份
保留、graceful shutdown和超过grace deadline后的强制停止。Embedded direct与split RPC共享对拍fixture。

## 12. 发布

- 默认 `memoh` 保持 embedded，避免破坏裸机升级路径。
- Split Server 使用独立产物名或镜像 target，不能靠同一 binary 的运行时配置切换。
- 现有 `[channel]` 产品配置保持稳定；未发布的 `[internal_rpc]` 配置可直接替换为最终split配置，
  不保留deprecated alias、运行时topology switch或fallback。
- 配置与构建 profile 冲突时明确失败，不静默降级。
- RPC首发只生成、注册和调用一套最终Proto；实现期可直接修正未发布字段，不reserve从未发布的
  field，也不保留旧service/package/DTO。后续若已发布协议需要演进，必须另立决策，不属于本迁移。
- Epoch v1 history不重写；Epoch v2通过统一Goose Migrator迁入owner独立schema/version table。

## 13. 主要风险

| 风险 | 后果 | 控制措施 |
| --- | --- | --- |
| `split` build tag 散落 | IDE/测试看到不同拓扑代码世界 | topology tag 只放 `cmd/agent/profile_*.go`；OS/platform tag 不受影响 |
| 每服务独立 tag | 构建组合爆炸 | 只保留全局 `split` |
| 业务owner被误当部署单元 | 无需求的proto/client/server和版本矩阵持续增长 | 只有真实Server/Channel边界拥有RPC |
| Web/CLI 仍依赖 Channel implementation | Split Server 无法裁剪 Channel | Web/CLI transport 明确迁入 API/Chat |
| Channel 继续复用 Core foundation | Child service 编译整个 Server domain | 切 Channel-owned store/RPC ports |
| RPC contract 复制 domain struct | 协议与domain同步困难 | RPC adapter显式映射；Server内部不经过wire type |
| DB interface 一次性拆除 | 大面积事务回归 | 按 consumer/store 一条链迁移 |
| 目录移动与行为修改混合 | Review 无法判断回归来源 | Move、contract、behavior 分开提交 |

## 14. 团队需要拍板的问题

建议默认答案写在第二列：

| 问题 | 建议答案 |
| --- | --- |
| 顶层叫 `services` 还是 `domains`？ | `domains`，表示代码所有权/领域隔离；不是 Go module、部署单元或进程。Channel/Media根contract已落在该目标根下 |
| Embedded/split 是否是同一 binary 运行时切换？ | 否，是两个 build artifact |
| 是否为每个服务增加 tag？ | 否，只使用一个全局 `split` |
| Web/CLI local Channel 归谁？ | API/Chat，不属于外部 Channel service |
| ACL/default/manager 配置谁写？ | API/Bot Access 唯一写；Channel 只读取并执行判定 |
| Message asset link 谁写？ | 全部归 Agent Message transaction；Media 只管 blob/catalog |
| Passive insert 与 AssetLink 可否最终一致？ | 仅在 durable outbox + 幂等/补偿契约获批后允许；否则重划整个 append aggregate owner |
| RPC 默认 deadline 谁决定？ | Consumer/caller；client adapter只做协议映射，具体数值在各 process-boundary capability spec 冻结 |
| Split Channel 是否继续 import Core foundation？ | 否，改为自身 Store/RPC ports |
| 第一批拆哪个模块？ | Channel，现有 Turn/RPC/spec 基础最好 |
| 第二批拆哪个模块？ | 当前不预定；Agent或其他owner只有出现真实独立进程需求后另立决策 |
| 是否立即拆多个 go.mod？ | 否，先用单 module + Go `internal` 强制边界 |
| 是否远程化 Model/Memory/Media/Runtime/API/Agent？ | 首期否；保留Server-local，禁止预造RPC/local层 |
| `cmd/agent` 是否同时改名？ | 否，避免扩大首轮变更面 |
| 裸机 archive 如何过渡？ | 一个版本同时提供 embedded `memoh-server`、split `memoh-server-split`、`memoh-channel` |
| GitHub Release 是否发布 Go binary archive？ | 需明确；当前 workflow只发source/npm/Desktop，不调用`release.sh` |
| 首轮是否拆独立 Channel OCI image？ | 默认仍由一个Server image携带两个独立binary，先完成编译隔离 |
| Embedded binary遇到non-empty secret？ | 建议hard error并提示使用split binary；是否给一个warning过渡期需批准 |
| `version` 是否暴露build profile？ | 建议增加machine-readable profile，供CI和诊断 |
| `user_channel_bindings` 谁写？ | 建议API写、Channel读projection；批准前禁止双写 |
| dormant Media catalog是否接入？ | 在Media Gate前决定接入或删除，不先机械搬迁 |
| disabled mem0/openviking如何处理？ | 先查存量配置兼容要求，再决定保留unsupported adapter或删除 |
| legacy Audio Edge adapter是否保留？ | 当前无production调用点；Model Gate前以配置/存量证据决定删除或迁入 `audio/edge` |
| Memory graph HTTP projection放哪？ | Memory contract返回完整graph或API presenter暂留必须二选一；对应Handler Move前批准 |
| Swag schema rename如何处理？ | 建议显式稳定schema名，避免目录迁移无意重命名SDK types；generated Gate前批准 |

## 15. 蓝图批准条件

在进入实现计划前，至少完成以下评审：

1. API、Agent、Channel、Memory、Runtime、Model、Media owner 无重叠写权限。
2. Web/CLI local transport 归属确定。
3. Embedded 与 split artifact/config/release 命名确定。
4. Channel Phase 1-4 所需的 Store/transaction/RPC port 可枚举，且业务代码不再依赖 broad Queries。
5. Split dependency guard 的禁止前缀确定。
6. 现有 Channel/Turn spec 与本蓝图没有冲突，或者冲突处有替代决策记录。
7. 每个迁移 Phase 可以独立合并、独立回滚并保持 main 可构建。
8. 只有Channel process boundary使用typed RPC；final inbound/Turn已有可独立实现的协议spec。
9. 跨 owner 的 passive Message/AssetLink 失败恢复语义已经批准并有 characterization test。
10. Database Epoch v2的owner schema/role/version table、Goose manifest和v1 bridge已经独立评审。

蓝图批准后，再为 Phase 1 生成逐文件 implementation plan；在批准前不创建新目录
scaffolding，也不移动生产代码。
