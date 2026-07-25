# Process Boundary RPC Decision

状态：**Accepted**

日期：2026-07-23
目录命名更新：2026-07-24

本文件冻结 Memoh 首期目录重构、build profile 与 RPC 的部署边界。若其他架构文档中的
`domains/<owner>/local`、`domains/<owner>/grpc`、七 domain 远程化、Channel command
executor 或 owner-to-owner RPC 设计与本文冲突，以本文为准。

本决策只修改 RPC、composition 和对应目录规则。Database Epoch v1/v2、legacy migration
归档、v1 -> v2 bridge、owner schema/Goose manifest/version table、Store/transaction 顺序、
安装包文件名和 service-manager 路径兼容均不变。

## 1. 决策摘要

1. `api`、`agent`、`channel`、`memory`、`model`、`runtime`、`media` 是七个业务 owner，
   不是七个预设部署单元。
2. 首期只有两个业务进程：Server 与 Channel。
3. Server 始终包含 API、Agent、Memory、Model、Runtime、Media；首期不为这些 owner 建立
   standalone binary 或独立部署能力。
4. Channel 是唯一从 Server 拆出的 child。拆分原因是外部平台长连接、webhook、polling、
   cursor、adapter lifecycle、Email runtime 和 tunnel 等进程亲和状态。
5. RPC 只存在于 Server <-> Channel 的真实进程边界，不按业务 owner 对称复制。
6. `domains/<owner>` 表示代码 owner 与领域隔离，放业务逻辑、领域值、consumer-owned ports
   和 owner adapter；它不是 Go module 或部署单元，也不强制
   `contract/local/grpc/internal` 模板。
7. Embedded composition 直接注入 concrete service。只有 split composition 才注入 RPC
   client；不为单一 concrete implementation 建长期 `local` wrapper。
8. Channel 只做平台协议与连接职责。Command 识别/执行、Skill、Model、Memory、Runtime、
   Media 和 Agent loop 保留在 Server。
9. Channel 收到输入后发送标准化 typed inbound event 给 Server；Server 完成业务决策后，
   通过 Channel delivery contract 发送结果。
10. Agent 是否将来拆为独立 worker 必须另立决策，前置条件是 durable run journal、lease/
    fencing、resume、cancel/inject routing 和失败恢复闭合；本轮不预造 Agent standalone RPC层。

## 2. 判定规则

“有数据库状态”不等于“需要 RPC”。是否建立 RPC 只由以下条件决定：

```text
存在真实独立进程
  AND
存在必须跨该进程边界完成的调用
  AND
该调用的owner、错误、deadline、幂等和恢复语义已冻结
```

Model、Memory、Media 等具有持久化数据，但其 application service 可以作为无进程亲和状态的
Server-local 代码横向扩展。为未来可能拆分而提前增加 `grpc`、`local`、proto 和 parity 层违反
YAGNI，也会扩大生成代码、错误映射、版本和测试矩阵。

## 3. 状态分类

| 模块/能力 | 当前状态性质 | 首期部署结论 |
| --- | --- | --- |
| Channel adapters | 平台长连接、webhook/polling、cursor、registry、retry、tunnel | 独立 Channel 进程 |
| Email runtime | provider listener、outbox delivery、runtime lifecycle | 跟随 Channel |
| Agent loop | in-flight run、stream、cancel、inject、approval/input、session lock、进程内claim | 留 Server；未来单独评审 worker化 |
| API HTTP/SSE/WS | 连接是临时状态；业务快照来自持久化层 | 留 Server；横向扩展前补共享event distribution |
| Model/Memory/Media | 持久化状态，无首期独立进程需求 | Server-local，无owner RPC包 |
| Runtime/Workspace | container/exec/bridge 数据面会话 | 首期 Server-local；`cmd/bridge`继续作为既有数据面 |
| Schedule/Heartbeat | background worker、claim/lease语义 | Server-local；通过claim/lease解决副本并发，不因其存在拆RPC |

API SSE 当前不是事件级无损恢复：会话消息只回放固定 50 条backlog，activity stream无durable
cursor，event hub 是进程内 fanout，buffer 满时允许丢事件并要求 REST 刷新。因此可以承诺
“最终界面可重建”，不能承诺“任意副本重连无损”。真正横向无损需要 durable sequence、SSE
`id`/`Last-Event-ID`、共享 fanout/event log 和 snapshot reconciliation；这属于独立 Gate。

## 4. 首期拓扑

```text
embedded
  memoh
    API + Agent + Channel + Memory + Model + Runtime + Media
    all calls are direct Go calls through consumer-owned ports

split
  memoh-server
    API + Agent + Memory + Model + Runtime + Media
    Channel boundary RPC client/server only

  memoh-channel
    Channel + Email + external platform adapters
    Channel boundary RPC client/server only
```

`split` build tag只选择这两个 composition profile。它只裁剪 Channel implementation，不裁剪
Model、Memory、Runtime、Media 或 Agent implementation，也不创建 `split_model`、
`split_memory` 等组合tag。

## 5. RPC 边界

### 5.1 Server -> Channel

Server 可以调用四组 Channel-owned typed capability：

| Capability | 责任 |
| --- | --- |
| Admin | config upsert/status/delete、webhook endpoint |
| Delivery | message send、reaction；附件/Metadata等待AD1/D11 |
| Status | connection/tunnel status |
| Email | refresh provider、send email |

这些 capability 的业务 owner 是 Channel，RPC adapter 只做 transport mapping，不承载业务
policy。Embedded 直接调用 Channel concrete service，split 使用同一 consumer port的 RPC client。

### 5.2 Channel -> Server

Channel 不再分别调用 API、Agent、Model、Memory、Runtime、Media RPC。它只发送
Channel-boundary typed input：

- normalized inbound message/event；
- platform interaction/control response；
- delivery receipt 或 provider observation；
- 启动 Agent turn 所需的稳定 Channel facts与 `AssetRef`。

Server 内部完成 access、settings、command、skill、session、model、speech、memory、runtime 和
media调用。现有 `server.command.*`、`server.skills.resolve`、`server.audio.*` generic method只作
行为盘点输入，最终全部由 Server-local调用替代，不生成对应 owner RPC。

最终 inbound/Turn contract仍受D8、AD1、D11、D12约束：typed oneof、ACK、unknown strategy、
field number、continuation、asset delivery、Metadata projection和build identity未冻结前，不切换
对应 capability，也不使用旧 generic RPC兜底。

### 5.3 禁止项

- 禁止 `domains/api/grpc`、`domains/model/grpc`、`domains/memory/grpc`、
  `domains/runtime/grpc`、`domains/media/grpc` 作为首期 Channel 反向依赖；
- 禁止把 Channel command executor迁入 Channel；
- 禁止 `PeerService`、`GlobalService`、generic method map、`Any`、raw JSON/bytes JSON；
- 禁止双写、双调用、dual registration、fallback、compat DTO/package、mixed-version；
- 禁止按请求条件在 direct/local 与 RPC间分流；profile在编译期确定；
- 禁止为未来独立部署预造空 `local`、`grpc` 或 standalone binary。

## 6. 目录规则

```text
cmd/
  agent/                  # Server composition root
  channel/                # Channel composition root

domains/
  api/                    # HTTP/API business and transport
  agent/                  # Agent loop, chat, turn, command, automation
  channel/                # Channel business, adapters, email, webhook
  memory/                 # Memory business and owner adapters
  model/                  # Model/provider/catalog business and owner adapters
  runtime/                # Runtime/workspace business and owner adapters
  media/                  # Asset/storage business and owner adapters

internal/
  rpc/
    channel/
      pb/                 # final typed Server <-> Channel wire only
      client/
      server/
```

`domains/`只表达代码所有权，不表达进程、Go module或独立发布边界。Server与Channel的最终
composition root分别保留在`cmd/agent`和`cmd/channel`；禁止建立`internal/composition`、
长期`cmd/internal/process`或新的共享wiring巨包。`cmd`只组合各domain公开contract、constructor
或明确的公开composition入口，不承载业务policy。

`domains/<owner>/internal`仍可用于阻止其他owner导入具体provider/postgres实现，但业务逻辑不应
为了隐藏而全部塞入`internal`。`internal/rpc/channel`是部署边界transport，不是新的业务owner。
`domains/<owner>/internal/**`只能由该owner子树导入；`cmd/**`、其他owner和
`internal/rpc/channel/**`都不得越过该边界。跨owner调用只能依赖公开domain contract与由consumer
定义的最小port，依赖图必须保持无环。

当`cmd`需要构造owner私有实现时，优先由owner根package暴露普通constructor；只有FX子图确实较大时，
才允许按需建立`domains/<owner>/compose`薄入口。`compose`不是composition root：不得选择profile、
启动进程或承载业务policy。`internal/rpc/channel`只可依赖公开domain contract与generated pb；
protobuf类型不得进入domain package。

不要求保留`domains/channel/local`。如果 concrete Channel service已满足consumer port，embedded
composition直接注入它；只有确实需要值映射的微型adapter才放在消费方或composition，不建立
统一`local`层。

## 7. 实施顺序

总顺序保持：

```text
Guard / owner
  -> Store / transaction
  -> Channel boundary RPC
  -> build profile
  -> Mechanical Move
  -> Database Epoch v2
  -> generated cleanup
```

RPC阶段的新顺序：

1. 停止新增或扩展非Channel owner的grpc/local/proto实现。
2. 保留已经完成且确属Server -> Channel边界的Admin、Status等typed capability。
3. 将Channel command/skill/model/memory/runtime调用方向改为typed inbound -> Server-local执行。
4. 冻结最终inbound/Turn/interaction typed contract；未闭合能力保持Blocked。
5. 每个Channel capability原子切换并同步删除对应generic caller/handler/registration。
6. 删除无consumer的owner grpc/local/proto及旧generic runtime；再启用split dependency guard。

## 8. 验收

- `go list -deps -tags split ./cmd/agent`不包含Channel adapter、Email、Webhook或
  `domains/channel/internal`；
- `go list -deps ./cmd/channel`不包含Server业务实现：API、Agent、Model、Memory、Runtime、
  Media concrete/internal；
- production只有Channel boundary generated proto；不存在为首期Channel反向调用创建的owner
  grpc package；
- Embedded direct path与split RPC path对完整Channel capability做parity；Server-local owner之间
  不做local/RPC parity；
- Channel package不执行command、skill、model、memory或runtime业务；
- Agent loop的进程内状态、SSE的有损/恢复语义均有显式guard，不被错误描述为无状态无损；
- 数据库与安装发布兼容设计保持原决策。
