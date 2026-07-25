# Channel Business Owner / Process Boundary 迁移账本

状态：Direction Update Accepted；详细Move账本仍为Discussion Draft

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

主审范围：`internal/channel/**`、`internal/email/**`、`internal/webhooktunnel/**`、
`internal/messaging/**`、`domains/agent/command/**`、`domains/agent/command/syntax/**`、
`domains/agent/command/slash/**`、`internal/pipeline/**`、`internal/i18n/**`、`cmd/channel/**`、
`cmd/internal/channel/**` 和当前 Channel 双向 runtime RPC。另审计 Channel-owned HTTP、
Attachment/Media consumer、数据库 query/contract/migration 与测试资产。

上位约束：

- `docs/superpowers/specs/2026-07-17-channel-boundary-design.md` 是已批准边界，
  `turn.Service`、`team_id`、幂等、取消/背压、数据写入规则不得回退；
- `docs/architecture/process-boundary-rpc-decision.md` 决定首期只有Server/Channel进程边界，
  command、skill、Agent、Model、Memory、Runtime和Media业务全部留Server；
- `docs/architecture/build-profile-service-blueprint.md` 与
  `docs/architecture/process-boundary-rpc-decision.md` 决定 Channel owner、单一 `split`
  build profile，以及 split Server 不编译 Channel implementation；目标代码根为
  `domains/channel`（仅领域隔离，不是部署服务）；
- 本文件是 current -> target 迁移账本，不表示当前目录已经迁移完成。
- 现状事实：Channel根contract已迁入`domains/channel/*`；Media根contract已迁入
  `domains/media/media.go`。其余过渡代码后续迁入`domains/<owner>`。composition root最终只在
  `cmd/agent`、`cmd/channel`；禁止建立 `internal/composition` 或长期 `cmd/internal/process`。

## 1. 审计结论

Channel 不是一个可机械 `git mv internal/channel domains/channel` 的包（也不得把已迁入的
`domains/channel` 根contract误当成Channel全部迁移完成）。当前代码至少混合了：

1. 可被 Server/Agent 消费的发送、配置和状态 contract；
2. 外部平台 adapter、连接生命周期、入站处理、出站渲染；
3. Web/CLI 本地 Chat transport；
4. Channel 管理 API 与公开 webhook/media HTTP；
5. Email provider/runtime；
6. Slash command 的平台输入、渲染和不应留在Channel的跨域业务执行；
7. 入站观察/DCP persistence 与 discuss driver；
8. 通用 runtime RPC envelope 和双向 method fan-out；
9. SQLC 直接访问，以及 Account/Bot/Session/Message/Media/Model 等实现依赖。

因此迁移类型以 **Split** 为主。目标不是保留一个更深的巨型 `channel` package，而是让：

```text
split Server -> Channel consumer ports + internal/rpc/channel/client
embedded     -> Channel concrete direct injection
cmd/channel  -> Channel concrete + internal/rpc/channel client/server
```

任何 `-tags split ./cmd/agent` 的依赖闭包都不得包含
`domains/channel/internal`或外部平台 SDK。

## 2. 目标目录

```text
domains/channel/
  channel.go                    # Runtime/Send/Admin 等稳定小 contract；不含实现
  message.go                    # ChannelType、Message、StreamEvent；附件wire只引用memoh.media.AssetRef
  email.go                      # Email admin/runtime DTO 与小接口
  internal/
    adapter/                    # adapter SPI、registry、descriptor、schema
      dingtalk/
      discord/
      feishu/ws/
      line/
      matrix/
      misskey/
      qq/
      slack/
      telegram/
      wechatoa/
      wecom/
      weixin/
    gateway/                    # Manager、connection reconcile、lifecycle、queue
    inbound/                    # external inbound only
    outbound/                   # prepare/chunk/render/filter/stream
    route/
    identity/
    access/                     # inbound ACL consumer adapter；管理授权仍归 API
    interaction/                # 平台interaction normalization；不执行业务command
    observe/                    # canonical inbound event/adapt/project/render/store
    discuss/                    # discuss cursor/context/driver/turn response
    email/
      generic/
      gmail/
      mailgun/
    webhook/
      tunnel/
    http/                       # external webhook、QR、public media；Echo 只在这里
    i18n/
    postgres/                   # Channel-owned query adapters；不暴露 sqlc record
    test/
      store/                    # compiled helper imported only by tests
      fixture/                  # cross-adapter canonical parts fixture

domains/api/http/chat/         # Web/CLI local transport，不属于 Channel domain module
domains/api/http/channel/      # authenticated Channel management API
domains/api/http/email/        # authenticated Email management/OAuth/outbox API
domains/api/internal/identity/link/ # link code 与 user-channel identity binding
domains/api/internal/bot/access/    # manager grant 与 ACL administration

internal/rpc/channel/
  client/                       # split process-boundary adapter
  server/                       # process host adapter
  pb/                           # final typed Server <-> Channel wire
```

Command parser、catalog、executor、i18n和result projection归Agent/Server。Channel只把平台消息
或interaction标准化为final typed inbound/control，并将Server返回的delivery command适配到外部
平台。不得为这条调用链建立API/Agent/Model/Memory/Runtime分散RPC，也不保留Channel command
contract或executor。

## 3. 当前 package -> 目标 package 总表

| 当前 package | 目标 | 类型 | 说明 |
| --- | --- | --- | --- |
| `internal/channel` | `domains/channel` + `internal/{adapter,gateway,outbound,postgres}` | Split | 根包同时含 contract、runtime、store、manager、rendering；不建立`local` wrapper |
| `internal/channel/adapters/*`（除 `local`） | `domains/channel/internal/adapter/*` | Move | 外部平台实现，仅 embedded/standalone 可达 |
| `internal/channel/adapters/feishu/wsclient` | `domains/channel/internal/adapter/feishu/ws` | Move | 修复复合包名 |
| `internal/channel/adapters/local` | `domains/api/http/chat/stream` | Split/Delete | Web SSE/WS hub 是 API/Chat transport；不进入 split child |
| `internal/channel/inbound` | `domains/channel/internal/inbound` | Split | 保留外部入站；巨型 processor 切 ports/文件 |
| `internal/channel/identities` | `domains/channel/internal/identity` + `postgres` | Split | 业务值/服务与 SQLC 分离 |
| `internal/channel/route` | `domains/channel/internal/route` + `postgres` | Split | 去除对 `conversation.Service` 的直接调用 |
| `internal/channel/publicmedia` | `domains/channel/internal/http` | Move | 签名路径属于 Channel public delivery surface |
| `internal/channel/common` | 各 adapter 邻近文件 | Delete | 不保留 `common`；logging/proxy 分别放入 adapter 包 |
| `internal/channel/channeltest` | `domains/channel/internal/test/store` | Move | 多个包测试共用的编译型 store helper |
| `internal/channel/partsfixture` | `domains/channel/internal/test/fixture` | Move | 多 adapter 测试共用 canonical parts fixture |
| `internal/email` | `domains/channel/email.go` + `internal/email` + `internal/postgres` | Split | 跨边界DTO公开；各consumer就地定义port；runtime与DB adapter内收 |
| `internal/email/adapters/*` | `domains/channel/internal/email/*` | Move | Email 运行实现只在 Channel 构建 |
| `internal/webhooktunnel` | `domains/channel/internal/webhook/tunnel` | Move | Channel 后台任务与 public base |
| `internal/messaging` | consumer-owned sender ports + Channel outbound adapter | Delete | 当前 Executor 是跨 owner 适配器，不保留顶层包 |
| `domains/agent/command` | `domains/agent/command` | Split/Move | parser/catalog/executor/result projection留Server；Channel adapter只保留平台normalization |
| `domains/agent/command/syntax` | `domains/agent/command/syntax` | Move | Server-owned invocation parser |
| `domains/agent/command/slash` | `domains/agent/command/slash` | Move | 业务classifier留Server；Channel只提交typed interaction |
| `internal/pipeline` | `domains/channel/internal/{observe,discuss}` | Split | 继承已批准的 Channel ownership |
| `internal/i18n` | Agent command i18n + Channel provider locale | Split | 业务command文案留Server；平台投递/错误文案跟随adapter |
| `cmd/internal/channel` | `domains/channel`公开constructors + `cmd/{agent,channel}` composition | Split/Delete | 业务 adapter 移入owner，最终删除共享巨型 wiring 包；不建立`local`层 |
| `cmd/channel` | `cmd/channel` | Keep/Split | `main.go` 留 composition root；RPC/wiring 下沉 |
| `internal/rpc/channelruntime` | `internal/rpc/channel/{client,server,pb}` | Split/Delete | Server -> Channel typed capability |
| `internal/rpc/serverruntime` | `internal/rpc/channel/{client,server,pb}` final inbound/Turn | Rewrite/Delete | 原command/skill/audio行为由Server boundary handler本地调用owner，不生成owner RPC |
| `internal/rpc/runtime*` | final Channel boundary pb + 最小共享transport | Split/Delete | 删除generic method map，不建立per-owner pb |

## 4. `internal/channel` 根文件逐项映射

| 当前文件/代码 | 目标 | 类型 | 精确动作 |
| --- | --- | --- | --- |
| `types.go` | `domains/channel/{message,channel,config}.go` 与 internal DTO | Split | `ChannelType/Message/StreamEvent/SendRequest/ReactRequest`及API/local/gRPC共享的`Config`/`AdminConfig`留root contract；Message附件只引用唯一`memoh.media.AssetRef`。当前`Attachment`仅可作为Channel内部platform delivery转换值并在AD1切换时删除，不得成为root/wire contract；`InboundMessage/BindingCriteria`和SQL persistence record内收；禁止一文件继续承载全部wire/domain值 |
| `runtime.go` 的 `Runtime` | API/Agent各consumer `port.go` + `internal/rpc/channel/server` handler port | Split/Delete | 不把giant provider interface搬到root；admin/send/status consumer各定义最小接口，direct concrete与RPC client隐式实现 |
| `runtime.go` 的 `LocalRuntime` | consumer/composition微型adapter或删除 | Split/Delete | embedded直接注入Channel concrete；只有确需值映射时才保留adapter |
| `adapter.go` | `domains/channel/internal/adapter/contract.go` | Move | Adapter/Sender/Receiver/stream/webhook SPI 与 `BaseConnection` |
| `capabilities.go`, `directory.go`, `schema.go`, `target.go` | `domains/channel/internal/adapter/{capability,directory,schema,target}.go` | Move | 只被 adapter catalog/API projection 使用；API 通过 contract DTO/RPC 获取，不导入 internal |
| `registry.go` | `domains/channel/internal/adapter/registry.go` | Move | Registry 只能位于 Channel implementation；split Server 的 `/channels` API 改调用 catalog client |
| `config.go` | `domains/channel/internal/adapter/config.go` | Move | adapter config decode helper |
| `connection.go`, `manager.go`, `inbound.go` | `domains/channel/internal/gateway/{connection,manager,inbound}.go` | Move | connection reconcile、queue、workers、send/react；保留 context shutdown |
| `lifecycle.go` | `domains/channel/internal/gateway/lifecycle.go` | Move | config 写入与 connection rollback；store interface 由 gateway consumer 定义 |
| `processor.go` | `domains/channel/internal/inbound/processor.go` | Move | `InboundProcessor` 小接口 |
| `observer.go` | `domains/channel/internal/outbound/observer.go` | Move | Web mirror observer 从实现中删除，改为跨边界 event feed；外部 outbound tee 保留 |
| `outbound.go` | `domains/channel/internal/outbound/{policy,chunk,delivery,stream}.go` | Split | 当前 1,250 行混合 policy/chunk/capability/Manager method/stream state；先按上述四文件拆，再移动 |
| `outbound_prepare.go` | `domains/channel/internal/outbound/{prepare,source}.go` | Split | HTTP/data/container/persisted asset 来源拆开；直接 `media.Service` 改为 Channel consumer-owned AssetPort |
| `prepared_outbound.go` | `domains/channel/internal/outbound/prepared.go` | Move | adapter-facing prepared value，只在 domain module internal |
| `parts_render.go`, `format.go` | `domains/channel/internal/outbound/{part,format}.go` | Move | Markdown/plain degradation 与 capability coercion |
| `toolcall_filter.go`, `toolcall_format.go`, `toolcall_formatters.go`, `toolcall_summary.go` | `domains/channel/internal/outbound/tool.go`（可分多文件同 package） | Move | tool lifecycle IM policy；basename 可保留，package 为 `outbound` |
| `error_redaction.go` | `domains/channel/internal/outbound/redact.go` | Move | IM secret redaction；禁止成为共享安全 helper |
| `normalize.go`, `attachment_bundle.go` | `domains/channel/internal/outbound/attachment.go` + `inbound/attachment.go` | Split | 入站 normalize 与出站 media bundle 转换按方向拆分 |
| `skill_metadata.go` | `domains/channel/internal/inbound/skill.go` | Move | inbound skill metadata security gate |
| `public_host.go` | `domains/channel/internal/webhook/host.go` | Move | tunnel/public URL SSRF 边界 |
| `webhook_endpoint.go` | contract request/response + `internal/webhook/endpoint.go` | Split | DTO/sentinel 给 admin client；路径校验与 adapter mutation 内收 |
| `webhook_handler.go` | `domains/channel/internal/http/webhook.go` | Move | 外部平台 webhook HTTP endpoint；是允许依赖 Echo 的边界 |
| `service.go` | `internal/{config,identity}/service.go` + `internal/postgres/{config,binding}.go` | Split | `Store` 必须消失；bot config、Matrix cursor、user binding 分离；SQLC row 不越过 postgres adapter |

`service.go` 的方法按职责拆分：

- `UpsertConfig/DeleteConfig/UpdateConfigDisabled/ResolveEffectiveConfig/ListBotConfigs/
  ListConfigsByType/SaveMatrixSyncSinceToken` -> Channel config service/store；
- `UpsertChannelIdentityConfig/GetChannelIdentityConfig/ListChannelIdentityConfigsByType/
  ResolveChannelIdentityBinding` -> binding reader/writer；若最终判定 user binding 由 API 拥有，
  Channel 只保留只读 projection port，不复制写入；
- adapter self-discovery/normalize 留 config service，SQL 参数构造下沉 `internal/postgres`。

## 5. 外部 adapter 映射

以下均为 **Move**，文件 basename 保持，测试随包移动。目标路径都在 domain module `internal`，
因此 split Server 从类型系统上无法导入平台 SDK。

| 当前目录 | 生产文件 | 目标目录 |
| --- | --- | --- |
| `internal/channel/adapters/dingtalk` | `client.go, config.go, descriptor.go, dingtalk.go, inbound.go, outbound.go, stream.go` | `domains/channel/internal/adapter/dingtalk` |
| `.../discord` | `config.go, descriptor.go, discord.go, message_rich.go, rich_escape.go, stream.go` | `domains/channel/internal/adapter/discord` |
| `.../feishu` | `bot_identity.go, config.go, descriptor.go, directory.go, feishu.go, inbound.go, inbound_mentions.go, inbound_post.go, message_rich.go, quoted_message.go, sender_profile.go, stream.go, webhook_handler.go` | `domains/channel/internal/adapter/feishu` |
| `.../feishu/wsclient` | `client.go, fragment.go` | `domains/channel/internal/adapter/feishu/ws` |
| `.../line` | `adapter.go, client.go, config.go, inbound.go, media.go, outbound.go, stream.go` | `domains/channel/internal/adapter/line` |
| `.../matrix` | `config.go, markdown.go, matrix.go, stream.go` | `domains/channel/internal/adapter/matrix` |
| `.../misskey` | `client.go, config.go, descriptor.go, misskey.go` | `domains/channel/internal/adapter/misskey` |
| `.../qq` | `client.go, config.go, descriptor.go, face_tags.go, factory.go, qq.go, receive.go, send.go, stream.go, target_resolver.go` | `domains/channel/internal/adapter/qq` |
| `.../slack` | `config.go, descriptor.go, emoji.go, message_rich.go, rich_escape.go, slack.go, stream.go` | `domains/channel/internal/adapter/slack` |
| `.../telegram` | `ask_user.go, config.go, descriptor.go, directory.go, markdown.go, message_rich.go, parts.go, rich_transport.go, stream.go, telegram.go` | `domains/channel/internal/adapter/telegram` |
| `.../wechatoa` | `client.go, config.go, inbound.go, protocol.go, security.go, wechatoa.go` | `domains/channel/internal/adapter/wechatoa` |
| `.../wecom` | `callback_cache.go, config.go, crypto.go, http_client.go, inbound.go, outbound.go, protocol.go, wecom.go, ws_client.go` | `domains/channel/internal/adapter/wecom` |
| `.../weixin` | `client.go, config.go, context_cache.go, crypto.go, inbound.go, outbound.go, qr_handler.go, types.go, weixin.go` | `domains/channel/internal/adapter/weixin`；`QRServerHandler` 拆到 `internal/http/qr.go` |

跨 owner 修正：Discord/Feishu/Matrix/Slack/QQ/WeCom/Weixin 不得 import Media owner 实现；
附件 wire 使用 `domains/media/attachment`；data mount 归 `domains/runtime`，
media access path/storage-key 归 `domains/media`；Media Service 位于 `domains/media/asset`，
adapter 构造时注入 Channel 定义的 `AssetReader/AssetWriter`。Telegram 的 `userinput` 与
`command` 特例移为 command/continuation contract，不允许 adapter 直接持有 Agent service。

## 6. Web/CLI local transport 必须移出 Channel

| 当前代码 | 目标 | 类型 | 原因/动作 |
| --- | --- | --- | --- |
| `internal/channel/adapters/local/{hub,broadcaster,web,descriptor}.go` | `domains/api/http/chat/stream` | Split/Delete | RouteHub、SSE/WS broadcaster、Web adapter 是 Server 本地 UI transport；不应成为 standalone Channel adapter |
| `internal/handlers/local_channel.go` | `domains/api/http/chat/{http,ws,quick,attachment,speech}.go` | Split | 2,100+ 行同时含 REST、WS、quick action、auth、session、media；按 route/职责拆文件 |
| `cmd/agent/http_providers.go:provideWebHandler` | `cmd/agent` common API wiring | Keep/Rewrite | 直接装配 Chat/Agent/Media ports，不依赖 Channel Manager/Store/Registry |
| `cmd/internal/channel.ServerLocalModule` | 删除 | Delete | split Server 不再通过“保留一小块 Channel implementation”支持 Web |

Web `GET /stream`、`POST /messages`、`GET /ws`、quick-actions 直接属于 API/Chat。
外部 Channel 事件若要镜像 Web，不再共享进程内 `RouteHub`：embedded local adapter 可直接
publish；split 使用 Agent/Chat event hub 或明确的 event RPC。不得因此让 split Server
import `domains/channel/internal`。

`domains/agent/command/slash` 的纯 classifier/value 可以被 Web 使用；IM command executor、i18n 和
platform callback rendering 不能被 Web handler 反向导入。

## 7. Inbound、Pipeline 与 Command 的代码拆分

### 7.1 Inbound

| 当前文件 | 目标 | 类型 | 拆分要求 |
| --- | --- | --- | --- |
| `inbound/channel.go` | `internal/inbound/{processor,turn,command,continuation,attachment,speech,session,stream,token}.go` | Split | 保持一个 package 可先拆文件；每个外部 owner 只经下列 port |
| `inbound/dispatcher.go` | `internal/inbound/dispatcher.go` | Move | per-route inject/queue/parallel |
| `inbound/identity.go` | `internal/inbound/identity.go` | Move | identity/policy middleware；DB implementation 不进入此包 |
| `inbound/result_render.go` | `domains/agent/command/render.go` | Move | Command Result -> Channel Message 属 command presentation |
| `inbound/user_input_plain_text.go` | `internal/inbound/continuation.go` | Move | continuation contract 调 Agent，不直接 import Agent implementation |

当前 `ChannelInboundProcessor` 已经定义了若干 consumer-owned interface，但 DTO 仍泄漏
`bots`、`session`、`message`、`media`、`skills`、`userinput`。迁移时冻结为 Channel 自有 DTO，
local 与 RPC adapter 同时实现；禁止接口签名返回其他 domain module 的 internal type。

### 7.2 Observe/Discuss

| 当前 `internal/pipeline` 文件 | 目标 | 类型 |
| --- | --- | --- |
| `adapt.go, pipeline.go, projection.go, rendering.go, types.go` | `domains/channel/internal/observe` | Move |
| `persistence.go` | `domains/channel/internal/postgres/event.go` + `observe.Store` | Split |
| `context.go, driver.go, turn_response.go` | `domains/channel/internal/discuss` | Move/Split |

保持已批准规则：`bot_session_events` 是 Channel 写入的入站观察事实；Discuss 只能经
`turn.Service` 发起 Turn。`driver.go` 的 `DiscussCursorStore` 留在 consumer，SQL 实现下沉。
`agent/event` 纯 payload 豁免应逐步由 `turn.Event` contract 替换，不扩大豁免。

### 7.3 Command

旧的“把42个action及其owner ports搬入Channel”方向已取消。42/42盘点仍可作为行为覆盖清单，
但不再产生Channel command application、per-owner RPC或local/RPC parity。最终owner如下：

| 当前文件族 | 目标 | 类型 |
| --- | --- | --- |
| parser、registry、catalog、executor、result/formatter、i18n | `domains/agent/command` | Split/Move；Server-local application |
| access/settings/session/model/memory/runtime等action adapters | 对应Server consumer port或Agent command adapter | Split；普通Go调用，不建立owner RPC |
| Channel callback/interaction payload normalization | `domains/channel/internal/interaction` | Split | 只解码平台payload并提交typed inbound/control，不执行业务command |
| `domains/agent/command/syntax/*`、业务`domains/agent/command/slash/*` | `domains/agent/command/{syntax,slash}` | Move | Web与外部Channel输入最终都由Server解释 |

现有`server.command.*`generic methods仅用于盘点字段和行为。Final inbound/Turn contract完成后，
Channel一次性切到“提交normalized input”，并同阶段删除这些generic caller、handler、constant和
registration。禁止把它们逐个改造成API/Agent/Model/Memory/Runtime RPC。

## 8. Email 与 Webhook

| 当前文件 | 目标 | 类型 |
| --- | --- | --- |
| `email/types.go`, `runtime.go` | `domains/channel/email.go` + internal values | Split |
| `email/provider.go` | `domains/channel/internal/email/provider.go` | Move |
| `email/manager.go`, `trigger.go` | `domains/channel/internal/email/{manager,trigger}.go` | Move/Split；Trigger 经 Turn port |
| `email/service.go` | `internal/email/service.go` + `internal/postgres/{provider,binding}.go` | Split；SQLC record 不外泄 |
| `email/outbox.go` | `internal/email/outbox.go` + `internal/postgres/outbox.go` | Split |
| `email/oauth_token_store.go` | `internal/email/oauth.go` + `internal/postgres/oauth.go` | Split |
| `email/adapters/generic/adapter.go` | `internal/email/generic/adapter.go` | Move |
| `email/adapters/gmail/adapter.go` | `internal/email/gmail/adapter.go` | Move |
| `email/adapters/mailgun/adapter.go` | `internal/email/mailgun/adapter.go` | Move |
| `webhooktunnel/manager.go` | `internal/webhook/tunnel/manager.go` | Move |

Authenticated management HTTP 不搬进 child process：

- `handlers/email_providers.go`, `email_bindings.go`, `email_outbox.go`, `email_oauth.go`
  -> `domains/api/http/email`，通过 Channel Email admin contract/RPC；
- `handlers/channel.go` -> `domains/api/http/channel`，catalog/config 经 Channel client；
- `handlers/channel_access.go` 的manager routes -> `domains/api/http/access/manager.go`；
  link code/binding routes -> `domains/api/http/account/link.go`，均不进入Channel runtime；
- `handlers/webhook_tunnel.go` -> API status endpoint，经 Channel status client。

Channel-owned public HTTP 必须进入 child/embedded implementation：

- `channel.NewWebhookServerHandler` -> `domains/channel/internal/http/webhook.go`；
- `weixin.NewQRServerHandler` -> `domains/channel/internal/http/qr.go`；
- `handlers/email_webhook.go` -> `domains/channel/internal/http/email.go`；
- `handlers/public_media.go` -> Decide AD1；不得在批准前固定endpoint owner或目标package。

`public_media.go` 的最终进程owner和byte delivery方式被`service-rpc-channel.md` AD1阻断。
媒体对象唯一owner仍是Media；在AD1批准前，不得把`OpenAsset/ReadLimited/Preview`、共享路径
或`AssetReadLease`写成实施结论，也不能把Media store或SQL搬入Channel。

## 9. Messaging 与 Attachment/Media ports

`internal/messaging/executor.go` 当前把 `channel.Manager`、`media.Service` 与 attachment bundle
连接起来，而 Agent tool、Schedule 等是 consumer。目标删除该顶层 package：

- Agent 的 `send_message` consumer 在 Agent 包定义 `Sender`；
- embedded composition直接注入Channel concrete；
- split composition注入`internal/rpc/channel/client`；
- Channel outbound 的公开输入只接收 `domains/channel.Message`，其中附件为唯一
  `memoh.media.AssetRef`；AD1批准后的platform delivery materialization只属于Channel internal，
  不得重新导出`Attachment` wire type。

Channel 所需 Media port 至少拆为：

- `Ingest/Open/IngestContainer/Preview` 仅是AD1候选能力清单，不是已批准跨进程port；
- 唯一已冻结wire是无`storage_key`的Media `AssetRef`；Channel只透传且不得写message-asset link；
- 附件Send、Speech/Media RPC和public-media split在AD1前均Blocked。

接口由 `inbound`/`outbound`/`http` 各 consumer 就地定义，不能建立一个全能 `MediaService`。

## 10. Composition 与 RPC 映射

| 当前代码 | 目标 | 类型/动作 |
| --- | --- | --- |
| `cmd/internal/channel/module.go:FoundationModule` | Channel providers -> `domains/channel`公开constructors；`local.NewRouteHub` -> API/Chat composition | Split；RouteHub服务Web/CLI，不跟随Channel child |
| `ServerLocalModule` | 删除 | Web local 已归 API/Chat |
| `RuntimeModule` | Channel公开constructors + `cmd/channel` | Split |
| `EmbeddedModule` | Channel公开constructors | Move；由 `profile_embedded.go` 唯一导入 |
| `cmd/internal/channel/providers.go` registry/manager/lifecycle | Channel owner composition | Move/Split |
| 同文件 `provideChannelRouter` | Channel adapter wiring | Split；外部Server业务依赖收缩为单一inbound boundary |
| 同文件 `provideCommandHandler` | Server Agent/command composition | Move；不得进入Channel owner |
| 同文件 email/tunnel starters | Channel lifecycle hooks | Move |
| 同文件 `startWebhookTunnelListener` | `internal/http` server construction | Move；不再 import API `handlers` |
| `cmd/channel/main.go` | 保留 composition root | Keep；组合Channel公开constructors与`internal/rpc/channel`client/server |
| `cmd/channel/rpc.go` | `internal/rpc/channel` client/server | Rewrite；只承载真实进程边界，不拆owner clients |
| `cmd/agent/module.go` runtime config branch | `profile_embedded.go`/`profile_split.go` build tags | Delete/Replace |
| `cmd/agent/rpc.go` Channel client | `internal/rpc/channel/client` | Move/Rewrite |

RPC 目标拆分：

- `internal/rpc/channelruntime/channelruntime.go` 的 config/status/send/react/email/tunnel methods
  -> `internal/rpc/channel/client` + `server`；保留sentinel parity；所有I/O port（包括
  connection status）显式接收`context.Context`并返回`error`，不在client内部创建background
  timeout或吞掉远端错误；
- `channel.message.send` 必须在附件和当前`Message.Metadata`全部语义完成typed projection后整体切换；
  附件由AD1阻断，WeCom card/edit/feedback等metadata投影由D11阻断，禁止Struct/Any/JSON或旧RPC fallback；
- `internal/rpc/serverruntime/serverruntime.go` 的 command/skill/audio methods只作行为盘点；
  final inbound/Turn handler在Server内调用Agent/API/Model/Memory/Runtime concrete，旧methods随
  capability切换删除，不生成owner client/server；
- `internal/rpc/runtime/runtime.go` 的 JSON method map 和
  `runtimepb/runtime.proto` 不应成为永久应用协议；定义 typed protobuf RPC 后删除；
- `internal/rpc/auth.go`, `conn.go` 可由 platform 账本保留为最小 transport/auth，
  但不能知道 Channel method；
- Turn 的 `internal/rpc/channel/turn/grpctransport` 只作行为盘点，最终映射到
  `internal/rpc/channel`，不建立`domains/agent/grpc`。

## 11. 当前依赖与必须先建立的 ports

| 当前直接依赖 | Channel 实际需要 | 前置 port/owner | 迁移门槛 |
| --- | --- | --- | --- |
| `accounts.Service`、`bots.Service`、`session.Service` | 处理normalized inbound所需的Server facts | final inbound/Turn boundary | Channel不分别查询API/Agent；Server handler本地调用owner |
| `message.DBService/Writer` | 被动消息 row insert、stream event；asset refs只透传 | Channel passive `MessageWriter` + Agent `AssetLink` port；主动 Turn message归Agent | Channel不得写message-asset link；冻结outbox/幂等/补偿 |
| `conversation.Service`、`turn.Service` | 驱动Agent turn | final typed inbound/Turn boundary | Server-local执行；event/control schema未闭合前Blocked |
| `acl.Service`, `policy.Service`, `settings.Service` | access/settings判定 | Server-local owner调用 | Channel仅保留平台级credential/route事实 |
| `audio.Service` | synthesize/transcribe | Server-local Model/Media调用 | AD1前Blocked，不得猜delivery wire |
| `media.Service`, attachment | ingest/open/container ingest | AD1待定Media port | 仅AssetRef已冻结；无Media concrete/sqlc import |
| `skills/ContainerdHandler`、schedule/MCP/model/provider/memory/search/heartbeat/compaction | command action | Server-local Agent/owner调用 | 不进入Channel，不建立细粒度RPC |
| `dbstore.Queries` | Channel-owned query 集 | consumer-owned repositories + Channel postgres adapters | standalone 不装完整 Core queries |

当前 `cmd/channel` 仍组合 `cmd/internal/core.FoundationModule()`，导致它编译大量非 Channel
实现。完成边界收缩后，standalone composition只装database pool、Channel postgres、Channel
implementation和单一`internal/rpc/channel`transport；不得装Server owner concrete或细粒度clients。

## 12. 数据 owner 与 SQL 映射

### 12.1 Channel 唯一写入

| 语义/表 | query 文件 | 目标 adapter |
| --- | --- | --- |
| 外部身份 `channel_identities` | `channel_identities.sql` | `domains/channel/internal/postgres/identity.go` |
| bot 外部 channel config、Matrix cursor `bot_channel_configs` | `channels.sql` 中 config/cursor queries | `.../postgres/config.go` |
| route `bot_channel_routes` | `channel_routes.sql` | `.../postgres/route.go`；创建 Chat 经 Agent port |
| 入站观察 `bot_session_events` | `session_events.sql` 全部 statements | `.../postgres/event.go` |
| discuss cursor | `sessions.sql` 的 `*SessionDiscussCursor*` statements | `.../postgres/cursor.go` |
| 被动 `bot_history_messages` user 行 | 从 `messages.sql:CreateMessage` 提炼 `CreatePassiveObservedMessage` | `.../postgres/message.go`；强制无Turn关联，不得写Turn行或asset link |
| Email provider/oauth/binding/outbox | `email_providers.sql`, `email_oauth_tokens.sql`, `email_bindings.sql`, `email_outbox.sql` | `.../postgres/email_*.go` |

### 12.2 API/Agent owner，Channel 仅经 port

| 数据 | 当前 query | owner/动作 |
| --- | --- | --- |
| manager grant `bot_channel_admins` | `bot_channel_admins.sql` | API Bot Access；Channel 只问 `HasManageGrant` |
| link/binding `channel_link_codes`, `user_channel_identity_bindings` | `channel_identity_bindings.sql` | API Identity/Access；外部身份事实仍归 Channel |
| user delivery preference `user_channel_bindings` | `channels.sql` 后半 | 先由 API/Channel spec 冻结；默认 API 写、Channel 读 projection，禁止双写 |
| Chat/Session/主动 message | `conversations.sql`、`sessions.sql` 非cursor statements、`messages.sql` 非passive statements | Agent；Channel 经 ports |
| 全部 message-asset link | `media.sql` 的 `*MessageAsset*` statements | Agent `AssetLink` transaction；Channel只传 `AssetRef` |

迁移历史文件 `0001_init.up.sql/down.sql`、`0002_channel_identity_avatar`、
`0003_route_conversation_type`、`0019_add_email`、`0036_chat_sessions`、
`0064_revert_local_to_web`、`0112_team_core`、`0115_team_memberships` 等 **Keep**：历史不因
Go 目录重构改名或重写。只有 query 文件与生成 SQLC adapter ownership 改变；若未来拆 schema，
在Epoch v1另立批准migration；Epoch v2则迁入Channel owner stream并以
`channel.goose_db_version`独立跟踪。`0001_init.up.sql`只保持v1 canonical，不作为v2 baseline。

`internal/db/store.Queries` 与 `internal/db/postgres/sqlc/*.sql.go` 是当前生成/过渡层，不能
整体搬进 Channel。Channel-owned statement最终迁到`db/postgres/channel/queries`，配置为
`db/postgres/channel/sqlc.yaml`，生成到`domains/channel/internal/postgres/sqlc`。仓库级命令
可以统一调用generator，但生成type只能在Channel的`internal/postgres` adapter内出现；
service contract不含`pgtype`/`sqlc.*Row`。

ACL的observed-conversation读模型由API组合：Agent返回`route_id + last_observed_at`，Channel按
`bot_id + route_ids`返回conversation projection，API按`route_id`合并。Channel SQL不得直接读
Agent relation，也不得为SQLC复制Agent DDL；split部署只通过typed RPC传递Channel projection。

## 13. 测试与资产迁移

原则：生产文件 Move 时同 basename 测试随包；Split 文件的测试按被测职责拆分，不把旧
package 测试全部堆入一个新目录。外部 adapter 的 canonical/stream/config/integration 测试
全部保留。关键parity：embedded direct与`internal/rpc/channel`client/server对同一contract的结果、
sentinel、event 顺序、取消和 payload limit 一致。

测试文件清单按当前 package：

- `internal/channel`：`adapter_test.go,error_redaction_test.go,format_test.go,helpers_test.go,
  inbound_test.go,lifecycle_test.go,manager_integration_test.go,normalize_test.go,observer_test.go,
  outbound_prepare_test.go,outbound_test.go,parts_render_test.go,public_host_test.go,
  toolcall_filter_test.go,toolcall_format_test.go,toolcall_formatters_test.go,toolcall_summary_test.go,
  types_test.go,webhook_endpoint_test.go,webhook_handler_test.go,config_test.go,manager_test.go,
  parts_canonical_external_test.go,registry_test.go`；按第 4 节目标随职责拆；
- adapter tests：DingTalk 4、Discord 4、Feishu 11 + WS 1、Line 5、Matrix 5、Misskey 1、
  QQ 6、Slack 3、Telegram 11、WeChatOA 4、WeCom 10、Weixin 7，加
  `adapters/interface_contract_test.go`；全部随第 5 节包移动；
- local tests：`broadcaster_test.go,hub_test.go,web_test.go` -> API Chat stream；
- inbound tests：`channel_test.go,cross_platform_consistency_test.go,dispatcher_test.go,
  fallback_coverage_test.go,identity_test.go,new_confirmation_test.go,result_render_choices_test.go,
  result_render_i18n_test.go,result_render_test.go`；result render 转 command，其余 inbound；
- identity/route/public media/common：`service_test.go,service_identity_integration_test.go,
  service_integration_test.go`（当前`//go:build ignore`，不得计作有效Gate）、route
  `service_test.go,public_media_test.go,proxy_test.go`随目标；
- Email：`service_test.go`, Gmail `adapter_test.go`；Webhook tunnel `manager_test.go`；
  Messaging `executor_test.go` 改为Agent sender direct/RPC boundary parity；
- Command 16 个：`callback_confirm_test.go,callback_test.go,context_test.go,fallback_test.go,
  formatter_test.go,handler_test.go,help_i18n_test.go,humanize_test.go,language_i18n_test.go,
  link_test.go,model_picker_test.go,parser_test.go,reasoning_test.go,result_test.go,
  settings_i18n_test.go,skill_test.go`；纯 contract 与 executor tests 分开；
- Syntax `invocation_test.go`；Slash `classifier_test.go,metadata_test.go`；Pipeline
  `adapt_test.go,driver_test.go,rendering_test.go,turn_response_test.go`；i18n `i18n_test.go`；
- composition/RPC：`cmd/channel/main_test.go`, `cmd/internal/channel/providers_test.go`,
  `internal/rpc/auth_test.go`, `channelruntime_test.go`, `runtime_test.go`；移动后增加 build profile
  dependency test 与 typed RPC parity。

非 Go 资产：

- `internal/i18n/locales/{en,ja,zh}.json` -> `domains/channel/internal/i18n/locales/`，embed
  path 同提交更新；
- `internal/channel/adapters/weixin/LICENSE` -> 对应 Weixin target，保留第三方许可；
- `internal/rpc/runtimepb/runtime.proto` 由 Persistence/Platform 账本 primary 认领；Channel
  只记录其中 Channel method 的替代要求。Typed Channel proto 落地后由
  `internal/rpc/channel/pb/channel.proto` 替代对应method，最终generated删除禁止手改。

### 13.1 跨账本 call sites

以下文件不计入 Channel domain primary 的 346 个文件，但已经逐个审计；它们由
API/Agent/Media/Composition
账本迁移自身代码，同时必须更新这里定义的 contract/port import：

| 当前文件 | 本次迁移要求 | 主 owner |
| --- | --- | --- |
| `internal/handlers/channel.go` | Channel catalog/config API 改用 `domains/channel` admin client | API HTTP |
| `internal/handlers/channel_access.go` | manager routes拆到API Access；link code/binding routes拆到API Account/Identity；不进入Channel child | API HTTP |
| `internal/handlers/email_{providers,bindings,outbox,oauth}.go` | Email admin API 改用 public Email contract/RPC client | API HTTP |
| `internal/handlers/email_webhook.go` | 移入 Channel public HTTP | Channel |
| `internal/handlers/local_channel.go` | 拆到 API/Chat，本地 Web 不再经 Channel Manager | API Chat |
| `internal/handlers/public_media.go` | Decide AD1；endpoint owner、目标package和Media port均未冻结 | Decide AD1 |
| `internal/handlers/webhook_tunnel.go` | API status handler 经 Channel client | API HTTP |
| `internal/handlers/{channel_runtime_error,local_channel_runtime_contract,local_channel,public_media}_test.go` | 随上述生产职责移动/拆分 | 对应 owner |
| `cmd/agent/http_providers.go` | Web/API handlers 只装 public contract/client | API composition |
| `cmd/agent/module.go`, `module_test.go` | 改为 build-tag profile 与依赖闭包断言 | composition |
| `cmd/agent/rpc.go`, `rpc_runtime_test.go` | Channel RPC client 下沉并保留 parity | Channel client/composition |
| `domains/agent/tools/message.go`, `message_test.go` | Agent consumer-owned Sender，direct/RPC boundary两实现 | Agent |
| `domains/agent/tools/email.go` | Email sender contract，不导入 Channel internal | Agent |
| `domains/agent/tools/{attachment_bundle,contacts,tts}.go` 与 `tts_test.go` | Media/identity/speech 各走窄 contract | Agent/Media/Model |
| `internal/conversation/flow/{platform_identity,requested_skills,resolver,resolver_retry_edit}.go` 及相邻测试 | 删除 Channel concrete types；使用 Turn/Chat DTO | Agent Chat |
| `internal/session/service.go` | route/session transaction owner 需与 Channel RoutePort 对齐 | Agent Session |
| `internal/healthcheck/checkers/channel/{checker,checker_test}.go` | 只消费 Channel status contract/client | Health/API |

补充 API/composition 文件计数：10 个生产 handlers + 4 个 handler tests + 3 个
`cmd/agent` 生产文件 + 2 个 tests = 19/19 已映射。`cmd/channel` 与
`cmd/internal/channel` 的6个Go文件也由`composition-release.md` primary认领。本节这些
composition映射全部是symbol cross-reference，不加入346分母，避免与其他主审
账本重复 owner；上表用于最终交叉去重。

## 14. 文件覆盖账本

以下是基线 `go list` 的生产文件覆盖。每一行的全部 `.go` 已在前述目标中映射；测试见
第 13 节并遵循同包/职责拆分规则。

| 当前 package | 生产 Go 文件数 | 文件 |
| --- | ---: | --- |
| `internal/channel` | 32 | `adapter,attachment_bundle,capabilities,config,connection,directory,error_redaction,format,inbound,lifecycle,manager,normalize,observer,outbound,outbound_prepare,parts_render,prepared_outbound,processor,public_host,registry,runtime,schema,service,skill_metadata,target,toolcall_filter,toolcall_format,toolcall_formatters,toolcall_summary,types,webhook_endpoint,webhook_handler.go` |
| external adapters + Feishu WS | 94 | 第 5 节逐目录列出的全部生产文件 |
| `channel/adapters/local` | 4 | `broadcaster,descriptor,hub,web.go` |
| `channel/{channeltest,partsfixture,common}` | 4 | `store.go,parts_fixture.go,logging.go,proxy.go` |
| `channel/identities` | 2 | `service.go,types.go` |
| `channel/inbound` | 5 | `channel.go,dispatcher.go,identity.go,result_render.go,user_input_plain_text.go` |
| `channel/publicmedia` | 1 | `public_media.go` |
| `channel/route` | 2 | `service.go,types.go` |
| `email` + adapters | 11 | `manager,oauth_token_store,outbox,provider,runtime,service,trigger,types.go` + `generic/gmail/mailgun adapter.go` |
| `webhooktunnel` | 1 | `manager.go` |
| `messaging` | 1 | `executor.go` |
| `command` | 30 | `access,callback,commands,compact,context,email_cmd,fallback,formatter,fs,handler,heartbeat_cmd,humanize,interfaces,language,link,mcp,memory,menu,model,model_picker,parser,reasoning,registry,result,schedule,search,settings,skill,status,usage.go` |
| `commandsyntax` | 2 | `invocation.go,parser.go` |
| `slash` | 3 | `classifier.go,errors.go,metadata.go` |
| `pipeline` | 9 | `adapt,context,driver,persistence,pipeline,projection,rendering,turn_response,types.go` |
| `i18n` | 1 | `i18n.go` |
| **合计** | **202** | **全部映射** |

测试计数为 144；生产 + 测试合计 346。`internal/rpc/**` 的 10 个 Go 文件与 1 个 proto
由 `persistence-iam.md` primary 认领；本账本只做 method/symbol cross-reference。
`cmd/**` composition同理由`composition-release.md` primary认领。
覆盖校验使用同一组 scope 和 ledger package，
不是按 import 猜测：

```bash
scope=(internal/channel internal/email internal/webhooktunnel internal/messaging \
  domains/agent/command domains/agent/command/syntax domains/agent/command/slash internal/pipeline internal/i18n)

root=$PWD
find "${scope[@]}" -type f -name '*.go' -print \
  | sed "s#^#$root/#" | sort > /tmp/channel-current.txt
# ledger_packages 来自本节逐 package 行；逐包展开 GoFiles/TestGoFiles/XTestGoFiles。
go list -f '{{.Dir}}|{{join .GoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}|{{join .IgnoredGoFiles ","}}' \
  ./internal/channel/... ./internal/email/... ./internal/webhooktunnel/... \
  ./internal/messaging/... ./domains/agent/command/... ./domains/agent/command/syntax/... \
  ./domains/agent/command/slash/... ./internal/pipeline/... ./internal/i18n/... \
  | awk -F'|' '{d=$1; for(i=2;i<=5;i++){n=split($i,a,","); for(j=1;j<=n;j++) if(a[j] ~ /\.go$/) print d "/" a[j]}}' \
  | sort -u > /tmp/channel-ledger.txt
comm -23 /tmp/channel-current.txt /tmp/channel-ledger.txt
comm -13 /tmp/channel-current.txt /tmp/channel-ledger.txt
```

基线执行结果：

```text
current Go files = 346
mapped Go files  = 346
uncovered        = 0
production       = 202
tests            = 144
owned non-Go assets = 4/4 mapped
```

## 15. 推荐提交序列与验收

1. 冻结message/admin/email/final inbound contracts、query statement owner、现有行为和import guard，
   不移动实现；
2. 首个Store slice只含`channel_identities.sql`的external identity facts，以及`channels.sql`
   中`bot_channel_configs`/Matrix cursor statements：consumer-owned contract -> 旧SQLC adapter ->
   wiring -> 删除对应broad Queries methods；明确排除`channel_identity_bindings.sql`和
   `user_channel_bindings` delivery preference；
   PostgreSQL characterization必须覆盖identity稳定upsert/唯一性、config CRUD/disabled、
   Matrix cursor `updated_at`和tenant/RLS；ignored integration test不算Gate证据；
3. 下沉Route/Email/Event/Passive Message Store，建立API Link/Access、Agent Session/AssetLink等
   窄ports；
4. 在AssetLink恢复语义批准后完成跨owner transaction contract；把Web/CLI local transport移到
   API/Chat并删除`ServerLocalModule`；
5. 拆Channel根大文件和`inbound/channel.go`的职责，但仍保持旧namespace与行为；
6. 按B11已批准的Media无关schema建立`internal/rpc/channel` final typed contract；Server ->
   Channel能力逐项原子切换，Channel -> Server能力等待final inbound/Turn整体切换；不建立
   per-owner client、永久peer、dual-register或legacy fallback；
7. Store/RPC graph闭合后再建`profile_embedded.go`/`profile_split.go`，更新发布资产并验证
   split dependency closure；
8. 机械移动external adapters、gateway、outbound、observe/discuss/email/http到Channel owner internal；
9. 统一Goose Migrator将Channel表迁入`channel` schema并初始化独立version table；通过fresh、
   v1 bridge、resume和embedded/split compatibility后cutover；
10. 最后按statement owner建立SQLC target并重生成，删除旧package、broad Store与临时facade。

每步至少验证：

```bash
go test ./domains/channel/... ./domains/api/http/chat/...
go test ./internal/arch/...
go build ./cmd/channel
go build ./cmd/agent
go build -tags split ./cmd/agent
go list -deps -tags split ./cmd/agent \
  | rg 'domains/channel/(internal|local)|internal/channel|internal/email'
```

最后一条必须无输出。实际CI脚本应先单独确认`go list`成功并启用`pipefail`，避免把命令失败
误判为无依赖。另需保留 channel integration/cross-platform、Turn event parity、
RPC sentinel/cancel/backpressure、Email webhook、public media limit/signature、DB tenant/RLS 与
embedded/split 行为对拍。目录 Move、contract change、behavior change、generated SQL/proto
更新必须分提交，禁止一次批量重命名掩盖语义变化。
