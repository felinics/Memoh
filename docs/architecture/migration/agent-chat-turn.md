# Agent / Chat / Turn 目录迁移蓝图

状态：Direction Update Accepted；详细Move账本仍为Discussion Draft

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

审计范围：Agent engine、Turn业务contract与Server/Channel边界映射、ACP、Conversation/Flow、Message、
Session、Compaction、运行期决策、MCP、扩展和自动任务，以及这些包对应的 composition、
PostgreSQL query/contract/migration、proto/generated 和测试。

## 1. 结论先行

当前目录**没有整理好**。本范围当前包含 39 个 package 目录、408 个 Go 文件，其中
219 个生产文件、189 个测试文件。目标结构不能通过一次 `git mv` 得到，原因是四个
现有热点同时包含多个职责：

- `internal/message/service.go`（2349 行）同时实现写入、History Turn、Replacement、
  Asset link、查询和 SQLC row conversion；
- `internal/session/service.go`（1537 行）同时实现 Session CRUD、Route active session、ACP
  descriptor/policy、Subagent fork/config 和 Hook side effect；
- `internal/conversation/flow` 的 43 个生产文件同时承担 Turn orchestration、模型选择、
  history/compaction、continuation、automation gateway 和外部服务 lookup；
- `domains/agent/tools` 的 30 个生产文件把 Agent tool contract 与 Channel、Memory、
  Model、Media、Runtime 等具体 capability adapter 放在同一个 package。

迁移应保持一个业务owner：`domains/agent`。这里的`domains`表示代码 owner/领域隔离，不是部署服务，也不表示Agent是
独立进程，更不是`controller/service/repository`分层。Agent loop、Command、Turn、Memory/Model/
Runtime/Media调用在embedded和split两种Server构建中都保持进程内。只有Channel独立进程通过
`internal/rpc/channel`调用Server能力；不得为Agent复制`local/grpc`部署模板。

## 2. 目标目录

```text
domains/agent/
  turn.go                         # StartTurnCommand/Event values; RunHandle由consumer定义
  message.go                      # Turn-owned wire message/attachment/value types
  continuation.go                 # approval/input continuation commands and results
  internal/
    turn/                         # Server-local flow adapter/conversion/idempotency
    engine/                       # native Agent loop
      background/
    acp/
      runtime/
      client/
      profile/
      feedback/
    chat/
      conversation/               # conversation aggregate and store-facing app service
      flow/                       # Turn orchestration only
      message/
        write.go                  # message/history-turn mutation use cases
        read.go                   # read models
        turn.go
        replacement.go
        asset.go
        event/
        convert/
      session/
        service.go
        route.go
        subagent.go
        descriptor.go
        mode/
        fence/
        runtime/                  # only if sessionruntime is retained and connected
      history/
        fragment/
      context/
        fragment/
        limit/
      compaction/
      text/
        prune/
    tool/
      catalog/                    # tool contracts, names, registration and snapshots
      approval/
      input/
      adapter/                    # Agent-owned adapters consuming other domain module ports
    decision/
    mcp/
      connection/
      gateway/
      oauth/
      protocol/
      federation/
    extension/
      hook/
      skill/
      plugin/
    automation/
      schedule/
      heartbeat/
    postgres/                     # Agent-owned ports' PostgreSQL implementation

internal/rpc/channel/
  client/                         # Server <-> Channel process-boundary adapters
  server/
  pb/                             # final typed inbound/Turn wire
```

`domains/agent`的公开文件只保留确有跨包consumer的稳定value/error。Agent concrete由Server
composition直接构造，不增加单实现`local` wrapper。`internal/rpc/channel`把final typed wire
映射到Agent/command application；generated pb不得进入Agent业务包。

## 3. 当前与目标依赖

当前主链：

```text
Channel/Pipeline
  -> domains/agent
  -> internal/rpc/channel/turn/{grpctransport,turnpb} (temporary process boundary)
  -> domains/agent/application
  -> internal/agent/runtime/{native,acp}
  -> domains/agent/tool
  -> Channel/Memory/Model/Media/Runtime concrete packages

conversation/flow
  -> 44 internal packages
  -> db/store.Queries + db/postgres/sqlc

message/session/compaction/decision/toolapproval/userinput
  -> db/store.Queries + db/postgres/sqlc
```

目标主链：

```text
API adapter / Server-side Channel boundary handler
  -> domains/agent (Turn/command application)
  -> domains/agent/chat/flow
  -> domains/agent/internal/engine OR internal/acp/runtime

Channel process
  -> internal/rpc/channel client
  -> internal/rpc/channel server
  -> Server-side Agent/command application

chat/flow
  -> consumer-owned ports (model, memory, media, runtime, channel, identity)
  -> chat/message + chat/session + chat/compaction

postgres adapters
  -> consumer-owned store ports
  -> generated SQLC (implementation detail)
```

禁止的新边：

- Agent `internal` 不得 import 其他 `domains/<name>/internal`；只能 import 对方根 contract；
- 根 `domains/agent` 不得 import Conversation、SQLC、Echo、FX、Channel 或实现包；
- `internal/rpc/channel`的client/server不得拥有Agent业务policy；pb不得被业务包import；
- `chat/*` 不得 import Handler、RPC transport、FX；
- 迁移完成后 `chat/*` 不得 import `internal/db/store` 或 `internal/db/postgres/sqlc`；
- `cmd/**` 只装配，不拥有 conversion、transaction 或业务 policy。

### 3.1 逐热点 import 改写

| 当前 importer | 当前直接 import | 目标依赖 |
| --- | --- | --- |
| `agent/turn` | `attachment`, `conversation`, `userinput` | 全部删除；根 `domains/agent` 自有 command/value/error |
| `turn/inprocess` | `agent`, `conversation`, `conversation/flow`, `pipeline`, `session`, `userinput` | `domains/agent/internal/turn`只import Agent root contract和内部consumer ports；Pipeline concrete dependency删除 |
| `turn/grpctransport` | `acpfeedback`, `session`, `userinput`, Turn/pb | 行为盘点后由`internal/rpc/channel`最终typed handler/client取代；不建立Agent transport package |
| `agent` | `background`, `event`, `sessionmode`, `tools`, `attachment`, `contextfrag`, `contextlimit`, `hooks`, `models`, `prune`, `skills`, `toolapproval`, `userinput`, `workspace/bridge` | engine internal + ToolCatalog/HookRunner/ModelGateway/RuntimeGateway/ApprovalGateway/InputGateway consumer ports |
| `agent/tools` | 36 个 internal package，含 Channel/Email/Memory/Model/Media/Runtime/DB/SQLC | catalog不 import capability实现；每个 adapter只 import自己的 consumer port和对方 service root contract；DB/SQLC为零 |
| `conversation` | `db`, `db/store`, `db/postgres/sqlc`, `message`, `agent/event`, `attachment`, `userinput`, `textutil` | conversation `Store` + Agent-owned values；SQLC和UI/message conversion移出 |
| `conversation/flow` | 44 个 internal package | flow-owned ports + Agent内部 chat packages；Server owner通过consumer port直接注入concrete，DB/SQLC为零 |
| `message` | `db`, `db/store`, `db/postgres/sqlc`, `message/event`, `runtimefence` | domain store/query ports + event publisher + FenceRunner；SQLC只在 Agent postgres adapter |
| `session` | `acpprofile`, `db`, `db/store`, `db/postgres/sqlc`, `hooks`, `message/event`, `runtimefence` | SessionStore/ACPPolicyReader/HookRunner/Publisher/FenceRunner；SQLC只在 postgres |
| `compaction` | `db*`, `conversation`, `message`, `models`, `hooks`, `userinput`, context/history | CompactionStore/Summarizer/HookRunner + internal context/history values；SQLC rows不出 adapter |
| `decision`, `runtimefence` | `db*`, SQLC lock params | pure domain/context留原目标；事务与锁完全下沉 `internal/postgres` |
| `toolapproval`, `userinput` | Channel/Settings/Hooks、`db*`、Decision/Fence | policy/interaction domain + consumer ports；Channel/Settings concrete import和SQLC移除 |
| `mcp` | `db*`, Agent event, context limit, fence, textutil | gateway domain + Connection/OAuth stores + ToolEvent/Fence ports；SQLC只在 postgres |
| `hooks`, `skills`, `plugins` | Workspace bridge、Config、DB/SQLC、彼此 concrete service | RuntimeFileClient、PluginLister、SkillInstaller、MCPManager和各自 Store ports |
| `schedule`, `heartbeat` | Auth/Boot、DB/SQLC | Triggerer、SessionCreator、Store、RuntimePolicy snapshot；Auth/Boot concrete import删除 |

迁移期每删除一条 broad import就收紧 `internal/arch` exemption；不允许先引入新的
`domains/agent/internal/common` 来隐藏同样的 fan-out。

## 4. Package 与生产文件映射

操作定义：

- **Move**：文件职责和 owner 已一致，可在 import 更新批次机械移动；
- **Split**：必须先按下表拆函数/type/port，不能原文件原样移动；
- **Keep**：本轮保持原位置，等待明确 gate；
- **Delete**：职责迁完后删除原 package，不留转发 package。

### 4.1 Turn contract、Server-local实现和进程边界映射

| 当前生产文件 | 目标文件 | 操作 | 变化 |
| --- | --- | --- | --- |
| `internal/rpc/channel/turn/turn.go` | `domains/agent/turn.go`、`message.go`、`continuation.go` + consumer `port.go` | Split | 只有command/event/error value留Agent根；consumer定义最小interface；去掉对`conversation`、`attachment`、`userinput`的alias/import |
| `internal/rpc/channel/turn/assistant_output.go` | `domains/agent/message.go` | Move | Assistant output 解析跟随 Turn wire vocabulary |
| `internal/rpc/channel/turn/user_header.go` | `domains/agent/message.go` | Move | headerified user text 是 Turn command normalization |
| `internal/rpc/channel/turn/inprocess/{adapter,convert,discuss,idempotency}.go` | `domains/agent/internal/turn/*.go` | Split/Move | Server-local实现；embedded/split均编译，不保留`local`层 |
| `internal/rpc/channel/turn/grpctransport/{client,server,feedback}.go` | `internal/rpc/channel/{client,server}/*.go` | Rewrite/Split | 只盘点行为与stream/error mechanics；按final typed process-boundary contract重写，不机械移动旧wrapper |
| `internal/rpc/channel/turn/turnpb/turn.proto` | `internal/rpc/channel/pb/turn.proto` | Replace/Generate | 现有proto只作字段盘点；直接生成唯一最终typed schema，不保留旧package/field约束 |
| `internal/rpc/channel/turn/turnpb/{turn.pb.go,turn_grpc.pb.go}` | `internal/rpc/channel/pb/*` | Replace/Generate | 从唯一最终proto重新生成；不移动旧generated code，不手改 |

Turn 精确去 alias 要求：

- 删除`Attachment`、`OutboundAssetRef`跨owner alias；最终root/wire附件只引用唯一
  `memoh.media.AssetRef`。`InjectMessage`、`DiscussMessage`、`DiscussImageRef`仅在确属
  Agent-owned typed command时迁入`domains/agent/message.go`；
- `SkillActivation`、`SkillActivationSkill`、`RequestedSkillContext` 迁入根 Turn value，
  Conversation 内部通过Server-local converter使用；
- `ModelMessage`、`AssistantOutput`、`ContentPart`、`ToolCall` 若仅 engine/chat 消费，放
  `domains/agent/chat/message/convert`，不得因当前 alias 就扩大 public API；
- `QuestionAnswer`、`AdvanceTextInput/Result` 迁入 `continuation.go` 的 owned wire type；
- direct path与RPC boundary handler均转换同一根command，禁止绕过根contract直接暴露
  `conversation.ChatRequest`。

Channel detach不保留现有`memoh.turn.v1.TurnService` wire，也不建立typed Turn v2迁移。
RPC未发布，必须在首发前直接冻结唯一最终`memoh.turn.TurnService`及typed event/control schema。
Channel consumer的准确接口位于`domains/channel/internal/agentturn/port.go`，
base `RunHandle`只使用`Events/Errors`和context-aware `Inject/Cancel`；`AddOutboundAssets`属于
独立可选`OutboundAssetCollector`且AD1前Blocked。`EventSink.Emit`在buffer满时阻塞并由ctx解除。
control ACK必须在最终schema中明确，不能把旧wire的nil/admission语义带入首发。
关闭顺序、tail、half-close和backpressure以
`service-rpc-channel.md`第5.3/7节为准。
现有`AddOutboundAssets` DTO仍依赖`storage_key`；新root `AssetRef`不暴露它，因此
add-assets RPC被AD1阻断。不得保留旧mapper/DTO或用storage key建立跨owner contract。

### 4.2 Native Agent engine 与 background

| 当前生产文件族 | 目标 | 操作 | 职责 |
| --- | --- | --- | --- |
| `internal/agent/agent.go`, `config.go`, `guard_state.go`, `retry.go`, `sential.go`, `stream.go`, `types.go` | `domains/agent/internal/engine/` 同名文件 | Split | Agent loop、limits、guard、retry、stream；将 tool/hook/runtime 依赖改成消费方小接口 |
| `internal/agent/prompt.go`, `context_frag.go` | `domains/agent/internal/engine/prompt.go`, `context.go` | Split | prompt assembly 依赖 chat/context contract，不直接知道 persistence |
| `internal/agent/attachment_bundle.go`, `read_media.go` | `domains/agent/internal/engine/media.go` | Split | 只保留 engine 需要的 Media reader port；bundle/value owner 由 Media contract 提供 |
| `internal/agent/fs.go` | `domains/agent/internal/engine/fs.go` | Move | engine filesystem callback |
| `internal/agent/hooks.go` | `domains/agent/internal/engine/hook.go` | Split | 改依赖 `HookRunner`，具体 hook service 由 local wiring 注入 |
| `internal/agent/resolve_result.go`, `tool_execution_metadata.go` | `domains/agent/internal/engine/result.go`, `tool.go` | Move | execution result/metadata normalization |
| `internal/agent/spawn_adapter.go` | `domains/agent/internal/engine/spawn.go` | Split | subagent coordinator port，不直接依赖 tool concrete provider |
| `domains/agent/engine/background/manager.go`, `spawn_task.go`, `types.go`, `video_task.go` | `domains/agent/internal/engine/background/` 同名文件 | Move | Agent-owned background task lifecycle |
| `domains/agent/event/event.go` | `domains/agent/message.go`（public stream event value）或 `internal/engine/event.go` | Split | Channel/Pipeline/MCP 当前消费；公开字段只保留跨 boundary 所需事件，engine-only state 内收 |
| `domains/agent/internal/sessionmode/sessionmode.go` | `domains/agent/chat/session/mode/mode.go` | Move | 单词 leaf，Session mode normalization |
| `internal/agentpayload/payload.go` | `domains/agent/internal/engine/payload/payload.go` | Move | 只被 core wiring/engine 使用，不应成为跨 domain module public contract |

### 4.3 ACP family

| 当前生产文件族 | 目标 | 操作 | 备注 |
| --- | --- | --- | --- |
| `internal/acpagent/session_pool.go`, `adapter_upgrade.go` | `domains/agent/internal/acp/runtime/session.go`, `upgrade.go` | Split | `SessionPool`、runtime handle、dynamic adapter；Bot/Session/Tool approval/User input/Workspace 均改成消费方 ports |
| `internal/acpclient/client.go`, `connection.go`, `session.go`, `session_context.go` | `domains/agent/internal/acp/client/` 同名文件 | Move | ACP runner/connection/session lifecycle |
| `internal/acpclient/process.go`, `path.go`, `terminal.go` | `domains/agent/internal/acp/client/` 同名文件 | Split | Workspace bridge改用Agent consumer-owned Runtime port，参数/结果引用Runtime root values；路径规则保持client-owned |
| `internal/acpclient/events.go`, `transcript.go` | `domains/agent/internal/acp/client/event.go`, `transcript.go` | Move | ACP -> Agent event/transcript conversion |
| `internal/acpclient/model_selector.go`, `models.go`, `reasoning.go` | `domains/agent/internal/acp/client/model.go`, `reasoning.go` | Move | ACP session config state，不归全局 Model catalog |
| `internal/acpclient/claude_config.go`, `codex_config.go`, `hermes_config.go`, `managed_config.go` | `domains/agent/internal/acp/client/config/` 对应单词文件 | Move | managed agent config；不要合并成巨型 config 文件 |
| `internal/acpclient/mcp.go` | `domains/agent/internal/acp/client/mcp.go` | Move | ACP MCP server bridge |
| `internal/acpclient/output_limit.go` | `domains/agent/internal/acp/client/output.go` | Move | 依赖 chat/context/limit |
| `internal/acpprofile/profile.go`, `quirks.go` | `domains/agent/internal/acp/profile/` | Move | 当前 Bot/Channel 也消费；跨 domain module 所需只读 snapshot 应升到根 contract，不能让外部 import Agent internal |
| `internal/acpfeedback/feedback.go` | `domains/agent/continuation.go` 或 `internal/rpc/channel/pb` owned error detail | Split | Channel需要的用户反馈成为业务value或boundary error detail；ACP内部error classification留runtime |

### 4.4 Conversation aggregate 与 UI/message conversion

| 当前生产文件 | 目标 | 操作 | 变化 |
| --- | --- | --- | --- |
| `internal/conversation/service.go` | `domains/agent/chat/conversation/service.go` + `store.go` | Split | CRUD/settings/access 用例与 consumer-owned `Store` 分开，去 SQLC/broad Queries |
| `internal/conversation/interfaces.go` | `domains/agent/chat/conversation/port.go` | Split | 接口按 consumer 定义，删除无消费方的大接口 |
| `internal/conversation/types.go` | `domains/agent/chat/conversation/types.go` + `domains/agent/message.go` | Split | Conversation aggregate 留内部；Turn wire types 上移根 contract；model-only types进 convert |
| `internal/conversation/attachment_bundle.go` | `domains/agent/internal/turn/convert.go` | Move/Delete | bundle -> Turn attachment只应在Server-local converter存在 |
| `internal/conversation/skill_activation.go` | `domains/agent/message.go` | Move | Turn command owned skill activation value |
| `internal/conversation/uimessage.go`, `uimessage_adapter.go`, `uimessage_background.go`, `uimessage_convert.go`, `uimessage_stream.go`, `uimessage_user_input.go` | `domains/agent/chat/message/convert/` 对应文件 | Move | UI/SSE projection，不与 Conversation CRUD 同包 |
| `domains/agent/chat/convert/messageconv.go` | `domains/agent/chat/message/convert/model.go` | Move/Delete | 合并到单词 leaf `convert`，原复合 package 删除 |

### 4.5 `conversation/flow` 逐生产文件族

| 当前生产文件 | 目标 | 操作 | 拆分说明 |
| --- | --- | --- | --- |
| `resolver.go`, `types.go`, `idle_timeout.go` | `domains/agent/chat/flow/{resolver,types,timeout}.go` | Split | Resolver constructor 改为参数对象/小 ports；不得继续接收 broad Queries 和 20+ concrete services |
| `resolver_stream.go`, `resolver_turns.go`, `resolver_messages.go`, `resolver_store.go` | `chat/flow/{stream,turn,message,store}.go` | Split | orchestration 留 flow；持久操作只调 MessageWriter/SessionWriter ports |
| `resolver_user_persist.go`, `resolver_retry_edit.go` | `chat/flow/{user,retry}.go` | Split | retry replacement 必须调用同一 atomic RoundWriter，不直接拼 SQLC |
| `resolver_history.go`, `resolver_history_prepare.go`, `resolver_history_messages.go` | `chat/history/{load,prepare,message}.go` | Split | history projection 成独立内部 package；flow 只调用 `HistoryBuilder` |
| `resolver_history_compaction.go`, `resolver_history_compaction_merge.go`, `resolver_compaction.go`, `resolver_compaction_barrier.go` | `chat/compaction/{read,merge,trigger,barrier}.go` | Split | 读路径与写入 service 同 owner；保留 claim/fence/barrier 顺序 |
| `context_frag.go`, `acp_context.go` | `chat/context/fragment/{flow,acp}.go` | Move | context compilation adapters |
| `tool_closure.go`, `gateway_prune.go`, `resolver_util.go` | `chat/message/convert/{closure,prune,normalize}.go` | Move | 纯 model message transformation |
| `resolver_model_selection.go` | `chat/flow/model.go` | Split | 定义 `ModelSelector`/`SessionModelReader`；禁止 SQLC Provider/Model 作为 flow 类型 |
| `resolver_memory.go`, `memory_query.go` | `chat/flow/memory.go` | Split | 定义Agent消费的Memory query/extract port；Server composition直接注入Memory concrete |
| `resolver_attachments.go` | `chat/flow/media.go` | Split | 定义 Media asset reader；无 concrete media service |
| `resolver_workspace_target.go` | `chat/flow/runtime.go` | Split | 定义 Runtime target resolver/permission port |
| `resolver_settings.go` | `chat/flow/setting.go` | Split/Delete | 按 Bot/Model/Tool policy consumer ports 拆，原“统一 settings”依赖消失 |
| `resolver_identity.go`, `platform_identity.go`, `resolver_timezone.go` | `chat/flow/{identity,timezone}.go` | Split | Account/Channel identity/timezone 只通过 read ports |
| `requested_skills.go`, `capability_policy.go` | `chat/flow/{skill,capability}.go` | Move | Turn execution policy |
| `hooks.go` | `chat/flow/hook.go` | Split | `HookRunner` port；hook implementation 在 extension/hook |
| `resolver_acp.go`, `resolver_acp_active_prompt.go` | `chat/flow/{acp,active}.go` | Split | ACP runtime port；active prompt state不得泄漏 concrete SessionPool |
| `resolver_continuation.go`, `resolver_tool_approval.go`, `resolver_user_input.go` | `chat/flow/{continuation,approval,input}.go` | Split | continuation authorization/orchestration 留 flow；decision persistence分别归 approval/input package |
| `resolver_title.go` | `chat/flow/title.go` | Split | `SessionTitleWriter`、`TitleModelReader`；后台副作用保留 best-effort 语义 |
| `resolver_trigger.go` | `automation/{schedule,heartbeat}/runner.go` | Split | schedule/heartbeat 入口不再让 flow 实现两个 domain interface |
| `schedule_gateway.go`, `heartbeat_gateway.go` | `automation/{schedule,heartbeat}/gateway.go` | Move | gateway 依赖根 Turn/local runner port |
| `email_gateway.go` | `local/email.go` | Split | embedded adapter；Email delivery 是 Channel port，不能成为 chat core import |

### 4.6 Context、History 和 Compaction

| 当前生产文件族 | 目标 | 操作 | 备注 |
| --- | --- | --- | --- |
| `internal/contextfrag/compile.go`, `contract.go`, `hash.go`, `render.go`, `types.go` | `domains/agent/internal/chat/context/fragment/` | Move | 纯 context contract/compiler；leaf `fragment` |
| `internal/contextlimit/output.go` | `domains/agent/internal/chat/context/limit/output.go` | Move | 当前只依赖 prune；保持纯函数 |
| `internal/historyfrag/compaction.go`, `db_message.go`, `frag.go`, `types.go` | `domains/agent/chat/history/fragment/` | Split | `db_message.go` 去掉 SQLC/Message concrete row，改吃 Agent-owned history record |
| `internal/compaction/artifact.go`, `artifact_frontier_merge.go`, `artifact_projection.go`, `artifact_store_projection.go` | `domains/agent/chat/compaction/artifact/` | Split | pure projection 与 store adapter 分开；store interface由 projection consumer 定义 |
| `internal/compaction/candidate_rows.go`, `entry.go`, `prompt.go`, `selection.go`, `types.go` | `domains/agent/chat/compaction/` | Split | candidate 不再暴露 SQLC row；纯 selection/prompt 可机械移动 |
| `internal/compaction/service.go`, `service_execution.go` | `domains/agent/chat/compaction/service.go`, `run.go` | Split | Store、Model、Hook 都使用小 ports；保留 in-flight owner/cooldown/claim/finalize 语义 |
| `domains/agent/chat/text/prune/text.go` | `domains/agent/chat/text/prune/text.go` | Move | 单一 owner，禁止新建 common/utils |

### 4.7 Message、Session、runtime fence 与 decision

| 当前生产文件 | 目标 | 操作 | 变化 |
| --- | --- | --- | --- |
| `internal/message/types.go` | `chat/message/{types,port}.go` | Split | value、writer、read model接口按消费用例拆小但保持同一Message package；删除巨型`Service` interface |
| `internal/message/service.go` | `chat/message/{write,turn,replacement,asset,read,convert}.go`, `postgres/*` | Split | 函数级拆分见第5节；不创建`store/query`层包，SQLC conversion只能进postgres adapter |
| `internal/message/event/hub.go` | `chat/message/event/hub.go` | Move | in-process publisher/hub；跨进程事件以后另建 contract |
| `internal/session/service.go` | `chat/session/{service,route,subagent,descriptor,hook}.go`, `postgres/*` | Split | 函数级拆分见第5节；Session aggregate保持一个domain package |
| `domains/agent/chat/fence/fence.go` | `chat/session/fence/fence.go` | Move | context-carried value/error，保持无 SQLC |
| `domains/agent/chat/fence/postgres.go` | `domains/agent/internal/postgres/fence.go` | Split | transaction/lock implementation 归 postgres；chat packages只依赖 `FenceRunner` port |
| `internal/decision/types.go`, `waiter.go` | `domains/agent/internal/decision/{types,waiter}.go` | Move | in-process waiter/notification |
| `internal/decision/transaction.go` | `domains/agent/internal/postgres/decision.go` | Split | parent-before-session lock 与事务 adapter，不应在 domain package import SQLC |

`internal/sessionruntime` 当前不在 `go list -deps ./cmd/agent` 或 `./cmd/channel` 的生产
依赖图中。它不是“已接入但目录不好看”，而是 dormant subsystem：

| 当前生产文件族 | 目标 | 操作 | 决策 |
| --- | --- | --- | --- |
| `commands.go`, `control.go`, `manager.go`, `state.go`, `types.go` | `chat/session/runtime/` | Keep/Decide | 只有产品确认采用分布式run ownership/steer/abort后才迁入，并适配各consumer-owned handle port；否则删除 |
| `memory.go`, `redis.go` | `chat/session/runtime/backend/{memory,redis}.go` | Keep/Decide | Backend 实现；未接入前不得被 split 架构当作现状依赖 |
| `config.go` | `local/runtime.go` | Keep/Decide | 仅 composition config adapter；不得进入 domain |

### 4.8 Tool、approval 与 user input

| 当前生产文件族 | 目标 | 操作 | 备注 |
| --- | --- | --- | --- |
| `domains/agent/tools/types.go`, `names.go`, `native_source.go` | `domains/agent/tool/catalog/{types,names,source}.go` | Split | ToolProvider/AvailableTools/snapshot/registry；Native source依赖 approval/input consumer ports |
| `domains/agent/tools/internal/toolname/toolname.go` | `tool/catalog/name/name.go` | Move | 单词 leaf |
| `domains/agent/tools/internal/toolset/toolset.go` | `tool/catalog/set/set.go` | Move | 单词 leaf |
| `domains/agent/tools/output_limit.go`, `prune.go` | `tool/catalog/{output,prune}.go` | Move | pure result policy |
| `apply_patch.go`, `container.go`, `fsops.go`, `browser.go`, `computer_a11y.go` | `tool/adapter/runtime/` | Split | 只依赖Agent consumer-owned Runtime ports和Runtime root values；hook/setting concrete services移除 |
| `attachment_bundle.go`, `read_media.go` | `tool/adapter/media/` | Split | 只依赖Agent consumer-owned Media ports和Media root values |
| `transcribe.go`, `tts.go`, `image_gen.go`, `video_gen.go` | `tool/adapter/model/` | Split | Model/Media ports；provider/service concrete type移除 |
| `contacts.go`, `email.go`, `message.go` | `tool/adapter/channel/` | Split | Agent-owned tool adapter调 Channel contract；不得 import Channel internal |
| `memory.go` | `tool/adapter/memory/memory.go` | Split | Server-local Memory concrete实现consumer port |
| `schedule.go` | `tool/adapter/schedule/schedule.go` | Split | 依赖 automation schedule command port |
| `history.go` | `tool/adapter/history/history.go` | Split | 依赖 Message query port |
| `federation.go` | `tool/adapter/mcp/federation.go` | Move | 依赖 MCP ToolSource contract |
| `skill.go` | `tool/adapter/skill/skill.go` | Move | Skill activation tool |
| `background.go`, `subagent.go`, `subagent_policy.go` | `tool/adapter/agent/{background,subagent,policy}.go` | Split | Session/subagent/model选择使用窄 ports |
| `web.go`, `webfetch.go` | `tool/adapter/web/` | Split | fetch/search provider ports |
| `ask_user.go` | `tool/input/ask.go` | Move | ask_user schema/provider与 input domain同 owner |

Approval/Input/Decision 精确映射：

| 当前生产文件族 | 目标 | 操作 |
| --- | --- | --- |
| `internal/toolapproval/types.go`, `message.go`, `policy.go` | `domains/agent/tool/approval/{types,message,policy}.go` | Split：跨 Turn response 的 value 上移根 continuation contract |
| `internal/toolapproval/service.go`, `flow.go` | `tool/approval/{service,flow}.go` + `postgres/approval.go` | Split：policy evaluation、lifecycle、waiter 和 SQL 分开 |
| `internal/userinput/types.go`, `payload.go`, `interaction.go`, `plain_text.go` | `domains/agent/tool/input/` 对应文件 | Split：跨 RPC 的 answer/advance value 上移根 continuation contract |
| `internal/userinput/service.go`, `flow.go` | `tool/input/{service,flow}.go` + `postgres/input.go` | Split：durable request、interaction reducer、waiter 和 SQL 分开 |

### 4.9 MCP、extensions 和 automation

| 当前生产文件族 | 目标 | 操作 | 备注 |
| --- | --- | --- | --- |
| `domains/agent/mcp/connections.go` | `domains/agent/mcp/connection/service.go` + `postgres/mcp.go` | Split | connection aggregate/store；管理 HTTP 不迁入 Agent internal |
| `domains/agent/mcp/oauth.go` | `mcp/oauth/service.go` + `postgres/oauth.go` | Split | OAuth protocol/client 与 token store 分开 |
| `domains/agent/mcp/jsonrpc.go`, `service.go` | `mcp/protocol/{jsonrpc,payload}.go` | Move | pure JSON-RPC/value helpers |
| `domains/agent/mcp/http_tools.go` | `mcp/gateway/http.go` | Split | HTTP MCP adapter；若是 public API host，应由 API transport装配但实现仍消费 Agent gateway port |
| `domains/agent/mcp/result_limit.go`, `tool_gateway_service.go`, `tool_registry.go`, `tool_session_store.go`, `tool_types.go` | `mcp/gateway/{result,service,registry,session,types}.go` | Split | runtime fence 与 tool source ports明确；无 SQLC |
| `domains/agent/mcp/sources/federation/source.go` | `mcp/federation/source.go` | Move | 单词 leaf，不保留 `sources` 层 |
| `domains/agent/extension/hooks/service.go`, `types.go` | `domains/agent/internal/extension/hook/{service,types}.go` | Split | Workspace/Plugin/Skill通过 ports；Hook catalog/value可留同包 |
| `domains/agent/extension/skills/catalog.go`, `skills.go` | `extension/skill/{catalog,service}.go` | Split | 文件读取走 Runtime port；API 配置管理不 import concrete service |
| `domains/agent/extension/plugins/oauth_clients.go`, `service.go`, `types.go` | `extension/plugin/{oauth,service,types}.go` + `postgres/plugin.go` | Split | plugin install/resource transaction、MCP和Skill side effects通过 ports |
| `domains/agent/automation/schedule/service.go`, `trigger.go`, `types.go` | `domains/agent/internal/automation/schedule/{service,trigger,types}.go` + `postgres/schedule.go` | Split | scheduler/lifecycle与 store adapter分开 |
| `domains/agent/automation/heartbeat/service.go`, `trigger.go`, `types.go` | `automation/heartbeat/{service,trigger,types}.go` + `postgres/heartbeat.go` | Split | heartbeat config在 Bot owner，Agent只消费 execution snapshot |

## 5. 四个关键拆分的函数/type 归属

### 5.1 `message/service.go`

目标 `chat/message/write.go`：

- `Persist`, `persistOnce`, `preparePersistMessage`, `persistDirectWithoutTx`, `persist`,
  `persistDirectHistoryMessage`, `finishPersistedMessage`, `cleanupPersistedMessage`；
- `PersistToolTailRound`, `isToolTailRoundShape`, `shouldPersistMessageInTx`；
- `PersistRound` 作为 atomic round application method，不能拆成多次跨 RPC 写。

目标 `chat/message/turn.go`：

- `persistHistoryTurn`, `lockHistoryTurnAppendByRequest`,
  `appendMessageToHistoryTurnByRequest`；
- History Turn value 与 writer port；
- `CreateHistoryTurn`、assistant bind、append/link 的锁顺序保持原子。

目标 `chat/message/replacement.go`：

- `replacePersistedRound`, `replaceHistoryTurn`, `ReplaceTurn`,
  `firstPersistedRoleID`；
- replacement 必须与新 round 写入和 session metadata fence 在同一事务。

目标 `chat/message/asset.go`：

- `LinkAssets`, `enrichAssets`, `ensureAssetsSlice`；
- Asset mutation 必须遵守 Session -> Compaction artifact 的既有 lock order，不能由 Media
  service 独立提交后再回写 Message。

目标 `chat/message/read.go`：

- `List*`, `LocateByExternalIDBySession`, `GetByIDBySession`,
  `ListVisibleFromBySession`, `GetVisibleTurnByMessage`,
  `GetLatestVisibleTurnBySession`；
- `DeleteByBot/IDs/Session`是mutation，不属于read path，放`message/delete.go`。

目标 `domains/agent/internal/postgres/message.go`：

- `toMessageFrom*`, `toMessageFields*`, `toMessagesFrom*`, `toHistoryTurn*`；
- UUID/pgtype/SQLC conversion；
- `historyTurnWriter`、`transactionalQueries` 等现有临时接口由清晰的 `MessageStore`
  PostgreSQL implementation 取代。

### 5.2 `session/service.go`

目标 `chat/session/service.go`：

- `Create`, `Get`, `ListByBot*`, `UpdateTitle`, `UpdateMetadata`, `SoftDelete`, `Touch`,
  `MessageCount`；
- `Session`, `CreateInput`, cursor/filter 等 aggregate values。

目标 `chat/session/route.go`：

- `canonicalChannelType`, `ListByRoute`, `GetActiveForRoute`, `SetRouteActiveSession`,
  `CreateNewSession*`, `EnsureActiveSession`；
- Route ID 是外部 reference；该包只依赖 route identifier，不 import Channel route实现。

目标 `chat/session/subagent.go`：

- `CreateSubagent`, `GetSubagentConfig`, `ListSubagentForkContext`,
  `ForkFromAssistantMessage`, `ListSubagentsByParent`；
- `CreateSubagent` 的 Session + config + fork context 保持一个事务；
- `ForkFromAssistantMessage` 继续由一个 SQL statement/transaction验证 source assistant
  可见性并建立 fork，禁止“先读再远程写”造成 TOCTOU。

目标 `chat/session/descriptor.go`：

- `UpdateTypeAndMetadata*`, `UpdateDescriptorAndMetadataWithOwner`,
  `ResolveDescriptor`, `DescriptorFromLegacyType`, `LegacyTypeForDescriptor`,
  ACP metadata defaults/validation；
- Bot ACP policy读取改成 `ACPPolicyReader`，不得直接由 Session store查询 Bot 设置。

目标 `chat/session/hook.go`：

- `publishSessionCreated`, `runSessionStartHook`；
- 事务成功后才发布/运行；保持 best-effort，不允许 hook 失败回滚已提交 Session；
- 若未来要求可靠投递，使用同事务 outbox，而不是把外部 hook放进 DB transaction。

所有 `toSession*`、pgtype/SQLC conversion 下沉 `postgres/session.go`。

### 5.3 `conversation/flow`

`Resolver` 最终只负责一个 Turn 的 orchestration。推荐消费 ports（接口定义在 flow）：

```go
type MessageWriter interface { PersistRound(...); ReplaceTurn(...) }
type MessageReader interface { ListVisibleHistory(...); Locate(...) }
type SessionReader interface { Get(...); LatestModel(...) }
type SessionWriter interface { UpdateTitle(...); UpdateMetadata(...) }
type ModelSelector interface { Select(...); GenerateTitle(...) }
type MemoryGateway interface { Query(...); Extract(...) }
type MediaReader interface { ResolveAssets(...) }
type RuntimeGateway interface { ResolveTarget(...); InlineImages(...) }
type ChannelGateway interface { SendEmail(...); ResolveIdentity(...) }
type HookRunner interface { Run(...); LoadEffective(...) }
type ApprovalGateway interface { Respond(...); Wait(...) }
type InputGateway interface { Respond(...); AdvanceText(...) }
```

这些名字表达能力示例，最终 method set 必须从实际 consumer call site提炼为 1-3 个方法，
不要照抄成统一“大 Service interface”。`Resolver` 不再持有 `dbstore.Queries`、SQLC
Provider、concrete Settings/Accounts/Bots/Media/Workspace/SessionPool。

### 5.4 `agent/tools`

`tool/catalog` 只拥有：tool descriptor/provider contract、名字冲突检测、tool snapshot、
输出限制、注册/查找。每个具体工具 adapter 仍可放 Agent domain module，因为它把能力呈现给
模型，但必须依赖 capability owner 的根 port。例如 Message tool 依赖 Channel sender，
Memory tool依赖Memory consumer port，Container tool依赖Runtime consumer port。两者都由同一
Server进程内的owner concrete实现；只有Message/Email等跨Channel边界的port具有RPC实现。

## 6. Persistence owner、ports 与事务边界

### 6.1 数据 owner

| 数据/表 | 唯一写入 owner | 外部调用方式 |
| --- | --- | --- |
| `chats`, chat settings/participants/read access | Agent Chat conversation | Channel/API 调 Conversation command/query port |
| `bot_sessions`, `subagent_configs` | Agent Session | Channel/API 调 Session port |
| `bot_session_discuss_cursors` | Channel Discuss | Agent不写cursor；Discuss driver经Channel-owned store维护 |
| `bot_history_messages` 的 active Turn rows、History Turn read model | Agent Message | Agent writer处理 Turn/history/replacement；不接管 Channel passive row intent |
| `bot_history_messages` 的 passive observed user rows | Channel | Channel narrow writer必须 `SkipHistoryTurn`，不得写 Turn/compaction/replacement；Agent不得重复实现该 row intent |
| `bot_history_message_assets` | Agent Message transaction；Media owns asset blob/catalog | Link 由 Message store在其事务完成 |
| `bot_history_message_compacts` | Agent Compaction | 仅 Compaction service修改 claim/completion/supersession |
| `tool_approval_requests` | Agent Tool Approval | Channel/API 只提交 decision command |
| `user_input_requests` | Agent User Input | Channel/API 只提交 answer/advance command |
| `mcp_connections`, `mcp_oauth_tokens` | Agent MCP integration | API 是 transport，不直接 SQL |
| `bot_plugin_installations`, `bot_plugin_resources` | Agent Plugin runtime | Bot/API management通过 Plugin port |
| `schedule`, `schedule_logs`, `bot_heartbeat_logs` | Agent Automation | Bot config字段仍由 Bot/API owner；执行日志归 Agent |

这里按已批准 Channel spec采用**同表、row-intent/statement 分权**：Channel拥有 passive
observed insert，Agent拥有 active Turn/history mutation。两边可以有各自 narrow PostgreSQL
adapter，但 SQL statement必须互斥：Channel writer强制 `SkipHistoryTurn`，不得绑定/替换
History Turn、claim/finalize compaction或修改 active assistant/tool row；Agent writer不得实现
Channel observation去重。跨 row-intent 的清理/备份由 owner command协调，不能共享 broad
`Queries` 绕过边界。

### 6.2 不得破坏的事务/锁顺序

1. Session-scoped write：先锁 Bot parent，再锁 Session/fence。
2. Approval/Input 创建：`decision.InCreateTransaction` 保持 Bot -> Session decision sequence
   锁，再插入 request。
3. Runtime fenced message/decision write：fence 校验和数据 mutation在同一 transaction。
4. `PersistRound`：一轮消息、History Turn link、replacement、session metadata在同一事务。
5. `PersistToolTailRound`：user -> assistant(tool call) -> tool -> assistant(final) 保持单语句/
   单事务 fast path。
6. Compaction claim/finalize、message replacement、asset mutation遵守现有 Session 与 artifact
   lock order；对应 PostgreSQL integration tests必须原样迁移后再重构 fixture。
7. Subagent create：child session + pinned config + fork context原子提交。
8. 事件 publish、Session start hook、title generation等外部副作用发生在 commit 后；需要可靠性
   时引入 outbox，不延长数据库事务。

### 6.3 Consumer-owned store ports

禁止把 `dbstore.Queries` 换一个名字搬到 Agent。至少拆为：

- ConversationStore；
- SessionStore、SubagentStore；
- MessageWriterStore、MessageQueryStore、HistoryTurnStore、AssetLinkStore；
- CompactionStore/ArtifactStore；
- FenceStore/DecisionStore；
- ApprovalStore、InputStore；
- MCPConnectionStore、MCPOAuthStore、PluginStore；
- ScheduleStore、HeartbeatLogStore。

Transaction port 应传递同一 owner store bundle 或提供 owner-specific closure，不能把 broad
`Queries` 再泄漏进 callback。

### 6.4 与其他模块账本的唯一交界

| 相邻模块 | 它拥有 | Agent 拥有 | 唯一交界，禁止重复实现 |
| --- | --- | --- | --- |
| Channel/Pipeline | 外部平台 observation、route、identity、delivery、ingress去重、passive observed message insert | Turn、Session、active Turn/History Message、continuation | Channel passive writer强制 `SkipHistoryTurn`且不得写 Turn/compaction；Discuss/active execution只 import根 `domains/agent`；两边 SQL statement/row intent不重叠 |
| Media | blob、content hash、asset catalog/storage reference | `bot_history_message_assets` link和与 History/Compaction一致的事务 | Agent接受 Media `AssetRef` value并在 Message transaction link；Media不能直接更新 message link，Agent不能复制 blob store |
| Memory | memory item/index/provider状态 | chat history/context、Memory tool adapter | Agent定义query/extract consumer port，由Server-local Memory concrete实现；Agent不复制Memory registry/store |
| Model | model/provider/catalog/capability | Turn model selection policy和ACP session config state | Agent消费 Model snapshot/client port；ACP config不是第二套 Model catalog |
| Runtime | workspace/container/network/bridge实现 | run/turn ownership、runtime fence、tool orchestration | Agent定义最小Runtime consumer port并引用root values；`sessionruntime`若保留也只管理Agent run state，不复制Workspace runtime |
| API/Botbackup | HTTP/IAM、Bot export/import use case | Agent data export snapshot与import command | Botbackup不能 import Agent postgres/SQLC；Agent提供按 aggregate版本化 export reader/import writer，事务仍由 Agent owner提交 |

特别是 Botbackup：当前 `provideBotBackupService` 直接注入 Session/MCP/Schedule/ACP 等
concrete services。目标不是把 Botbackup搬入 Agent，而是由 API-owned backup coordinator
消费 `AgentExportReader`/`AgentImportWriter`。Export可跨 aggregate读取一致 snapshot；Import
按 Agent transaction边界调用，Botbackup不得自己拼写 Session/Message/Plugin SQL。

## 7. DB query、generated、contract 与 migration 映射

| 当前位置 | 最终目标 | 操作 |
| --- | --- | --- |
| `db/postgres/queries/conversations.sql` | `db/postgres/agent/queries/conversation.sql` | Move；ConversationStore owner |
| `db/postgres/queries/messages.sql` | Agent `db/postgres/agent/queries/{message,turn,replacement}.sql` + Channel `db/postgres/channel/queries/message.sql` | Split；passive `CreateMessage`提炼为Channel-only `CreatePassiveObservedMessage`；其余active/history/replacement归Agent |
| `db/postgres/queries/media.sql` 的 `*MessageAsset*` statements | `db/postgres/agent/queries/asset.sql` | Split out；全部message-asset link归Agent transaction，blob/catalog statements迁`db/postgres/media/queries` |
| `db/postgres/queries/sessions.sql` | `db/postgres/agent/queries/session.sql` + `db/postgres/channel/queries/cursor.sql` | Split；Session CRUD/fence归Agent，discuss cursor statements归Channel |
| `db/postgres/queries/session_info.sql`, `subagents.sql` | `db/postgres/agent/queries/{session_info,subagent}.sql` | Move/Split |
| `db/postgres/queries/session_events.sql` | `db/postgres/channel/queries/event.sql` | Move out；完全归Channel inbound observation，Agent若读只能经Channel reader/projection port |
| `db/postgres/queries/compaction_logs.sql` | `db/postgres/agent/queries/compaction.sql` | Move |
| `db/postgres/queries/tool_approval.sql`, `user_input.sql` | `db/postgres/agent/queries/{approval,input}.sql` | Move |
| `db/postgres/queries/mcp.sql`, `mcp_oauth.sql`, `plugins.sql` | `db/postgres/agent/queries/{mcp,oauth,plugin}.sql` | Move |
| `db/postgres/queries/schedule.sql`, `schedule_logs.sql`, `heartbeat_logs.sql` | `db/postgres/agent/queries/{schedule,schedule_log,heartbeat_log}.sql` | Move |
| 对应 `internal/db/postgres/sqlc/*.sql.go` | `domains/agent/internal/postgres/sqlc/*.sql.go` | Generate；先改 sqlc config再重生成，不手工搬 generated symbol |
| `internal/db/postgres/store/message_turns.go` | `domains/agent/internal/postgres/turn.go` | Move/Split |
| `internal/db/store/contracts.go`, `queries.go` 中上述 method/type | 各 consumer package `port.go` + `internal/postgres` adapter | Split/Delete；迁一组删一组 broad method |
| `internal/db/db.go`、pool/migration runner | `internal/database` | 不由 Agent domain module拥有 |

Agent手写SQL与`sqlc.yaml`留在`db/postgres/agent`，生成代码输出到
`domains/agent/internal/postgres/sqlc`。Store/Transactor contract位于Message、Session、
Compaction等消费use case，`domains/agent/internal/postgres`只提供具体实现，不形成第二个全局Store。

Epoch v1历史migration在cutover时从`db/postgres/migrations`整体归档到
`db/postgres/legacy/v1/migrations`，不改basename/编号/内容/checksum。Epoch v2 Agent schema从
`db/postgres/agent/migrations/00001_baseline.sql`独立编号，并使用
`agent.goose_db_version`；统一Migrator负责执行。
本范围直接相关的历史包括 `0003`, `0007`, `0009`, `0012`, `0016`, `0017`, `0022`,
`0023`, `0036`, `0038`, `0040`, `0043`, `0052`, `0054`, `0055`, `0058`, `0059`,
`0060`, `0073`, `0074`, `0082`, `0085`, `0086`, `0089`, `0090`, `0091`, `0096`,
`0098`, `0102`, `0103`, `0105`, `0108`, `0109`, `0111`, `0113`, `0116`, `0119`。
Epoch v2未来schema change只能在Agent owner stream追加migration，不能修改已发布文件，也不能
由Agent业务进程启动时自行执行。

DB 测试映射：

- `internal/db/acp_migration_test.go` -> `domains/agent/internal/postgres/acp_migration_test.go`；
- `internal/db/compaction_artifact_migration_{,integration_}test.go`,
  `uncompacted_messages_query_integration_test.go` -> Agent postgres/compaction tests；
- `internal/db/runtime_fence_migration_integration_test.go` -> Agent postgres/fence test；
- `internal/db/subagent_fork_history_migration_integration_test.go` -> Agent postgres/subagent test；
- `internal/db/delete_chat_compaction_postgres_integration_test.go` -> Agent postgres cross-aggregate
  lock-order test；
- team/RLS composite-key tests留 `internal/database`，因为它们验证全库 tenant infrastructure。

## 8. Composition 与生产入口映射

| 当前 wiring | 目标 | 操作 |
| --- | --- | --- |
| `cmd/internal/core/providers.go`: Agent/Chat/ACP/Tool/Extension/Automation providers | `domains/agent`公开constructors + 对应`internal/*`实现 | Split；构造参数改为consumer ports，不建立统一`local`module；composition root 只在`cmd/agent`/`cmd/channel`，禁止`internal/composition`或长期`cmd/internal/process` |
| `cmd/agent/rpc.go`: Turn server registration | `internal/rpc/channel/server` composition | Rewrite；只承载final Channel -> Server boundary，业务handler注入Agent application port |
| `cmd/channel/rpc.go`: `provideTurnClient` | `internal/rpc/channel/client` composition | Rewrite；Channel只提交normalized inbound/control |
| `cmd/agent/http_providers.go`: Message/Session/ACP HTTP providers | `domains/api/http/agent` wiring | 不迁入 Agent；API adapter只依赖 Agent ports |
| `cmd/internal/core/module.go` Agent-related `fx.Provide/Invoke` | Server common composition + Agent公开constructors | Split/Delete；`core` 不再枚举Agent internals |

Agent是两种`cmd/agent`构建的共同宿主能力：embedded和split Server运行同一个Agent
implementation。Build profile只替换Channel consumer port的实现。Split额外装配
`internal/rpc/channel/server`接收Channel child的typed inbound/Turn请求；这不是Agent独立部署，
也不授权建立`domains/agent/grpc`。

## 9. 测试迁移规则

所有测试与被测生产职责同目录迁移，文件 basename保持；不能把大量 fixture变成 production
exported helper。具体规则：

- `internal/rpc/channel/turn/**/**/*_test.go` -> 对应 `domains/agent/{local,grpc/*}`；
- Agent engine/background、ACP client/runtime/profile tests -> 对应 `internal/engine`、
  `internal/acp/*`；
- `conversation/flow` 41 个 tests跟随第 4.5 节文件 owner；跨 history/compaction/flow 的
  contract test放较高层 `domains/agent/chat/flow`；
- `message` 11 个 tests中，service unit tests拆到 store/query；所有 PostgreSQL lock-order/
  concurrency tests统一到 `internal/postgres`，不得因目录迁移降级为 mock test；
- Session 4 个tests分别跟随session/subagent/fence职责；
- Compaction 18 个 tests跟随 artifact/prompt/selection/service；
- Tool 22 个 tests跟随 catalog/adapter owner；
- Tool approval 6、User input 6、MCP 7、Session runtime 4、其他 package tests按同名职责迁移。

逐 package 测试落点（精确文件名见 coverage manifest；数量不含生产文件）：

| 当前测试路径 | 数量 | 目标测试路径/拆分 |
| --- | ---: | --- |
| `internal/agent/*_test.go` | 15 | `domains/agent/internal/engine/*_test.go`；attachment/media 测试跟随 `engine/media`，background e2e留 engine integration |
| `domains/agent/engine/background/*_test.go` | 3 | `domains/agent/internal/engine/background/*_test.go` |
| `domains/agent/internal/sessionmode/*_test.go` | 1 | `domains/agent/chat/session/mode/*_test.go` |
| `domains/agent/tools/*_test.go` | 22 | 按第 4.8 节拆到 `tool/catalog` 与 `tool/adapter/{runtime,media,model,channel,memory,history,agent,web}` |
| `internal/rpc/channel/turn/*_test.go` | 3 | `domains/agent/*_test.go`，验证根 value/helper contract |
| `internal/rpc/channel/turn/inprocess/*_test.go` | 3 | `domains/agent/internal/turn/*_test.go` |
| `internal/rpc/channel/turn/grpctransport/*_test.go` | 1 | `internal/rpc/channel/server/turn_test.go`；同一test构造boundary client |
| `internal/agentpayload/*_test.go` | 1 | `domains/agent/internal/engine/payload/*_test.go` |
| `internal/acpagent/*_test.go` | 2 | `domains/agent/internal/acp/runtime/*_test.go`；PostgreSQL fence integration转 `internal/postgres` 但保持 runtime fixture |
| `internal/acpclient/*_test.go` | 15 | `domains/agent/internal/acp/client/*_test.go`；live container test保持 explicit env gate |
| `internal/acpprofile/*_test.go` | 1 | `domains/agent/internal/acp/profile/*_test.go` |
| `internal/conversation/*_test.go` | 3 | UI conversion tests到 `chat/message/convert`；runtime contract test到 `chat/conversation` |
| `internal/conversation/flow/*_test.go` | 41 | 按第 4.5 节拆到 `chat/flow`, `chat/history`, `chat/compaction`, `chat/context/fragment`, `automation/*` |
| `internal/message/*_test.go` | 11 | unit tests到 `chat/message/{store,query}`；全部 PostgreSQL integration/lock-order tests到 `internal/postgres` |
| `internal/message/event/*_test.go` | 1 | `chat/message/event/*_test.go` |
| `domains/agent/chat/convert/*_test.go` | 1 | `chat/message/convert/model_test.go` |
| `internal/session/*_test.go` | 4 | `chat/session`；runtime fence integration到`internal/postgres/fence_test.go`，subagent test到`chat/session/subagent_test.go` |
| `internal/sessionruntime/*_test.go` | 4 | Keep；拍板保留后到 `chat/session/runtime` 和 `runtime/backend` |
| `domains/agent/chat/fence/*_test.go` | 3 | pure fence test到 `chat/session/fence`；postgres unit/integration到 `internal/postgres` |
| `internal/compaction/*_test.go` | 18 | 按 production owner拆到 `chat/compaction` 与 `chat/compaction/artifact` |
| `internal/historyfrag/*_test.go` | 2 | `chat/history/fragment/*_test.go` |
| `internal/contextfrag/*_test.go` | 4 | `chat/context/fragment/*_test.go` |
| `internal/contextlimit/*_test.go` | 1 | `chat/context/limit/*_test.go` |
| `internal/decision/*_test.go` | 2 | waiter到 `internal/decision`；transaction/lock test到 `internal/postgres` |
| `internal/toolapproval/*_test.go` | 6 | policy/message/flow到 `tool/approval`；fence/lifecycle persistence到 `internal/postgres` |
| `internal/userinput/*_test.go` | 6 | interaction/payload/flow到 `tool/input`；store/fence/lifecycle到 `internal/postgres` |
| `domains/agent/mcp/*_test.go` | 7 | 分到 `mcp/{connection,gateway,oauth}`；HTTP middleware test留 gateway adapter |
| `domains/agent/mcp/sources/federation/*_test.go` | 1 | `mcp/federation/source_test.go` |
| `domains/agent/extension/hooks/*_test.go` | 1 | `extension/hook/service_test.go` |
| `domains/agent/extension/skills/*_test.go` | 2 | `extension/skill/{catalog,service}_test.go` |
| `domains/agent/extension/plugins/*_test.go` | 1 | `extension/plugin/service_test.go` |
| `domains/agent/automation/schedule/*_test.go` | 2 | unit到 `automation/schedule`；integration到 `internal/postgres/schedule_test.go` |
| `domains/agent/automation/heartbeat/*_test.go` | 1 | `automation/heartbeat/service_test.go` |

没有当前测试文件的 package：`agent/event`、`agent/tools/internal/toolname`、
`agent/tools/internal/toolset`、`agent/turn/turnpb`、`acpfeedback`、`prune`。Generated pb
由 transport/contract tests覆盖，不能在 generated package里补手工测试来替代 parity。

必须保留并扩展 parity：

1. Agent direct application path与`internal/rpc/channel` client/server使用同一fixture跑最终typed StartTurn、Inject、
   approval/input continuation；outbound-assets只有在AD1批准并冻结最终schema后才加入parity，
   AD1前保持Blocked；
2. stream cancellation、tail event drain、error identity、idempotency retry行为一致；
3. proto generated test不手写，测试公开 contract和 transport；
4. dormant `sessionruntime` 未拍板前仍运行现有 tests，但不能把它加入 production wiring来
   “证明迁移成功”。

## 10. 分阶段迁移过程

1. **Gate A - 冻结根 contract**：先消除 Turn 对 Conversation/UserInput/Attachment alias，
   冻结最终typed RPC语义；当前wire只用于行为审计，不创建兼容transport、不移动实现。
2. **Gate B - Persistence ports**：Message、Session、HistoryTurn、Replacement、AssetLink、
   Compaction、UserInput、ToolApproval、RuntimeFence作为一个transaction cluster切
   consumer-owned stores；Decision由该cluster内对应原子用例协调。保留现有SQL/lock tests，
   删除global Queries callback与pool-less假事务fallback。
3. **Gate C - Flow/Tool 去 concrete dependency**：逐个替换44个fan-out imports；先Model/Memory/
   Runtime/Channel，再 DB/SQLC。
4. **Gate D - Process transport boundary**：批准唯一最终typed contract后建立
   `internal/rpc/channel` client/server/pb，以同一fixture补direct/RPC parity，替换旧
   proto/generated并更新arch test；不建立Agent local/grpc package。
5. **Gate E - 原namespace内拆巨型文件**：按chat/engine/tool/extension职责拆文件和package，
   保持业务行为；RPC旧路径不作为兼容入口。
6. **Gate F - Mechanical Move**：依赖方向闭合后迁到`domains/agent/internal`；Tool adapters按
   capability owner迁移，Extension/Automation/ACP在ports稳定后同阶段机械迁移；split
   consumer只编入RPC client/root contract。
7. **Gate G - Database Epoch v2**：统一Goose Migrator迁入`agent` schema并初始化独立
   version table；fresh baseline、v1 bridge、resume和embedded/split compatibility通过后cutover。
8. **Gate H - Generated cleanup**：按statement owner建Agent SQLC target并重生成，删除旧
   generated import。
9. **Gate I - 删除旧 namespace**：无转发package；`go list -deps`与arch guards证明旧路径和
   forbidden imports为零。

每个 Gate将 namespace-only、contract change、behavior change、SQL change分开提交。

## 11. 验收命令

```bash
go list ./...
go test ./domains/agent/... ./internal/arch
go test ./domains/agent/internal/postgres/...   # 需要测试数据库的 suite按现有 env gate
go build ./cmd/agent
go build -tags split ./cmd/agent
go build ./cmd/channel

# Channel child不得编入 Agent implementation
go list -deps ./cmd/channel \
  | rg 'domains/agent/internal' && exit 1 || true

# 根 contract不得泄漏内部实现/transport/database
go list -json ./domains/agent \
  | jq -e '.Imports | any(test("conversation|sqlc|db/store|channel|fx|echo|grpc"))' \
  && exit 1 || true
```

## 12. Scope coverage

覆盖审计以本文件末尾的精确 manifest为准。基线统计：

| 项目 | 数量 |
| --- | ---: |
| package 目录 | 39 |
| 生产 Go 文件 | 219 |
| 测试 Go 文件 | 189 |
| Go 文件总数 | 408 |
| manifest 已覆盖 | 408 |
| 未覆盖 | 0 |
| manifest 中不存在于基线的文件 | 0 |

校验逻辑：从 `coverage-manifest` fenced block抽取每一个路径，与本蓝图 scope 的 `find`
结果排序后执行 `comm`；同时按 `_test.go` 分别计数。该 manifest只表示“文件已被审计和
映射”，不表示所有 Move/Split已获批准。

可复现校验脚本（在仓库根目录使用 zsh）：

```zsh
scope=(
  internal/agent internal/agentpayload internal/acpagent internal/acpclient
  internal/acpfeedback internal/acpprofile internal/conversation internal/message
  domains/agent/chat/convert internal/session internal/sessionruntime domains/agent/chat/fence
  internal/compaction internal/historyfrag internal/contextfrag internal/contextlimit
  internal/decision internal/toolapproval internal/userinput domains/agent/mcp domains/agent/extension/hooks
  domains/agent/extension/skills domains/agent/extension/plugins domains/agent/automation/schedule domains/agent/automation/heartbeat domains/agent/chat/text/prune
)
doc=docs/architecture/migration/agent-chat-turn.md
actual() { find ${scope[@]} -type f -name '*.go' -print | sort }
manifest() {
  awk '/^<!-- coverage-manifest:/{on=1; next}
       on && /^```text$/{next}
       on && /^```$/{exit}
       on{print}' "$doc" | sort
}

printf 'actual=%s manifest=%s production=%s tests=%s missing=%s extra=%s\n' \
  "$(actual | wc -l | tr -d ' ')" \
  "$(manifest | wc -l | tr -d ' ')" \
  "$(actual | rg -v '_test\.go$' | wc -l | tr -d ' ')" \
  "$(actual | rg '_test\.go$' | wc -l | tr -d ' ')" \
  "$(comm -23 <(actual) <(manifest) | wc -l | tr -d ' ')" \
  "$(comm -13 <(actual) <(manifest) | wc -l | tr -d ' ')"
```

本基线实际输出：

```text
actual=408 manifest=408 production=219 tests=189 missing=0 extra=0
```

<!-- coverage-manifest: generated from 257f894f; do not edit paths manually -->
```text
internal/acpagent/adapter_upgrade.go
internal/acpagent/runtime_fence_postgres_integration_test.go
internal/acpagent/session_pool.go
internal/acpagent/sessionpool_test.go
internal/acpclient/claude_config.go
internal/acpclient/client.go
internal/acpclient/client_test.go
internal/acpclient/codex_config.go
internal/acpclient/codex_container_live_test.go
internal/acpclient/connection.go
internal/acpclient/events.go
internal/acpclient/events_reclassify_test.go
internal/acpclient/events_test.go
internal/acpclient/hermes_config.go
internal/acpclient/hermes_config_test.go
internal/acpclient/managed_config.go
internal/acpclient/mcp.go
internal/acpclient/model_selector.go
internal/acpclient/model_selector_test.go
internal/acpclient/models.go
internal/acpclient/output_limit.go
internal/acpclient/parity_test.go
internal/acpclient/path.go
internal/acpclient/path_test.go
internal/acpclient/process.go
internal/acpclient/process_test.go
internal/acpclient/reasoning.go
internal/acpclient/reasoning_test.go
internal/acpclient/render_parity_test.go
internal/acpclient/session.go
internal/acpclient/session_context.go
internal/acpclient/session_context_test.go
internal/acpclient/session_usage_test.go
internal/acpclient/terminal.go
internal/acpclient/terminal_test.go
internal/acpclient/transcript.go
internal/acpclient/transcript_test.go
internal/acpfeedback/feedback.go
internal/acpprofile/profile.go
internal/acpprofile/profile_test.go
internal/acpprofile/quirks.go
internal/agent/agent.go
internal/agent/agent_reasoning_test.go
internal/agent/attachment_bundle.go
internal/agent/attachment_bundle_test.go
domains/agent/engine/background/manager.go
domains/agent/engine/background/manager_test.go
domains/agent/engine/background/spawn_task.go
domains/agent/engine/background/spawn_task_test.go
domains/agent/engine/background/types.go
domains/agent/engine/background/video_task.go
domains/agent/engine/background/video_task_test.go
domains/agent/engine/background_exec_e2e_test.go
internal/agent/config.go
domains/agent/chat/context_frag.go
domains/agent/chat/context_frag_test.go
domains/agent/event/event.go
internal/agent/fork_context_test.go
internal/agent/fs.go
internal/agent/generate_loop_test.go
internal/agent/guard_state.go
internal/agent/guard_state_test.go
internal/agent/hooks.go
internal/agent/prompt.go
internal/agent/prompt_test.go
internal/agent/read_media.go
internal/agent/read_media_test.go
internal/agent/resolve_result.go
internal/agent/retry.go
internal/agent/sential.go
internal/agent/sential_test.go
domains/agent/internal/sessionmode/sessionmode.go
domains/agent/internal/sessionmode/sessionmode_test.go
internal/agent/spawn_adapter.go
internal/agent/stream.go
internal/agent/stream_test.go
domains/agent/tool_approval_test.go
domains/agent/tool_execution_metadata.go
domains/agent/tool_execution_metadata_test.go
domains/agent/tool_output_limit_test.go
domains/agent/tools/apply_patch.go
domains/agent/tools/apply_patch_test.go
domains/agent/tools/ask_user.go
domains/agent/tools/attachment_bundle.go
domains/agent/tools/background.go
domains/agent/tools/background_test.go
domains/agent/tools/browser.go
domains/agent/tools/browser_test.go
domains/agent/tools/computer_a11y.go
domains/agent/tools/contacts.go
domains/agent/tools/container.go
domains/agent/tools/container_test.go
domains/agent/tools/email.go
domains/agent/tools/federation.go
domains/agent/tools/federation_test.go
domains/agent/tools/fsops.go
domains/agent/tools/history.go
domains/agent/tools/history_test.go
domains/agent/tools/image_gen.go
domains/agent/tools/image_gen_test.go
domains/agent/tools/internal/toolname/toolname.go
domains/agent/tools/internal/toolset/toolset.go
domains/agent/tools/memory.go
domains/agent/tools/message.go
domains/agent/tools/message_snapshot_test.go
domains/agent/tools/message_test.go
domains/agent/tools/names.go
domains/agent/tools/names_test.go
domains/agent/tools/native_source.go
domains/agent/tools/native_source_test.go
domains/agent/tools/output_limit.go
domains/agent/tools/output_limit_test.go
domains/agent/tools/prune.go
domains/agent/tools/read_media.go
domains/agent/tools/read_media_test.go
domains/agent/tools/schedule.go
domains/agent/tools/skill.go
domains/agent/tools/skill_test.go
domains/agent/tools/subagent.go
domains/agent/tools/subagent_bg_test.go
domains/agent/tools/subagent_model_test.go
domains/agent/tools/subagent_policy.go
domains/agent/tools/subagent_policy_test.go
domains/agent/tools/transcribe.go
domains/agent/tools/tts.go
domains/agent/tools/tts_test.go
domains/agent/tools/types.go
domains/agent/tools/usage_test.go
domains/agent/tools/video_gen.go
domains/agent/tools/video_gen_test.go
domains/agent/tools/watchdog_test.go
domains/agent/tools/web.go
domains/agent/tools/webfetch.go
domains/agent/tools/webfetch_test.go
internal/rpc/channel/turn/assistant_output.go
internal/rpc/channel/turn/assistant_output_test.go
internal/rpc/channel/turn/grpctransport/client.go
internal/rpc/channel/turn/grpctransport/feedback.go
internal/rpc/channel/turn/grpctransport/server.go
internal/rpc/channel/turn/grpctransport/transport_test.go
internal/rpc/channel/turn/inprocess/adapter.go
internal/rpc/channel/turn/inprocess/adapter_test.go
internal/rpc/channel/turn/inprocess/convert.go
internal/rpc/channel/turn/inprocess/discuss.go
internal/rpc/channel/turn/inprocess/discuss_test.go
internal/rpc/channel/turn/inprocess/idempotency.go
internal/rpc/channel/turn/inprocess/parity_test.go
internal/rpc/channel/turn/turn.go
internal/rpc/channel/turn/turn_test.go
internal/rpc/channel/turn/turnpb/turn.pb.go
internal/rpc/channel/turn/turnpb/turn_grpc.pb.go
internal/rpc/channel/turn/user_header.go
internal/rpc/channel/turn/user_header_test.go
internal/agent/types.go
internal/agent/usage_injection_test.go
internal/agentpayload/payload.go
internal/agentpayload/payload_test.go
internal/compaction/artifact.go
internal/compaction/artifact_coverage_validation_test.go
internal/compaction/artifact_frontier_coverage_test.go
internal/compaction/artifact_frontier_merge.go
internal/compaction/artifact_frontier_merge_test.go
internal/compaction/artifact_projection.go
internal/compaction/artifact_projection_test.go
internal/compaction/artifact_store_projection.go
internal/compaction/artifact_store_projection_review_test.go
internal/compaction/assembly_test.go
internal/compaction/boundary_test.go
internal/compaction/candidate_rows.go
internal/compaction/entry.go
internal/compaction/entry_test.go
internal/compaction/prompt.go
internal/compaction/prompt_test.go
internal/compaction/selection.go
internal/compaction/selection_test.go
internal/compaction/service.go
internal/compaction/service_artifact_test.go
internal/compaction/service_barrier_test.go
internal/compaction/service_coordination_test.go
internal/compaction/service_epoch_test.go
internal/compaction/service_execution.go
internal/compaction/service_frontier_test.go
internal/compaction/service_machinery_test.go
internal/compaction/service_starvation_test.go
internal/compaction/service_state_test.go
internal/compaction/types.go
internal/contextfrag/compile.go
internal/contextfrag/compile_contract_test.go
internal/contextfrag/compile_test.go
internal/contextfrag/contract.go
internal/contextfrag/contract_test.go
internal/contextfrag/coverage_test.go
internal/contextfrag/hash.go
internal/contextfrag/render.go
internal/contextfrag/types.go
internal/contextlimit/output.go
internal/contextlimit/output_test.go
internal/conversation/attachment_bundle.go
internal/conversation/flow/acp_context.go
internal/conversation/flow/acp_context_test.go
internal/conversation/flow/capability_policy.go
internal/conversation/flow/capability_policy_test.go
internal/conversation/flow/compaction_read_path_pairing_test.go
internal/conversation/flow/context_frag.go
internal/conversation/flow/context_frag_test.go
internal/conversation/flow/email_gateway.go
internal/conversation/flow/gateway_prune.go
internal/conversation/flow/heartbeat_gateway.go
internal/conversation/flow/hooks.go
internal/conversation/flow/idle_timeout.go
internal/conversation/flow/memory_query.go
internal/conversation/flow/memory_query_test.go
internal/conversation/flow/platform_identity.go
internal/conversation/flow/platform_identity_test.go
internal/conversation/flow/requested_skills.go
internal/conversation/flow/requested_skills_test.go
internal/conversation/flow/resolver.go
internal/conversation/flow/resolver_acp.go
internal/conversation/flow/resolver_acp_active_prompt.go
internal/conversation/flow/resolver_acp_active_prompt_test.go
internal/conversation/flow/resolver_acp_test.go
internal/conversation/flow/resolver_attachments.go
internal/conversation/flow/resolver_compaction.go
internal/conversation/flow/resolver_compaction_barrier.go
internal/conversation/flow/resolver_compaction_barrier_test.go
internal/conversation/flow/resolver_compaction_config_test.go
internal/conversation/flow/resolver_compaction_postprocess_test.go
internal/conversation/flow/resolver_continuation.go
internal/conversation/flow/resolver_continuation_test.go
internal/conversation/flow/resolver_dedupe_test.go
internal/conversation/flow/resolver_fork_context_test.go
internal/conversation/flow/resolver_history.go
internal/conversation/flow/resolver_history_compaction.go
internal/conversation/flow/resolver_history_compaction_merge.go
internal/conversation/flow/resolver_history_compaction_projection_test.go
internal/conversation/flow/resolver_history_compaction_sessionless_test.go
internal/conversation/flow/resolver_history_compaction_test.go
internal/conversation/flow/resolver_history_messages.go
internal/conversation/flow/resolver_history_pipeline_records_test.go
internal/conversation/flow/resolver_history_prepare.go
internal/conversation/flow/resolver_history_records_test.go
internal/conversation/flow/resolver_history_test.go
internal/conversation/flow/resolver_identity.go
internal/conversation/flow/resolver_loop_detection_test.go
internal/conversation/flow/resolver_memory.go
internal/conversation/flow/resolver_memory_context_test.go
internal/conversation/flow/resolver_messages.go
internal/conversation/flow/resolver_model_selection.go
internal/conversation/flow/resolver_model_selection_test.go
internal/conversation/flow/resolver_prune_test.go
internal/conversation/flow/resolver_query_header_test.go
internal/conversation/flow/resolver_retry_edit.go
internal/conversation/flow/resolver_retry_edit_test.go
internal/conversation/flow/resolver_settings.go
internal/conversation/flow/resolver_store.go
internal/conversation/flow/resolver_store_test.go
internal/conversation/flow/resolver_stream.go
internal/conversation/flow/resolver_stream_test.go
internal/conversation/flow/resolver_test.go
internal/conversation/flow/resolver_timezone.go
internal/conversation/flow/resolver_title.go
internal/conversation/flow/resolver_title_test.go
internal/conversation/flow/resolver_tool_approval.go
internal/conversation/flow/resolver_tool_approval_test.go
internal/conversation/flow/resolver_trigger.go
internal/conversation/flow/resolver_trim_test.go
internal/conversation/flow/resolver_turns.go
internal/conversation/flow/resolver_turns_test.go
internal/conversation/flow/resolver_user_input.go
internal/conversation/flow/resolver_user_input_test.go
internal/conversation/flow/resolver_user_persist.go
internal/conversation/flow/resolver_user_persist_test.go
internal/conversation/flow/resolver_util.go
internal/conversation/flow/resolver_workspace_history_test.go
internal/conversation/flow/resolver_workspace_target.go
internal/conversation/flow/schedule_gateway.go
internal/conversation/flow/session_service_test.go
internal/conversation/flow/skill_test.go
internal/conversation/flow/tool_approval_session_test.go
internal/conversation/flow/tool_closure.go
internal/conversation/flow/tool_closure_test.go
internal/conversation/flow/types.go
internal/conversation/interfaces.go
internal/conversation/runtime_contract_test.go
internal/conversation/service.go
internal/conversation/skill_activation.go
internal/conversation/types.go
internal/conversation/uimessage.go
internal/conversation/uimessage_adapter.go
internal/conversation/uimessage_background.go
internal/conversation/uimessage_convert.go
internal/conversation/uimessage_convert_background_test.go
internal/conversation/uimessage_stream.go
internal/conversation/uimessage_test.go
internal/conversation/uimessage_user_input.go
internal/decision/transaction.go
internal/decision/transaction_test.go
internal/decision/types.go
internal/decision/waiter.go
internal/decision/waiter_test.go
domains/agent/automation/heartbeat/service.go
domains/agent/automation/heartbeat/service_test.go
domains/agent/automation/heartbeat/trigger.go
domains/agent/automation/heartbeat/types.go
internal/historyfrag/compaction.go
internal/historyfrag/db_message.go
internal/historyfrag/db_message_test.go
internal/historyfrag/frag.go
internal/historyfrag/summary_test.go
internal/historyfrag/types.go
domains/agent/extension/hooks/service.go
domains/agent/extension/hooks/service_test.go
domains/agent/extension/hooks/types.go
domains/agent/mcp/connections.go
domains/agent/mcp/connections_test.go
domains/agent/mcp/http_tools.go
domains/agent/mcp/http_tools_test.go
domains/agent/mcp/jsonrpc.go
domains/agent/mcp/oauth.go
domains/agent/mcp/oauth_test.go
domains/agent/mcp/result_limit.go
domains/agent/mcp/result_limit_test.go
domains/agent/mcp/service.go
domains/agent/mcp/sources/federation/source.go
domains/agent/mcp/sources/federation/source_test.go
domains/agent/mcp/tool_gateway_service.go
domains/agent/mcp/tool_gateway_service_test.go
domains/agent/mcp/tool_registry.go
domains/agent/mcp/tool_registry_test.go
domains/agent/mcp/tool_session_store.go
domains/agent/mcp/tool_session_store_test.go
domains/agent/mcp/tool_types.go
internal/message/channel_route_lock_order_postgres_integration_test.go
internal/message/compaction_claim_postgres_integration_test.go
internal/message/compaction_concurrency_postgres_integration_test.go
internal/message/compaction_cross_owner_lock_order_postgres_integration_test.go
internal/message/compaction_epoch_postgres_integration_test.go
internal/message/compaction_finalize_postgres_integration_test.go
internal/message/compaction_lock_order_postgres_integration_test.go
internal/message/event/hub.go
internal/message/event/hub_test.go
internal/message/history_append_lock_order_postgres_integration_test.go
internal/message/runtime_fence_postgres_integration_test.go
internal/message/service.go
internal/message/service_postgres_integration_test.go
internal/message/service_test.go
internal/message/types.go
domains/agent/chat/convert/messageconv.go
domains/agent/chat/convert/messageconv_test.go
domains/agent/extension/plugins/oauth_clients.go
domains/agent/extension/plugins/service.go
domains/agent/extension/plugins/service_test.go
domains/agent/extension/plugins/types.go
domains/agent/chat/text/prune/text.go
domains/agent/chat/fence/fence.go
domains/agent/chat/fence/fence_test.go
domains/agent/chat/fence/postgres.go
domains/agent/chat/fence/postgres_integration_test.go
domains/agent/chat/fence/postgres_test.go
domains/agent/automation/schedule/service.go
domains/agent/automation/schedule/service_integration_test.go
domains/agent/automation/schedule/service_test.go
domains/agent/automation/schedule/trigger.go
domains/agent/automation/schedule/types.go
internal/session/runtime_fence_test.go
internal/session/service.go
internal/session/service_postgres_integration_test.go
internal/session/service_test.go
internal/session/subagent_config_test.go
internal/sessionruntime/commands.go
internal/sessionruntime/config.go
internal/sessionruntime/config_test.go
internal/sessionruntime/control.go
internal/sessionruntime/manager.go
internal/sessionruntime/manager_test.go
internal/sessionruntime/memory.go
internal/sessionruntime/memory_test.go
internal/sessionruntime/redis.go
internal/sessionruntime/redis_test.go
internal/sessionruntime/state.go
internal/sessionruntime/types.go
domains/agent/extension/skills/catalog.go
domains/agent/extension/skills/catalog_test.go
domains/agent/extension/skills/skills.go
domains/agent/extension/skills/skills_test.go
internal/toolapproval/flow.go
internal/toolapproval/flow_test.go
internal/toolapproval/message.go
internal/toolapproval/message_test.go
internal/toolapproval/policy.go
internal/toolapproval/policy_test.go
internal/toolapproval/runtime_fence_test.go
internal/toolapproval/service.go
internal/toolapproval/service_lifecycle_test.go
internal/toolapproval/service_target_test.go
internal/toolapproval/types.go
internal/userinput/flow.go
internal/userinput/flow_test.go
internal/userinput/interaction.go
internal/userinput/interaction_test.go
internal/userinput/payload.go
internal/userinput/plain_text.go
internal/userinput/runtime_fence_test.go
internal/userinput/service.go
internal/userinput/service_lifecycle_test.go
internal/userinput/service_store_test.go
internal/userinput/service_test.go
internal/userinput/types.go
```
