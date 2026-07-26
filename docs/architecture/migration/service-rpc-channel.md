# Server / Channel Typed RPC Contract

状态：**Direction reset accepted；Server -> Channel inventory Ready；Channel -> Server final
inbound/Turn contract Blocked by D8/AD1/D11/D12。**

权威边界：`docs/architecture/process-boundary-rpc-decision.md`

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

本文取代旧的“七个domain分别建立local/grpc、command executor迁入Channel、Channel反向调用
API/Agent/Model/Memory/Runtime/Media”设计。RPC尚未发布，仍只实现一套最终typed contract，
不保留generic、旧Turn wire、v1/v2、mixed-version、dual-register、fallback或升级迁移路径。

本次方向重置只修改RPC、composition和目录。Database Epoch、Store/transaction、owner schema、
legacy migration和安装发布兼容不变。

## 1. Scope

本文冻结：

1. 首期唯一真实业务进程边界：Server <-> Channel；
2. 当前20个generic method在新边界下的唯一去向；
3. Channel职责收缩与command执行留Server；
4. final typed inbound/Turn contract的阻断项；
5. Embedded direct与split RPC parity、build closure和删除顺序。

本文不设计Model、Memory、Runtime、Media、API或Agent standalone，也不为它们建立grpc/local
目录。`cmd/bridge`继续是Workspace数据面，不属于本控制面RPC。

## 2. 部署拓扑

七个名称对应 `domains/<owner>`，表示代码 owner/领域隔离，不表示 Go module、进程或部署单元：

```text
Server process
  API
  Agent / command / Turn
  Memory
  Model
  Runtime
  Media

Channel process
  Channel
  Email
  external platform adapters
  webhook / polling / cursor / tunnel
```

Embedded构建把Channel concrete一起装入Server，使用直接Go调用。Split Server不编译Channel
implementation，通过Channel boundary RPC client调用。Channel child不得编译Server owner的
concrete/internal；它通过final inbound/Turn boundary把输入交回Server。

## 3. 职责边界

### 3.1 Channel拥有

- 平台连接、registry、lifecycle、credential使用；
- webhook/polling/cursor、provider ACK、retry和rate-limit；
- external identity、route和Channel-owned config；
- inbound平台payload标准化；
- outbound平台投递、reaction、Email和tunnel；
- passive observation/event及已冻结的Channel persistence。

### 3.2 Server拥有

- access、settings、member role和API policy；
- command识别、catalog、执行与结果投影；
- Skill、Session、Message、Agent Turn和continuation；
- Model/Speech、Memory、Runtime File和Media业务；
- Web/CLI local Chat、HTTP、SSE和WebSocket。

Channel可以做平台协议级快速ACK或syntax normalization，但不得执行Memoh业务command，也不得
为执行command查询多个Server owner。

## 4. RPC方向

### 4.1 Server -> Channel

| 当前generic method | 最终typed capability | 状态 |
| --- | --- | --- |
| `channel.config.upsert` | Channel Admin / UpsertConfig | Ready/已切换则保留 |
| `channel.config.status` | Channel Admin / SetStatus | Ready/已切换则保留 |
| `channel.config.delete` | Channel Admin / DeleteConfig | Ready/已切换则保留 |
| `channel.webhook.set` | Channel Admin / SetWebhookEndpoint | Ready/已切换则保留 |
| `channel.connection.statuses` | Channel Status / ListConnectionStatuses | Ready/已切换则保留 |
| `channel.tunnel.status` | Channel Status / GetTunnelStatus | Ready/已切换则保留 |
| `channel.message.react` | Channel Delivery / React | Ready after parity |
| `channel.email.refresh` | Channel Email / RefreshProvider | Ready after parity |
| `channel.email.send` | Channel Email / SendEmail | Ready after parity |
| `channel.message.send` | Channel Delivery / SendMessage | Blocked by AD1 + D11 |

每个capability整体原子切换：typed client/server和parity完成后，同阶段删除对应generic caller、
handler、constant和registration。禁止双调用、按request分流和fallback。

### 4.2 Channel -> Server

原10个generic method不再一一变成owner RPC：

| 当前generic method | 新去向 |
| --- | --- |
| `server.command.access` | Server收到inbound后本地调用API access |
| `server.command.context` | Server本地组合API/Agent/Model facts |
| `server.command.execute` | command executor继续留Server并本地执行 |
| `server.command.execute_text` | Server本地result projection |
| `server.command.has_resource` | Server本地command catalog |
| `server.command.member_role` | Server本地API role reader |
| `server.command.resolve_locale` | Server本地settings reader |
| `server.skills.resolve` | Server本地Agent skill resolver |
| `server.audio.synthesize` | Server本地Model/Media；delivery仍受AD1约束 |
| `server.audio.transcribe` | Server本地Model/Media；asset input仍受AD1约束 |

这些方法的行为和字段盘点仍是final inbound/Turn contract的输入，但不保留原method边界，不生成
`memoh.api`、`memoh.model`、`memoh.memory`、`memoh.runtime`或`memoh.media` Channel command
RPC。

## 5. Final Channel -> Server contract

Channel只需要一个聚焦的Server-facing业务入口族：接收标准化Channel输入并驱动Server-owned
Agent/command流程。Proto namespace使用最终`memoh.agent`或团队最终批准的单一namespace；
它不表示Agent已成为独立进程。

输入必须typed oneof，至少覆盖：

- inbound message；
- platform interaction/control；
- tool approval response；
- user input response；
- delivery/provider observation；
- `AssetRef`列表和稳定route/identity facts。

禁止`Any`、`Struct`、`map`、raw JSON、bytes JSON或generic `method + payload`。首发前必须冻结：

- event taxonomy和field number；
- control ACK和unknown strategy；
- continuation、cancel、inject和duplicate语义；
- caller deadline、backpressure、half-close与shutdown；
- error code/detail和authorization identity；
- D12 build/generated contract identity检查。

D8未关闭时Turn/inbound切换Blocked；AD1未关闭时附件输入/输出和speech delivery Blocked；D11未
关闭时整个Send capability Blocked。不得用空附件、空Metadata或旧generic RPC组成缩水子集。

## 6. Consumer-owned Go ports

Server consumers在各自业务包定义1-3 method Channel port，例如：

```go
type ConfigWriter interface {
    UpsertChannelConfig(context.Context, channel.ConfigCommand) (channel.Config, error)
    SetChannelDisabled(context.Context, channel.StatusCommand) (channel.Config, error)
    DeleteChannelConfig(context.Context, channel.DeleteCommand) error
}

type MessageSender interface {
    SendChannelMessage(context.Context, channel.SendCommand) error
}
```

Embedded composition直接注入满足port的Channel concrete。Split composition注入
`internal/rpc/channel/client` concrete。只有签名/值映射确实不同时，才在consumer或composition
放微型adapter；不得建立所有owner统一`local`层。

Channel的Server-facing port只表达“提交标准化inbound/control”，不暴露API/Model/Memory/
Runtime子接口。Server-side handler进入Agent/command application后使用普通本地Go调用。

所有I/O port首参为`context.Context`并显式返回`error`。deadline由consumer决定；client不得
创建background timeout、吞错或伪造业务成功。

## 7. Proto与目录

```text
domains/channel/                 Channel业务和值
  internal/...                    adapter/email/postgres等Channel concrete

domains/{api,agent,memory,model,runtime,media}/
  ...                             Server-local业务；无本轮owner grpc/local模板

internal/rpc/channel/
  pb/                             final Server <-> Channel generated contract
  client/                         split adapters
  server/                         process host adapters
```

如果final inbound/Turn因职责和生成约束需要与Channel provider拆成两个proto文件，仍放在同一真实
process-boundary transport树中，并使用最终typed namespace；不得据此恢复per-owner grpc目录。

业务包不得import generated pb。RPC client/server负责pb与owner value的转换；composition root 最终留在
`cmd/agent` 与 `cmd/channel`，只负责构造和注册，不承载business policy。禁止建立
`internal/composition` 或长期 `cmd/internal/process`。

## 8. Embedded / split parity

只对跨Channel边界的完整capability运行direct/RPC parity：

- result value与nil/empty规范化；
- `errors.Is/As`和canonical gRPC code/detail；
- side effect次数和provider调用顺序；
- cancellation、deadline、payload limit；
- auth、shutdown和unavailable；
- no generic fallback/no dual registration。

Server内部API/Agent/Model/Memory/Runtime/Media之间不做local/RPC parity，因为没有第二个
transport实现。它们使用普通unit/integration test。

## 9. Build closure

### 9.1 Split Server

必须包含Server全部owner implementation和Channel RPC client；不得包含：

```text
domains/channel/internal
external Channel platform SDK implementations
Channel Email/Webhook runtime
```

Split Server可以编译`internal/rpc/channel/server`中的Server-side inbound/Turn handler；该package
只能依赖consumer ports和pb，不能借此引入Channel concrete。

### 9.2 Channel child

只包含Channel concrete及process-boundary RPC client/server；不得包含：

```text
cmd/internal/core
domains/{api,agent,memory,model,runtime,media}/internal
Server command/model/memory/runtime/media concrete
domains/api/http/chat
global db/store or global SQLC
```

### 9.3 Gate

```bash
go build ./cmd/agent
go build -tags split ./cmd/agent
go build ./cmd/channel
go test ./domains/channel/... ./internal/rpc/channel/...

set -o pipefail
go list -deps -tags split ./cmd/agent >/tmp/split-server-deps
! rg 'domains/channel/internal|internal/channel|internal/email|internal/webhooktunnel' /tmp/split-server-deps

go list -deps ./cmd/channel >/tmp/channel-deps
! rg 'cmd/internal/core|domains/(api|agent|memory|model|runtime|media)/internal|domains/api/http/chat' /tmp/channel-deps
```

`go test -tags split ./...`不证明binary依赖闭包；必须使用`go list -deps`。

## 10. 切换顺序

1. 停止并审计非Channel owner grpc/local/proto lane；不在共享工作树中盲目删除。
2. 保留确属Server -> Channel边界且已完成原子切换的Admin/Status实现。
3. command executor、skill、model、memory、runtime逻辑固定留Server；删除“迁入Channel”的目标。
4. 冻结final typed inbound/Turn contract；未闭合前保持现有generic行为但不与typed双注册。
5. 按完整Channel capability逐个切换Server -> Channel generic方法。
6. inbound/Turn final typed parity通过后一次性切换Channel -> Server入口，并同阶段删除对应10个
   generic caller/handler/registration；不建立owner RPC替代品。
7. invocation/caller为0后删除generic runtime/runtimepb和旧Turn wrapper/generated。
8. 引入build profile并证明依赖闭包；之后才做Mechanical Move。
9. Database Epoch v2和generated SQLC cleanup按原顺序串行执行，不并入transport提交。

## 11. 部署与兼容

Server <-> Channel typed RPC尚未发布，不定义mixed-version matrix。首发要求同一build/version、
同一generated contract、原子升级和同时回滚。service未注册、method `Unimplemented`或版本不匹配
都是deployment error；不得探测旧transport或fallback。

安装包文件名、镜像名、service-manager路径和bare-metal rollback兼容继续保留；它们不是RPC
wire兼容，不得据此创建compat package、alias或旧service registration。

## 12. 验收清单

- [x] 七个业务owner与两个首期进程已区分；
- [x] RPC只服务Server/Channel真实进程边界；
- [x] Server -> Channel 10个method有唯一typed去向或明确Blocked；
- [x] Channel -> Server 10个generic method改为单一inbound/Turn入口 + Server-local执行；
- [x] command executor不迁入Channel；
- [x] 不创建API/Model/Memory/Runtime/Media Channel command RPC；
- [x] 不要求每个service复制local/grpc目录；
- [ ] final typed inbound/Turn event/control schema完成D8；
- [ ] AD1 asset delivery与D11 outbound Metadata完成；
- [ ] D12 build/generated identity检测完成；
- [ ] existing非Channel owner grpc/local/proto implementation已审计并完成保留/删除决定；
- [ ] embedded direct与split Channel RPC parity全部通过；
- [ ] generic runtime和旧Turn wire consumer归零并删除；
- [ ] split Server与Channel child dependency closure通过。

因此当前允许继续Channel Admin/Status及其他已闭合Server -> Channel capability。不得继续推进
per-owner reverse RPC或Channel command executor。Full split发布仍被final inbound/Turn、AD1、
D11、D12和既有Store/transaction/Profile Gates阻断。
