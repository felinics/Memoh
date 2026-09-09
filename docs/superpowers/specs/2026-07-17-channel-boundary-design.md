# Channel边界拆分设计

日期：2026-07-17
状态：已批准（2026-07-17，二进制目录定为cmd/channel）
上游文档：四边界架构RFC（Agent／API Server／Channel／Bridge）
相关变更：PR #791（team核心与RLS，迁移`0112_team_core`）

## 1. 背景与动机

四边界RFC确定了按运行边界重组代码的方向：先落实package与composition root，再拆进程，最后决定module。本spec是该RFC的第一份落地设计，选择Channel作为第一个拆分的边界。

选择Channel先行的依据（基于2026-07-17对main分支的耦合摸底）：

- Channel与Agent之间的直接耦合集中且可枚举：出站已经通过`messaging.Sender`接口解耦（`internal/agent/tool/message.go`依赖接口，`channel.Manager`是实现）；入站的ACL、identity、route、slash命令等横切逻辑已全部位于`internal/channel/inbound/`，与RFC所有权表一致，无需搬迁。
- 当时唯一的硬连线是入站Processor直接构建`conversation.ChatRequest`并调用`flow.Runner.StreamChat()`（`internal/channel/inbound/channel.go`）。该路径现已由`agent/turn.Service`替代，具体编排归`internal/agent/application`。
- Web Chat（`internal/handlers/local_channel.go`）仍属于Agent边界，但现在同样依赖Application服务，不再依赖旧Conversation Resolver。
- Bridge已成熟无需重写；Agent需先完成Turn契约设计；API Server的拆分是别人搬走后的减法。

PR #791引入的多租户数据面（team GUC连接绑定、RLS、`TeamIDResolver`模式）要求本次定义的跨边界契约从第一版就携带`team_id`，避免后续破坏性升版。

## 2. 范围与非目标

### 范围

1. 定义Turn应用层契约包`internal/agent/turn`：命令、事件与port接口；`application.Service`直接实现进程内Turn服务。
2. Channel入站Processor与DiscussDriver改为依赖Turn port，切断对Agent Application与Runtime实现的直接依赖。
3. `internal/channel`包内目录重组（gateway／inbound／outbound分层）。
4. 建立供命令入口复用的Channel装配模块：`cmd/internal/channel`；`cmd/agent`改为组合该模块，行为不变。
5. 新增`cmd/channel`验证二进制（单实例，装配进程内Turn适配器）。

### 非目标

- 跨进程传输选型与实现（gRPC／消息队列）——留给下一份spec。
- 水平扩容、连接所有权选主、多副本Channel。
- Agent侧Run Journal、Turn状态机、可恢复性（RFC第6节）的完整落地——本spec只定义契约形状，不实现持久化Run生命周期。
- `cmd/agent`改名`cmd/memoh`（涉及Docker与发布流程，单独处理）。
- 顶层`api/`目录、Go module拆分、通用Worker。
- `ask_user`／tool approval的跨进程恢复（`decision.Waiter`仍为进程内机制，属Agent侧RFC 6.4工作）。

## 3. 数据所有权（已定决策）

所有权按**数据语义**划分，不按表划分：

| 数据 | 语义 | 唯一写入方 | 现状位置 |
| --- | --- | --- | --- |
| `bot_session_events` | 入站观察事实（DCP事件源，含不触发Turn的被动消息） | Channel | `internal/chat/timeline/persistence.go`，由`channel/inbound/channel.go`调用 |
| `bot_history_messages`被动行（role=user、无Turn关联） | 入站观察事实 | Channel | `persistPassiveMessage`（`channel/inbound/channel.go`） |
| `bot_history_messages` Turn行（触发Turn的用户消息、assistant／tool输出） | Turn产物 | Agent | `internal/agent/application/service_user_persist.go`的`persistUserTurn`及stream落库 |
| Compaction、Turn元数据 | Turn产物 | Agent | `internal/agent/context/compaction` |

规则表述：**Turn产物唯一写入方是Agent；入站事实唯一写入方是Channel。**

推论：

- 两条现有落库路径均维持现状，本spec迁移工作量为零；规则从「有例外」变为「无例外」。
- `bot_session_events`的幂等去重（`external_message_id`上的`ON CONFLICT DO NOTHING`）属于入站边界职责，留在Channel。
- `StartTurnCommand`携带消息内容与附件引用，触发Turn的用户消息由Agent在Turn生命周期内落主历史（现状即如此，Resolver属未来Agent边界）。
- 守卫规则可测试：Channel的写入路径只允许写无Turn关联的user行与session events。

## 4. 目标目录结构

```
Memoh/
├── cmd/
│   ├── agent/                    # all-in-one入口（保留，改为组合共享装配模块）
│   ├── channel/                  # 新增：Channel单实例验证二进制
│   └── internal/
│       ├── core/                 # 共享的Core命令装配模块
│       └── channel/              # 共享的Channel命令装配模块
│
├── internal/
│   ├── agent/
│   │   ├── application/          # Agent应用编排；直接实现turn.Service
│   │   ├── context/              # 上下文、历史、压缩与限制
│   │   ├── decision/             # approval、ask_user与稳定反馈
│   │   ├── runtime/              # native、ACP与per-thread runtime
│   │   ├── tool/                 # 原生工具提供者
│   │   └── turn/                 # 纯Turn port及gRPC transport
│   │
│   ├── channel/
│   │   ├── gateway/              # 重组：Manager、Registry、连接生命周期、webhook接入、入站队列
│   │   ├── adapters/             # 原样保留：平台适配器
│   │   ├── inbound/              # 原样保留：入站Processor、dispatcher、identity中间件、结果渲染
│   │   ├── outbound/             # 重组：出站准备、分段、渲染、toolcall格式化
│   │   ├── identities/           # 原样保留
│   │   ├── route/                # 原样保留
│   │   ├── common/               # 原样保留
│   │   ├── publicmedia/          # 原样保留
│   │   └── （包根）              # 共享类型与工具：types、schema、config、capabilities等
│   │
│   ├── chat/
│   │   ├── thread/               # 内部Thread生命周期与fork
│   │   ├── message/              # 消息持久化
│   │   ├── timeline/             # 规范事件、投影、渲染与落库
│   │   └── view/                 # API／UI历史投影
│   ├── messaging/                # 原地保留：出站port（Sender接口）与Executor
│   └── ...
```

### `internal/channel`包根文件映射

| 现文件（包根） | 去向 |
| --- | --- |
| `manager.go`、`registry.go`、`connection.go`、`lifecycle.go`、`processor.go`、`observer.go`、`inbound.go`（Manager入站队列）、`webhook_handler.go`、`webhook_endpoint.go` | `gateway/` |
| `outbound.go`、`outbound_prepare.go`、`prepared_outbound.go`、`parts_render.go`、`format.go`、`toolcall_filter.go`、`toolcall_format.go`、`toolcall_formatters.go`、`toolcall_summary.go` | `outbound/` |
| `types.go`、`schema.go`、`config.go`、`capabilities.go`、`target.go`、`normalize.go`、`attachment_bundle.go`、`error_redaction.go`、`directory.go`、`public_host.go`、`skill_metadata.go`、`adapter.go`、`service.go`（Store） | 包根保留（共享类型、adapter契约、配置存储） |
| `channeltest/`、`partsfixture/` | 原样保留（测试辅助） |

说明：

- 目录重组是纯移动提交（见第9节），不与接口调整混在同一PR。
- **执行修正（2026-07-17）**：实测依赖后发现上表映射不成立——`registry.go`、`prepared_outbound.go`、`parts_render.go`被包根adapter契约（`adapter.go`、`types.go`）引用，必须留在包根；`outbound.go`混合Manager方法与被adapters直接引用的chunking函数（`ChunkText`／`ChunkMarkdownText`／`OutboundPolicy`），需先拆文件才能移动。重组因此不是纯移动，已从首个落地PR中移除，推迟到修正映射后的独立PR。
- `internal/messaging`不并入`channel/outbound`：`Sender`是Agent工具与Schedule等多个消费方依赖的出站port，依赖方向（消费方依赖接口、Channel提供实现）已经正确。移动只会制造import噪音。
- `internal/email`、`internal/webhooktunnel`不搬目录，但装配归入`cmd/internal/channel`（见第7节），与RFC第8节任务归属一致。

## 5. Turn契约（`internal/agent/turn`）

### 5.1 设计原则

- 契约不携带函数、Go channel或进程内状态引用（区别于现有`conversation.ChatRequest`）。
- 所有命令与事件必填`team_id`。OSS单团队下由Channel边缘从`ChannelConfig.TeamID`填入（PR #791后config行已携带team_id）；禁止在契约层回退到`team.DefaultTeamID`。
- 幂等键从平台`external_message_id`派生，由Channel生成、Agent去重。进程内适配器按`(team_id, idempotency_key)`做进程生命周期的认领（有界注册表），重复投递返回`ErrDuplicateTurn`、Channel静默丢弃；跨进程的持久化认领随Run Journal（RFC第6节）落地。
- 进程内适配器只服务组合根注入的单一team（自托管为`DefaultTeamID`）：DB连接池按会话绑定单team GUC，非本team命令返回`ErrTeamNotServed`fail-closed，防止静默读写默认team数据；hosted多team运行时以请求级team绑定替换该守卫。
- 事件流的消费语义与RFC 6.3对齐：每个Run内顺序号单调递增；本spec阶段事件仅进程内传递，不落Run Journal。

### 5.2 契约形状（示意，字段以实现PR为准）

```go
package turn

type StartTurnCommand struct {
    SchemaVersion  int
    TeamID         string            // 必填
    BotID          string
    ThreadID       string
    RouteID        string
    Mode           Mode              // chat | discuss
    IdempotencyKey string            // 平台消息去重键
    Origin         Origin            // 平台、会话类型／名称、发送者channel identity
    Message        InboundMessage    // 文本+附件引用（content hash，不含字节）
    ReplyTarget    string
    Locale         string
}

type Event struct {
    RunID     string
    TeamID    string
    ThreadID  string
    Seq       int
    Kind      EventKind             // 文本段、附件、reaction、状态变更、终态等
    Payload   json.RawMessage
}

type RunHandle interface {
    RunID() string
    Events() <-chan Event           // 进程内阶段的订阅形式；gRPC传输映射同构的流
    Errs() <-chan error             // 终止错误通道；实现保证先排空事件再交付错误
    Inject(ctx context.Context, msg InjectMessage) error
    AddOutboundAssets(refs []OutboundAssetRef)
    Cancel()
}

type Service interface {
    StartTurn(ctx context.Context, cmd StartTurnCommand) (RunHandle, error)
    // resume命令（tool approval / user input / 纯文本ask_user推进）见turn.go；
    // eventCh参数是进程内surface，gRPC传输以流式RPC承载同样的事件序列。
}
```

> 实现偏差记录：`Inject`从`Service`收敛到`RunHandle`（进程内无需按RunID路由，跨进程由Run流的控制帧承载）；`RunHandle`增加`Errs()`与`AddOutboundAssets()`以忠实桥接现有双通道与出站资产收集语义。Go内部字段使用`ThreadID`；REST、SSE、WebSocket、数据库与gRPC proto继续保留既有`session_id`，由边界适配器转换。

`RunHandle.Events()`的Go channel是**进程内适配器的实现细节**，不属于命令契约本身；跨进程spec将定义等价的流式传输，事件结构（含Seq）不变。

### 5.3 进程内实现（`internal/agent/application`）

- `application.Service`直接实现`turn.Service`，将`StartTurnCommand`翻译为Application内部的`ChatRequest`并驱动Native或ACP runtime。
- 旧`turn/inprocess`与`conversation/flow`已经删除，不保留兼容包装。
- 事件映射完整性由Application与Turn transport测试共同保证。

### 5.4 Channel侧改造点

- `ChannelInboundProcessor`：删除对`flow.Runner`与`conversation.ChatRequest`的依赖，改为注入`turn.Service`。
- `channel/discuss.DiscussDriver`：删除对Native Agent的直接依赖，改走`turn.Service`（`Mode=discuss`）。
- `ask_user`纯文本回答与`/approve`命令：依赖`agent/decision/input`与`agent/decision/approval`端口。

## 6. Pipeline归属拆分

旧`internal/pipeline`已经按职责拆除：

| 文件 | 职责 | 归属 |
| --- | --- | --- |
| `channel/inbound/adapt.go` | 入站观察规范化 | Channel |
| `chat/timeline`中的`persistence.go`、`projection.go`、`rendering.go`、`context.go`、`types.go` | 规范事件、落库、投影与渲染 | Chat |
| `channel/discuss/driver.go` | discuss模式的Turn触发决策 | Channel，触发只走`turn.Service` |
| `chat/timeline/turn_response.go` | Turn响应转为timeline记录 | Chat，只依赖Turn port |

DiscussDriver留在Channel侧的理由：它的输入是入站观察投影（Channel拥有的数据），它对Agent的需求只是「发起一个discuss模式的Turn」，与入站Processor对Agent的需求同构。

## 7. 装配与二进制

### 7.1 `cmd/internal/core`与`cmd/internal/channel`

两个目录是仅供`cmd/**`复用的fx装配模块，不是独立composition root。真正的composition root仍是各二进制入口中的`options()`。`cmd/internal/channel`汇集Channel边界的装配（从`cmd/agent/app.go`迁出）：

- Channel Registry、Manager、Lifecycle、入站Processor、Command Handler、Webhook handler注册；
- Email Manager、Webhook Tunnel listener（RFC第8节归属Channel的后台任务）；
- 依赖注入的端口：`turn.Service`、`dbstore.Queries`、`identities.Service`、`acl.Service`等由上层composition root提供。

`cmd/internal/core`汇集数据库、Workspace、Agent、Schedule等两个二进制共同需要的装配。Go的`internal`可见性保证这两个模块只能由`cmd/**`引用。

### 7.2 `cmd/agent`

改为组合`cmd/internal/core`与`cmd/internal/channel`，注入`turn/inprocess`适配器。按配置装配两种形态之一：

- **embedded（默认，`internal_rpc.shared_secret`为空）**：`EmbeddedModule`把完整Channel运行时（外部渠道适配器、Email Manager、webhook tunnel与webhook/QR/email-webhook/公共媒体HTTP端点）嵌入本进程，等价于拆分前的all-in-one。既有裸机部署的config无需修改即可继续工作。
- **split（设置shared_secret，docker compose采用）**：Channel运行时运行在独立的`cmd/channel`进程，经带共享密钥认证的内部gRPC互联；本进程只保留Web/CLI本地渠道路径（`ServerLocalModule`），并把web/cli出站短路到本进程Manager（Web SSE订阅的是本进程的RouteHub）。

### 7.3 `cmd/channel`

装配`cmd/internal/channel`的`RuntimeModule`：外部渠道适配器、Email、webhook端点与tunnel，Turn与命令/技能/音频面经`internal/agent/turn/grpctransport`与`internal/rpc`的认证gRPC回到Server进程。

> 实现偏差记录：本spec最初把`cmd/channel`定位为「装配闭合验证、不进发布产物」，实现阶段跨进程传输（`turnpb`/`runtimepb`）已经落地，`cmd/channel`随之成为docker compose默认拓扑中的正式服务（`channel`，8081），并进入发布归档与安装脚本。裸机/单进程部署由§7.2的embedded形态兜底，二进制仍可单机可用。

## 8. 依赖规则

| 规则 | 说明 |
| --- | --- |
| `internal/channel/**`只能import `internal/agent/turn`、`internal/agent/event`与`internal/agent/decision` | Channel不得穿透到Application、Runtime或Tool实现 |
| `internal/channel/**`（`gateway`的webhook handler除外）不得import Echo | webhook接入是Channel拥有的HTTP端点，允许 |
| `internal/channel/**`、`internal/chat/timeline`不得import `fx` | 装配只在`cmd/**` |
| `internal/agent/turn`不得import Echo、fx、sqlc、`internal/channel/**`、Application或Runtime | RFC第5节规则的第一条落地 |
| `team.DefaultTeamID`仅允许`internal/db`、`cmd/**`与测试引用 | 防止业务包hardcode单团队假设 |

以上规则由`internal/arch`的守卫测试机械执行（`go test ./internal/arch/`）。当前记录在案的豁免与规则细化（与守卫测试中的豁免表一一对应）：

- `internal/agent/event`视同Turn port的一部分：turn事件payload就是序列化的agent事件，Channel侧消费该纯数据词汇包不构成边界泄漏。
- `internal/channel/route`已直接路由Bot与内部Thread；旧的伪Conversation生命周期和查询已经删除。
- `team.DefaultTeamID`豁免：`internal/memory/adapters/**`（既有单团队fallback）与`internal/channel/service.go`（configless渠道合成配置必须携带TeamID，`turn.Service`对空TeamID fail-closed）。

## 9. 提交序列

遵循RFC「目录移动、接口调整和行为变化分开提交」，五个独立可合的PR：

1. **契约包**：新增`internal/agent/turn`+`inprocess`适配器+单测。纯新增，零风险。
2. **切换依赖**：Processor与DiscussDriver改走`turn.Service`。接口调整，行为不变，靠现有channel集成测试守护。
3. **目录重组**：`internal/channel`包根按第4节映射拆入`gateway/`、`outbound/`。纯移动（`git mv`+import路径更新），快速合并减少主干冲突。
4. **装配拆分**：新增`cmd/internal/core`与`cmd/internal/channel`，`cmd/agent`接线。
5. **验证二进制**：新增`cmd/channel`+CI构建检查。

## 10. 测试与验证

- 契约包与适配器：单元测试+StreamChunk→Event对拍测试（枚举完整性）。
- 行为回归：现有`manager_integration_test.go`、`channel/inbound`全部测试、`cross_platform_consistency_test.go`必须全绿；不新增mock层。
- 验收标准：
  1. `cmd/channel`与`cmd/agent`均可构建，all-in-one行为不变；
  2. `internal/channel/**`对`conversation`／`flow`／`agent`（除`turn`）的import为零；
  3. `mise run lint`与`go test ./...`通过。

## 11. 风险与后续

| 风险 | 对策 |
| --- | --- |
| StreamChunk→Event映射遗漏导致平台消息丢失 | 对拍测试枚举chunk类型；PR2保留旧路径的测试快照对比 |
| 目录重组与快速迭代的主干冲突 | PR3为纯移动，窗口期内优先合并；与团队约定冻结channel包根的并行改动 |
| `ChatRequest`字段语义在翻译层丢失（函数／channel字段的隐式行为） | PR1阶段逐字段审计`ChatRequest`，隐式行为显式化为契约字段或适配器逻辑 |
| DiscussDriver改造影响discuss模式时序 | discuss相关pipeline测试全绿；必要时PR2拆为chat先行、discuss跟进两步 |

后续spec（不在本spec范围）：

- Turn契约的跨进程传输选型（依赖Agent侧Run Journal进度）。
- Channel连接所有权与多副本选主。
- `bot_session_events`跨团队枚举在FORCE RLS下的reconcile路径（hosted需求）。
