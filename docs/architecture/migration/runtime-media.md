# Runtime / Media 逐文件迁移账本

状态：Direction Update Accepted；详细Move账本仍为Discussion Draft，本文未移动生产代码。

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

## 1. 范围、计数与操作定义

主范围：

```text
internal/container/**
internal/workspace/**
internal/network/**
internal/display/**
internal/userruntime/**
internal/storage/**
internal/media/**
internal/boot/**
```

主范围共 **135 个现存项**：90 个 production Go、44 个 test Go、1 个 proto。本文末尾
用完整 manifest 与 `find` 结果做集合校验。

伴随审计但不计入 135 个 owned 项的 shared/consumer surface：

- `cmd/bridge/**`、`cmd/internal/core/{module,providers}.go`、`cmd/agent/**`；
- Runtime/Workspace/Media 相关 HTTP handlers 与 tests；
- Agent tools、ACP/MCP、Bot lifecycle、Backup 等 consumers；
- `internal/db/store`、PostgreSQL query/SQLC/migrations；
- `docker/Dockerfile.workspace`、workspace contract、display scripts、CI/dev assets。

| 操作 | 含义 |
| --- | --- |
| Move | owner 单一，contract 就绪后可保持行为机械移动 |
| Split | 文件/package 内有多个 owner 或 transaction，先拆代码/接口再移动 |
| Keep | 首轮保留位置/协议，仅更新 consumer；通常用于历史/全局基础设施 |
| Delete | 有效符号迁出或确认 dormant 后删除原 package，不建转发层 |
| Decide | 产品/部署或数据 owner 尚未冻结，禁止先搬 |

## 2. 目标目录与关键约束

目标根为 `domains/<owner>`（代码 owner/领域隔离，非进程/Go module/部署单元）。Bridge 公开包由
`cmd/bridge` 导入；Server/Channel composition root 仍为 `cmd/agent`/`cmd/channel`，禁止
`internal/composition` 与长期 `cmd/internal/process`。

```text
domains/runtime/
  runtime.go                 package runtime; stable values/errors only
  contract.go                package runtime; workspace image contract schema/version/paths
  bridge/
    client/                  package client; workspace data-plane gRPC client
    server/                  package server; cmd/bridge imports this
    pb/                      package pb; generated from bridge.proto
  internal/
    container/
      apple/
      containerd/
      docker/
    workspace/
      data/
      image/
      lifecycle/
      snapshot/
      target/
      postgres/
    network/
      overlay/
        netbird/
        tailscale/
    display/
    client/                  remote user Runtime registration/connection

domains/media/
  media.go                   package media; Asset/MediaType/consumer contract
  attachment/                package attachment; cross-domain pure values
  asset/                     package asset; real Service + public constructors
  storage/                   package storage; public Provider / ContainerFile ports
  internal/storage/
    container/
    fallback/
    local/
```

### 2.1 Bridge 是Workspace数据面，不是Server/Channel控制面RPC

当前 `cmd/bridge` 运行在 Workspace 内，提供文件、Exec、TCP tunnel、reverse HTTP 等
数据面能力。它不是本轮Server/Channel控制面RPC，也不授权创建`cmd/runtime`。两类职责必须
分开命名和contract：

```text
Server/Agent -> Runtime consumer port -> in-process Manager
Runtime Manager/Agent tool -> Bridge client -> cmd/bridge inside selected workspace
```

`cmd/bridge` 位于 `cmd/**`，Go 无法合法 import `domains/runtime/internal/**`。因此 Bridge
server 必须是公开包 `domains/runtime/bridge/server`；真正的执行 helper
可以留在 Runtime internal，但公开 server package只导出构造/注册入口。

### 2.2 Build profile

Runtime和Media在embedded/split两种Server profile中均为进程内owner。Channel不直接调用
Runtime File或Media owner RPC；它提交typed inbound/control，由Server内Agent/command本地调用。
本轮不建立Runtime/Media `local` wrapper、control-plane grpc或standalone binary。Bridge gRPC
始终存在，属于Workspace数据面，不由`split`tag选择。

## 3. Container 文件账本（14 production + 7 tests）

### 3.1 Root contract / host helpers

| 当前文件 | 当前职责 | 目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `internal/container/service.go` | Image/Container/Workload/Network/Snapshot 五组接口及请求 options；总 `Service` 聚合 | 跨边界request/value移`domains/runtime/runtime.go`；Image/Workload/Snapshot等最小接口由Agent/API/Workspace各consumer定义；仅backend内部接口放`runtime/internal/container/service.go` | `runtime` / `container` | Split/Delete |
| `internal/container/types.go` | error、runtime/container/task/image/snapshot/network/resource value | 跨 boundary value -> `domains/runtime/runtime.go`; backend-only mount/spec -> `internal/container/model.go` | `runtime` / `container` | Split |
| `internal/container/factory.go` | backend/socket/namespace constants | backend keys -> Runtime owner composition；containerd defaults -> containerd adapter | `runtime` / `containerd` | Split/Delete |
| `internal/container/host.go` | host resolv.conf fallback、timezone env | `domains/runtime/internal/container/host.go` | `container` | Move |
| `internal/container/host_test.go` | resolv/timezone tests | 同目标 package | `container` | Move |

根 `Service` 不是给所有 consumer 的永久巨型接口。Workspace 目前已经定义较窄的
`runtimeService`，迁移时继续由消费方定义 Image/Workload/Snapshot 等 port；不要让 Agent
或 API import backend aggregate。

### 3.2 Backend adapters

| 当前文件/文件族 | 当前职责 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `apple/service.go` | Socktainer/Apple Containerization backend 全实现 | `domains/runtime/internal/container/apple/service.go` | `apple` | Move |
| `docker/service.go` | Docker image/container/task/snapshot/network adapter | `domains/runtime/internal/container/docker/service.go` | `docker` | Move |
| `containerd/client.go` | containerd client dial | `domains/runtime/internal/container/containerd/client.go` | `containerd` | Move |
| `containerd/service.go` | containerd image/container/task/snapshot/network implementation、OCI spec/err mapping | 同目标 `service.go` | `containerd` | Move/Split |
| `containerd/network.go` | CNI setup/check/remove | 同目标 `network.go` | `containerd` | Move |
| `containerd/metrics.go` | cgroup v1/v2 sampling | 同目标 `metrics.go` | `containerd` | Move |
| `containerd/resolv.go`、`timezone.go` | host logic的旧重复实现 | 删除 duplicate，统一调用 `internal/container/host.go` | 无 | Delete |
| `containerd/aliases.go` | 对 root contract 的全部 type/error alias compatibility layer | backend改 import Runtime contract后删除 | 无 | Delete |
| `provider/provider.go` | 按backend switch创建adapter/cleanup | Runtime owner composition/backend.go | `runtime` | Move |

测试随实现移动：

```text
containerd/{metrics,resolv,service,timezone}_test.go
docker/service_test.go
provider/provider_test.go
```

`resolv_test.go` / `timezone_test.go` 与 root `host_test.go` 合并后删除重复。Backend tests
必须继续分别证明 unsupported snapshot/network/resource behavior，不能只测 constructor。

## 4. Workspace 文件账本（28 production + 19 tests + 1 proto）

当前根 `workspace` package 是最大重构点：17 个生产文件把 native lifecycle、remote
mount、Bot metadata、Settings、Network、snapshot transaction、archive IO、Bridge TLS
和 HTTP-facing DTO 混在一个 `Manager` 上。

### 4.1 Workspace 根 package

| 当前文件 | 职责一致的代码块 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `runtime.go` | `runtimeService` capability aggregate | `domains/runtime/internal/workspace/runtime.go`，继续由 consumer定义 | `workspace` | Move |
| `manager.go` | Manager state/constructor、bridge pool、target resolve、native spec、start/stop/delete、display/settings lookups | 拆到 `internal/workspace/{manager,target,spec}.go` 和 Runtime local facade | `workspace` / `local` | Split |
| `manager_lifecycle.go` | container resolve、network attach、ensure/start/stop/setup/cleanup/reconcile、DB status | `internal/workspace/lifecycle/service.go`; DB到 `workspace/postgres` | `lifecycle` / `postgres` | Split |
| `container_ref.go` | per-container lock/ref + DB record ensure | `internal/workspace/lifecycle/ref.go` | `lifecycle` | Move |
| `contract.go` | image contract schema/version/paths + payload validation；Bridge I/O executable check 仍留 workspace | schema/value -> `domains/runtime/contract.go`；`validateWorkspaceContract` 留 `internal/workspace/contract.go` | `runtime` / `workspace` | Split |
| `image_preference.go` | Bot metadata image/GPU/skill roots read/write + pure normalization | pure value -> `internal/workspace/image/preference.go`; persistence改成 API-owned `PreferenceReader/Writer` port | `image` | Split |
| `image_pull.go` | image prepare/pull mode/progress | `internal/workspace/image/pull.go` | `image` | Move |
| `template_bootstrap.go` | workspace template extraction/bootstrap/contract | `internal/workspace/image/bootstrap.go` | `image` | Move |
| `dataio.go` | export/import/preserve/archive fallback、snapshot mount、secret scrub、tar safety | `internal/workspace/data/service.go` + `archive.go` | `data` | Split |
| `versioning.go` | snapshot/version/list/rollback、runtime mutation、DB transaction/event | `internal/workspace/snapshot/service.go` + `postgres/store.go` | `snapshot` / `postgres` | Split |
| `resource_limits.go` | desired limits persistence、backend capability、observed metrics | `internal/workspace/resource.go`; Store到 postgres | `workspace` / `postgres` | Split |
| `metrics.go` | container CPU/memory/storage metrics | `internal/workspace/metric.go` | `workspace` | Move |
| `remote.go` | persisted remote mounts、live connection resolve、tool approval DTO/config | `internal/workspace/target/remote.go`; public command/value到 Runtime contract | `target` / `runtime` | Split |
| `target_context.go` | request-scoped target ID context value | consumer-owned context helper；Agent target selection建议 `domains/agent/internal/...` | consumer | Split/Delete |
| `identity.go` | Bot ID from container labels | `internal/workspace/lifecycle/identity.go` | `lifecycle` | Move |
| `hooks.go` | direct `hooks.Service` callback | `internal/workspace/lifecycle/hook.go`，换 `HookRunner` port | `lifecycle` | Move/Split |
| `bridge_tls.go` | config -> client/server TLS material、validation | `domains/runtime/bridge/client/tls.go` + `runtime/local/tls.go` | `client` / `local` | Split |

### 4.2 Manager 必须去除的反向依赖

| 当前依赖 | 当前位置 | 目标 port / owner |
| --- | --- | --- |
| `settings.GetSettingsByBotID`（display/tool approval） | `manager.go` | API `BotRuntimeSettings` reader；Runtime只读 projection |
| `bots.metadata.workspace`（image/GPU/skill roots） | `image_preference.go` | API-owned `WorkspacePreference` reader/writer |
| `hooks.Service` | `hooks.go` | Agent-owned `HookRunner` consumer port |
| broad `dbstore.Queries` + SQLC | manager/lifecycle/resource/version | Runtime-owned Store interfaces + postgres adapter |
| `pgxpool.Pool`/`WithTx` | `versioning.go` | `SnapshotStore.RecordVersion` 单一 transaction method |
| concrete `*bridge.Client` return | Manager/Remote target | consumer-owned `Workspace`/`Executor` interfaces；transport concrete留 adapter |

`versioning.go` 中 stop/commit/replace/start 使用 `context.WithoutCancel` 保证请求取消不会
留下 missing container；`recordSnapshotVersion` 同时写 snapshot、version、lifecycle event。
这些原子性/恢复语义必须整块保留在 Runtime owner，不能拆成 API/Agent 逐步 RPC。

### 4.3 Bridge client / wire / server

| 当前文件/资产 | 目标 | package | 操作 |
| --- | --- | --- | --- |
| `domains/runtime/bridge/client/client.go` | 已迁入；按 file/exec/tunnel/raw能力继续拆文件 | `client` | Split |
| `domains/runtime/bridge/client/errors.go` | 已迁入 | `client` | Move |
| `domains/runtime/bridge/client/reverse_http.go` | 已迁入 | `client` | Move |
| `domains/runtime/bridge/client/tls.go` | 已迁入 | `client` | Move |
| `domains/runtime/bridge/client/workspace_info.go` | 已整包迁入 client；stable backend/info value 后续可再拆 Runtime root | `client` | Move |
| `workspace/bridgepb/bridge.proto` | `domains/runtime/bridge/pb/bridge.proto`; update `go_package` | `pb` | Move |
| `bridgepb/bridge.pb.go`、`bridge_grpc.pb.go` | 从新 proto 路径重新生成，禁止手搬编辑 | `pb` | Move/Generate |
| `domains/runtime/bridge/server/server.go` | 已迁入；file/exec/raw handlers按职责拆 | `server` | Split |
| `domains/runtime/bridge/server/reverse_http.go` | 已迁入（保留文件名；账本曾记为 `http.go`） | `server` | Move |
| `domains/runtime/bridge/server/command_cancel_{unix,fallback}.go` | 已迁入，保留 build tags | `server` | Move |

当前 proto 的唯一 service `ContainerService` 实际包含 12 个 RPC：Read/Write/List/Stat/
Mkdir/Rename、bidi Exec、bidi Tunnel、bidi ReverseHTTP、stream ReadRaw/WriteRaw、Delete。
协议已经生产使用，目录迁移只更新 `go_package`；field number/name 不改，删除字段必须
reserve。`mise.toml` 的 `grpc-generate` 输入路径和 CODEOWNERS generated paths同步更新。

### 4.4 Workspace tests

测试按职责移而非统一堆到新 `workspace` root：

| 当前测试族 | 目标 |
| --- | --- |
| `bridge/{client,errors}_test.go` | 已迁入 `bridge/client`；client↔server 集成测在 `bridge/server`（`client_bridge_test.go`） |
| `bridge/server/{env,exec_stream,exit_code,path,server,client_bridge}_test.go` | 已迁入 `bridge/server` |
| `bridge_tls_test.go` | `bridge/client` + `runtime/local` TLS tests |
| `contract_test.go` | payload tests -> `domains/runtime/contract_test.go`；executable check 留 `internal/workspace` |
| `dataio_test.go` | `workspace/data`，保留 archive traversal/symlink/secret exclusion |
| `image_{preference,pull}_test.go`、`gpu_labels_test.go`、`template_bootstrap_test.go` | `workspace/image` |
| `identity_test.go`、`manager_legacy_test.go` | `workspace/lifecycle`；legacy behavior必须先判定支持期 |
| `remote_test.go`、`target_context_test.go` | `workspace/target` / consumer context |
| `resource_limits_test.go` | `workspace` resource + postgres contract |

## 5. Network 文件账本（27 production + 5 tests）

### 5.1 Core / runtime adapter

| 当前文件 | 当前职责 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `protocol.go` | Provider/Driver/Controller/BindingResolver/Runtime interfaces与status/action DTO | contract值 -> Runtime root；internal interfaces -> `internal/network` | `runtime` / `network` | Split |
| `provider.go` | provider descriptor/schema/config interfaces | `internal/network/provider.go` | `network` | Move |
| `registry.go` | provider registry/descriptors | `internal/network/registry.go` | `network` | Move |
| `controller.go` | attach/detach/status，组合 Runtime + BindingResolver + Provider | `internal/network/controller.go` | `network` | Move |
| `service.go` | config normalize、status/node/action/reconcile/overlay lifecycle；直接 broad Queries | `internal/network/service.go`; config读取改 API port | `network` | Split |
| `store.go` | `GetBotOverlayConfig` SQLC decode | API Bot Setting reader adapter；Runtime只定义 `ConfigReader` | `postgres`/API | Split/Delete |
| `runtime.go` | runtime descriptor/factory contract | `internal/network/runtime.go` | `network` | Move |
| `runtime_container.go` | Container backend -> network Runtime adapter | `internal/network/container.go` | `network` | Move |
| `workspace_probe.go` | container/task/workspace status + attachment request build | `internal/network/workspace.go` | `network` | Move/Split |
| `types.go` | HTTP-facing Bot status/node + internal attachment values混合 | internal values -> network；HTTP DTO -> API HTTP runtime | `network` / `runtime` | Split |
| `noop.go` | unsupported Runtime | `internal/network/noop.go` | `network` | Move |
| `strings.go` | one local helper | inline到 consumer后删除 | 无 | Delete |

当前通过 `Service.SetController` 人为打破 `Service <-> Controller` construction cycle。
目标应拆 `ConfigReader/Reconciler` 与 `BindingResolver`，由 `local` composition 构造，删除
setter cycle；不能把 FX 放进 network package。

### 5.2 Overlay providers

| 当前文件族 | 精确目标 | package | 操作 |
| --- | --- | --- | --- |
| `overlay/builtin.go`、`deps.go` | `domains/runtime/internal/network/overlay/{builtin,deps}.go` | `overlay` | Move |
| `overlay/internal/configutil/config.go` | `overlay/config/config.go`，leaf `config` | `config` | Move |
| `overlay/internal/sidecar/{manager,runtime,spec}.go` | `overlay/sidecar/*` | `sidecar` | Move |
| `overlay/netbird/{native,provider,schema}.go` | `overlay/netbird/*` | `netbird` | Move |
| `overlay/tailscale/{localapi,native,nodes,provider,schema,status}.go` | `overlay/tailscale/*` | `tailscale` | Move |

Network state目录 `${data_root}/network/<bot>/<provider>`、sidecar lifecycle、CNI attach和
logout wipe由 Runtime owner。Bot overlay enabled/provider/config 当前存 `bots` columns，写
owner仍是 API Bot Setting；Runtime通过 config port reconcile，禁止 Runtime直接更新 Bot。

测试映射：root `controller/registry/runtime_container/service_test.go` 随 core；
`overlay/tailscale/provider_test.go` 随 provider。需要新增 `SetController` cycle消除后的
construction test、reconcile idempotency和sidecar cleanup test。

## 6. Display 文件账本（3 production + 1 test）

| 当前文件 | 当前职责 | 目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `display/service.go` | 1647行 WebRTC/GStreamer/RFB status/session/encoder/screenshot/control | `domains/runtime/internal/display/{service,webrtc,encoder,rfb,screenshot,control}.go` | `display` | Split |
| `display/service_windows.go` | Windows platform dial/behavior | 同目标 `platform_windows.go`，保留 build tag | `display` | Move |
| `display/service_other.go` | non-Windows platform behavior | 同目标 `platform_other.go`，保留 build tag | `display` | Move |
| `display/service_test.go` | SDP/codec/RFB/session/screenshot behavior | 按拆分文件分测 | `display` | Split |

Display service当前消费 Workspace的 `BotDisplayEnabled`、socket path和optional tunnel
dialer。目标定义 `WorkspaceDisplay` consumer port；API HTTP和Agent browser tool都不能
直接 import Runtime internal display types，应通过 Runtime facade或各自port。进程内
WebRTC session不能透明远程化；若 Runtime control plane拆进程，offer/control/session
RPC和UDP/NAT拓扑必须另立spec，首轮保持 local。

## 7. Remote User Runtime 文件账本（7 production + 5 tests）

当前 `userruntime` 表示用户机器主动连接 Server 的 reverse Runtime，不等于 native
container backend，也不等于 `cmd/bridge`。

| 当前文件 | 当前职责 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `types.go` | Runtime credential/API DTO、HandshakeInfo | `domains/runtime/runtime.go` public values + API HTTP DTO | `runtime` | Split |
| `key.go` | token generation/format | `internal/client/key.go` | `client` | Move |
| `metadata.go` | handshake metadata decode/strict validation | `internal/client/handshake.go` | `client` | Move |
| `hub.go` | live connection registry、kick/unregister/shutdown | `internal/client/hub.go` | `client` | Move |
| `lifecycle.go` | per-runtime ref-counted activation lock | `internal/client/lock.go` | `client` | Move |
| `pipe.go` | raw net.Conn -> gRPC client conn、single-use dial readiness | `internal/client/pipe.go` | `client` | Move |
| `service.go` | credential CRUD/auth、single active activation/deactivation/connection lookup | `internal/client/service.go`; Store到 Runtime postgres | `client` / `postgres` | Split |

对应 tests全部随 leaf移动：`hub/key(lifecycle)/metadata/pipe/service`，现有无 `key_test.go`
则新增 format/entropy test。必须保持：

- activation lock只允许一个提交者替换 connection；
- guard失败不注册 connection；revoke会kick live connection；
- shutdown关闭hub；direct pipe context cancel不泄漏 goroutine；
- `APIToken` 当前按产品语义 owner-readable，DB为明文；若改hash属于单独安全migration，
  不能混入目录移动。

DB owner为 Runtime：`user_runtimes`、`bot_remote_runtime_bindings`。其中 mount必须校验
Runtime user与Bot owner同一 membership，revoked/owner mismatch/client version状态继续
fail closed。

## 8. Storage / Media / Attachment 文件账本

### 8.1 Storage（4 production + 2 tests）

| 当前文件 | 目标 | package | 操作 |
| --- | --- | --- | --- |
| `storage/storage.go` | Provider/optional capability接口移 `domains/media/storage/`（公开 ports）；其他 domain只见 Media port | `storage` | Move |
| `providers/localfs/provider.go` | `domains/media/internal/storage/local/provider.go` | `local` | Move |
| `providers/containerfs/provider.go` | `domains/media/internal/storage/container/provider.go`; Bridge concrete换 Runtime file port | `container` | Split |
| `providers/fallback/provider.go` | `domains/media/internal/storage/fallback/provider.go` | `fallback` | Move |
| `containerfs/provider_test.go`、`fallback/provider_test.go` | 随实现；新增partial-copy/cancel/cleanup测试 | 对应 leaf | Move |

Fallback `EnsureAccessPath` 可能从 primary读取并复制到 secondary，这不是纯 path helper；
必须保留流关闭、partial write和重试语义。Container storage key包含 bot routing，Media
必须在 service层先校验 bot ownership，不能让任意 caller直接使用 Provider key。

### 8.2 Media（4 production + 2 tests）

| 当前文件 | 当前职责 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `media/types.go` | `MediaType`、`Asset`、`IngestInput` | `domains/media/media.go` | `media` | Move |
| `media/errors.go` | not found/invalid/too large | `domains/media/media.go` | `media` | Move |
| `media/limits.go` | bounded reader | `domains/media/limits.go` | `media` | Move |
| `media/service.go` | spool/hash/dedupe key、put/open/stat/access、container ingest、MIME mapping | `domains/media/asset/service.go`; cmd 经 `NewLocalService` / `NewContainerFallbackService` / `NewService` 装配 | `asset` | Move |
| `limits_test.go`、`service_access_test.go` | bounded reader、access fallback tests | `domains/media/limits_test.go`、`domains/media/asset/service_access_test.go`；保持traversal/bot-prefix/content-hash语义 | `media` / `asset` | Move |

当前 `media.Service` **不访问 PostgreSQL**；source of truth是 provider中的
content-addressed key，`Asset` 从 key/MIME派生。`storage_providers`、
`bot_storage_bindings`、`media_assets` 的 generated queries在生产 Go中没有 caller。
团队必须 Decide：

1. 正式接入 Media-owned catalog/store并迁移现有对象；或
2. 宣布这些表dormant，Epoch v1历史migrations保持，停止暴露未使用queries；或
3. 继续纯 object-key模型并明确不承诺 DB catalog。

不得在目录移动中假装这些表已被Media service使用。

`bot_history_message_assets` 是 Agent Message read model，`CreateMessageAsset` 同时 lock
message/session、更新 compaction epoch并 upsert link，transaction owner必须留 Agent。
Media拥有 blob/content hash；Agent拥有 message-asset link。不要把该 SQL transaction
搬进 Media。

### 8.3 Attachment（2 production + 2 tests）

| 当前文件 | 当前职责 | 精确目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `attachment/bundle.go` | cross Channel/Agent/HTTP Bundle map wire、tool input parse | `domains/media/attachment/bundle.go` | `attachment` | Move |
| `attachment/normalize.go` | MIME/data URL/base64 bounded decode | `domains/media/attachment/normalize.go` | `attachment` | Move |
| `bundle_test.go`、`normalize_test.go` | wire roundtrip/MIME/base64 limits | 同目标 | `attachment` | Move |

Attachment是跨 domain稳定value vocabulary，不能放 `domains/media/internal`，否则
Channel/Agent无法合法import。`BundleFromMap/ToMap/MergeIntoMap` wire兼容字段必须在迁移
前冻结；canonical data mount 归 `domains/runtime`；media AccessPath /
storage-key 反解归 `domains/media`（显式接收 dataMount）；不进入 attachment 公共 API。

## 9. Boot 文件账本（1 production + 1 test）

| 当前文件 | 当前职责 | 目标 | package | 操作 |
| --- | --- | --- | --- | --- |
| `boot/runtime.go` | JWT/server addr、container backend/socket、timezone混合`RuntimeConfig` | auth/server fields归API composition；backend/timezone归Runtime owner config；最终删除`boot` | `runtime` + API | Split/Delete |
| `boot/runtime_test.go` | JWT/backend/timezone/env override混测 | 随consumer config tests拆分 | 对应 owner | Split |

`RuntimeConfig` 名称具有误导性：它同时承载 API JWT和HTTP address。Runtime domain不应
import认证secret。`internal/config` 仍保存operator TOML vocabulary，`cmd/**` 将窄 config
view传给各 domain；环境变量 override只解析一次，配置不匹配fail fast。

## 10. HTTP Handler 映射

Runtime/Media相关 Handler 不能继续共用一个 `ContainerdHandler` concrete type。目标
HTTP package只依赖 Runtime/Media ports，RPC server也依赖同一 application port。

| 当前 Handler 文件/族 | 精确目标 | 操作/注意 |
| --- | --- | --- |
| `containerd.go` | `domains/api/http/runtime/{container,metric,snapshot,resource}.go` | Split；skills/MCP routes移Agent HTTP；Bot access留API middleware |
| `containerd_terminal.go` | `http/runtime/terminal.go` | Bridge stream通过 Runtime Exec port；保留WS cancel/resize/exit drain |
| `containerd_browser.go` | `http/runtime/browser.go` | browser session proxy/WS tunnel通过 Runtime port |
| `display.go` | `http/runtime/display.go` | HTTP DTO/error/SSE留API；Display session实现留Runtime |
| `filemanager.go` | `http/runtime/file.go` | archive/extract/path revision只做transport；实际IO通过 Runtime File port |
| `workspace.go` | `http/runtime/workspace.go` | workspace target/context adapter |
| `user_runtime.go` | `http/runtime/client.go` | credential CRUD；API token presenter审计 |
| `runtime_connect.go` | `domains/api/http/runtime/connect.go` + Runtime activation port | 当前 public `/runtimes/connect` upgrade承载reverse gRPC连接；不是未来Runtime control gRPC server，保持握手认证顺序 |
| `bot_remote_runtime.go` | `http/runtime/target.go` | mount/primary/tool approval；native setting update通过API coordinator |
| `public_media.go` | Decide AD1 | endpoint进程owner与Media byte delivery尚未批准；不得预设Channel-owned endpoint或Open/Preview跨进程port |

对应 test文件族全部随目标移动：browser/terminal/display/filemanager/public_media/
runtime_connect/bot_remote_runtime。`containerd_snapshot_lineage_test.go` 应下沉 Runtime
snapshot application test而非留 HTTP。

OpenAPI/SDK需保持所有现有 Runtime routes/status/stream headers。Handler package 改名会
改变 Swag schema名，必须运行 `mise run swagger-generate`、`mise run sdk-generate` 并
单独评审 generated diff。

## 11. `cmd/bridge`、composition 与 Agent consumers

### 11.1 `cmd/bridge`

| 当前文件 | 目标/处理 |
| --- | --- |
| `main.go` | 保持 composition root；改 import `runtime/bridge/server` + `pb`，不搬业务实现进cmd |
| `tls.go` | server credential composition可移 `bridge/server/tls.go`，env读取留cmd/local config |
| `display.go` | Workspace内Xvnc/XFCE/Chromium supervisor属于Bridge host实现，移 `bridge/server/display.go` 或保持cmd helper待process supervisor contract冻结 |
| `tools_proxy.go` | reverse HTTP ACP tools proxy随Bridge server HTTP broker，loopback policy保留 |
| 三组tests | 随职责移动，TCP listen/mTLS身份校验必须保留 |

`cmd/bridge/main.go` 的PID1 reaper、UDS/TCP bind policy、strict mTLS、gRPC size/keepalive、
GracefulStop都属于可执行装配契约，不能在普通目录move中静默变化。

### 11.2 `cmd/internal/core` wiring

当前 providers拆分目标：

```text
provideContainerService        -> domains/runtime public constructor
provideNetworkController       -> runtime owner wiring
provideOverlayProviderRegistry -> runtime owner wiring
provideNetworkService          -> runtime owner wiring + API ConfigReader
provideUserRuntimeStore        -> runtime/internal/postgres
provideBotRemoteRuntimeBindingStore -> runtime/internal/postgres
provideUserRuntimeHub/Pipe     -> runtime owner lifecycle
provideWorkspaceManager        -> runtime owner constructor
provideBridgeProvider          -> runtime adapter exposed as consumer ports
provideMediaService            -> domains/media/asset.NewContainerFallbackService
startContainerReconciliation   -> runtime owner lifecycle hook
```

`core.ServerModule`最终只组合Runtime/Media公开constructors，不逐个construct internal types。
两种Server profile装配完全相同；build tag不得替换为RPC clients。

### 11.3 Agent tools consumers

以下 consumers当前直接持有 `*bridge.Client`、`bridge.Provider`、`*media.Service` 或
attachment concrete types：

```text
agent/tools/container.go
agent/tools/browser.go
agent/tools/computer_a11y.go
agent/tools/apply_patch.go
agent/tools/read_media.go
agent/tools/attachment_bundle.go
agent/tools/image_gen.go
agent/tools/video_gen.go
agent/tools/transcribe.go
```

每个 consumer定义最小接口：File/Exec/Tunnel/WorkspaceInfo/MediaOpen/MediaIngest；local
adapter包住Bridge/Media，future RPC client实现相同接口。`browser.go` 约2.4k行还混合CDP、
display input、screenshot和workspace save，应另拆 Agent tool实现，但不归Runtime owner。

## 12. Persistence owner、query 与 transaction

### 12.1 Runtime DB owner

| 表/query family | Owner | 事务/语义 |
| --- | --- | --- |
| `containers.sql` | Runtime Workspace | runtime record状态必须与backend mutation补偿一致 |
| `snapshots.sql`、`versions.sql`、lifecycle events | Runtime Snapshot | recordSnapshotVersion单transaction；runtime mutation带per-container lock |
| `workspace_resource_limits.sql` | Runtime Workspace | desired配置与applied/observed区分 |
| `user_runtimes.sql` 前半 | Runtime Client | credential create/auth/list/revoke |
| `user_runtimes.sql` mount部分 | Runtime Target | Bot owner/runtime owner/revoked/primary约束 |
| `bots` workspace metadata/settings reads | API owner | Runtime通过Preference/Setting reader，不直写Bot row |
| `network.sql:GetBotOverlayConfig` | API Bot Setting | Runtime读projection并reconcile；不拥有写 |

当前 `WorkspaceStore` 14方法需要至少拆为 `ContainerStore`、`SnapshotStore`、
`ResourceStore`、`LifecycleWriter`；Remote client再拆 `RuntimeStore`、`MountStore`。SQLC row
不得出 Runtime application package，adapter目标：

```text
domains/runtime/internal/workspace/postgres/*.go
domains/runtime/internal/client/postgres/*.go
```

### 12.2 Media DB owner

- Blob/content hash/storage key：Media owner；
- `bot_history_message_assets` link与compaction invalidation：Agent Message owner；
- dormant `storage_providers/bot_storage_bindings/media_assets`：Decide后才接入，不能由 broad
  Queries继续假装contract；
- Epoch v1 migrations历史immutable；cutover只归档到`legacy/v1/migrations`，不改basename、
  编号、内容或checksum；Epoch v2分别进入Runtime/Media owner stream并使用各自schema内
  Goose version table，由统一Migrator执行。

Generated SQLC可能受影响的文件：`containers.sql.go`、`snapshots.sql.go`、
`versions.sql.go`、`workspace_resource_limits.sql.go`、`user_runtimes.sql.go`、
`network.sql.go`、`media.sql.go`。只从SQL源重新生成，不手改。

Runtime/Media-owned handwritten SQL分别迁到`db/postgres/runtime/queries`和
`db/postgres/media/queries`，各自使用owner-local `sqlc.yaml`；generated Go output分别进入
`domains/runtime/internal/postgres/sqlc`和`domains/media/internal/postgres/sqlc`。Workspace、
Client、Asset等Store interface仍在消费use case定义，不能集中到Runtime/Media共享Store。

## 13. Workspace image、模板、协议与发布资产

以下不是主范围owned文件，但路径/contract变化必须同步审计：

| 资产 | 影响 |
| --- | --- |
| `docker/Dockerfile.workspace` | 构建/安装 Bridge、toolkit、display assets；目标路径不因Go目录改变而变 |
| `docker/workspace-contract.json` | 必须与 `domains/runtime.CurrentWorkspaceContractVersion`、path/executable list一致 |
| `scripts/display-prepare.sh`、`display-apply-style.sh`、`desktop-style.sh` | Display supervisor/runtime contract |
| `docker/toolkit/bin/*` | required executable contract；wrapper与glibc/arch约束 |
| `.github/workflows/docker.yml` | workspace change detection、build、contract smoke test |
| `devenv/Dockerfile.server`、`server-entrypoint.sh`、compose/Kata files | containerd/CNI/Bridge build与mount |
| `conf/app.example.toml`、`app.apple.toml` | backend/workspace/Bridge TLS config compatibility |
| `mise.toml` | bridge.proto generation path |
| `.github/CODEOWNERS` | Runtime/generated ownership path |

Workspace contract升级必须是独立兼容变更：先扩展reader，再发布镜像，再切required
version；不能在Go package move时顺手bump version。

## 14. 测试矩阵与迁移顺序

### 14.1 Characterization gates

1. 每个Container backend的create/start/stop/delete/snapshot/network/resource/error parity。
2. Workspace setup/cleanup/reconcile、per-container lock、request cancel后的detached mutation、
   preserved data与archive traversal安全。
3. Snapshot/version DB transaction、runtime replacement失败恢复、lineage/list/rollback。
4. Bridge unary/stream/PTY/cancel/tunnel/reverse HTTP/raw IO、mTLS identity和shutdown。
5. Network config normalize、provider action、CNI/sidecar idempotency/logout wipe。
6. Remote Runtime credential、handshake、single connection activation、mount owner/revoke状态。
7. Display WebRTC/RFB/encoder/session cleanup、browser tunnel。
8. Media hash/limit/MIME/bot隔离/fallback copy、attachment wire roundtrip、public signed media。

Focused baseline command：

```bash
go test ./internal/container/... ./internal/workspace/... ./internal/network/...
go test ./internal/display ./internal/userruntime
go test ./domains/media/... ./internal/boot
go test ./cmd/bridge ./internal/handlers ./domains/agent/tool
```

迁移后：

```bash
go test ./domains/runtime/... ./domains/media/... ./cmd/bridge
go test ./internal/arch
go list ./...
go test ./...
mise run swagger-generate
mise run sdk-generate
```

本次只读审计已在基线 checkout 实际运行第一组 focused command，所有有测试的 package
通过；Apple、Bridge PB、Overlay/NetBird、Storage root/LocalFS 当前显示
`[no test files]`。未运行 Swagger/SDK/proto生成命令，因为本次未改 annotation/proto，
调查阶段不应产生 generated churn。

### 14.2 推荐提交顺序

1. 冻结Runtime root values/consumer ports、Bridge protocol位置和Media `AssetRef`/legacy value；
   delivery wire等待AD1，不在本步冻结。
2. 先拆Workspace Store/Snapshot/version/lifecycle transaction和API Preference/Setting ports；
   在旧SQLC上实现Runtime adapter，删除对应broad Queries methods。
3. 拆Media catalog/storage/AssetLink consumer边界；单独决策dormant schema，不移动实现。
4. 在旧namespace内拆Manager为lifecycle/image/data/snapshot/target，消除SetController cycle。
5. 建立Runtime/Bridge/Media公开contract与consumer ports；先切HTTP与Agent tool consumers，
   Handler文件仍留旧路径。Channel输入通过final inbound/Turn进入Server，不创建Runtime File
   RPC、空transport或standalone profile。
6. 依赖方向闭合后机械迁Container backends、Bridge client/server、Network/Display/Remote
   Client、Media/Storage/Attachment与对应Handler；Move不更新generated。
7. 统一Goose Migrator将Runtime/Media迁入各自schema/version table；fresh baseline与v1 bridge
   通过后cutover。
8. 单独建立owner SQLC target并重生成；SQLC diff不混入Docker/proto/CI路径变化。
9. 单独更新Docker/workspace/proto/CI路径与OpenAPI/SDK generated，并跑镜像contract smoke。
10. imports/architecture/coverage为零后删除旧generated、旧packages和facade。

Contract、behavioral split、mechanical move、generated artifacts和schema migration必须是
不同提交，保证每步可review、可回滚。

## 15. 主范围机械覆盖校验

### 15.1 完整 manifest（139项）

```text
internal/boot/runtime.go
internal/boot/runtime_test.go
internal/container/apple/service.go
internal/container/containerd/aliases.go
internal/container/containerd/client.go
internal/container/containerd/metrics.go
internal/container/containerd/metrics_test.go
internal/container/containerd/network.go
internal/container/containerd/resolv.go
internal/container/containerd/resolv_test.go
internal/container/containerd/service.go
internal/container/containerd/service_test.go
internal/container/containerd/timezone.go
internal/container/containerd/timezone_test.go
internal/container/docker/service.go
internal/container/docker/service_test.go
internal/container/factory.go
internal/container/host.go
internal/container/host_test.go
internal/container/provider/provider.go
internal/container/provider/provider_test.go
internal/container/service.go
internal/container/types.go
internal/display/service.go
internal/display/service_other.go
internal/display/service_test.go
internal/display/service_windows.go
domains/media/media.go
domains/media/limits.go
domains/media/limits_test.go
domains/media/access_path.go
domains/media/access_path_test.go
domains/media/asset/service.go
domains/media/asset/service_access_test.go
domains/media/asset/constructors.go
domains/media/attachment/bundle.go
domains/media/attachment/normalize.go
domains/media/storage/storage.go
domains/media/storage/container_file.go
domains/media/internal/storage/container/provider.go
domains/media/internal/storage/container/provider_test.go
domains/media/internal/storage/fallback/provider.go
domains/media/internal/storage/fallback/provider_test.go
domains/media/internal/storage/local/provider.go
internal/network/controller.go
internal/network/controller_test.go
internal/network/noop.go
internal/network/overlay/builtin.go
internal/network/overlay/deps.go
internal/network/overlay/internal/configutil/config.go
internal/network/overlay/internal/sidecar/manager.go
internal/network/overlay/internal/sidecar/runtime.go
internal/network/overlay/internal/sidecar/spec.go
internal/network/overlay/netbird/native.go
internal/network/overlay/netbird/provider.go
internal/network/overlay/netbird/schema.go
internal/network/overlay/tailscale/localapi.go
internal/network/overlay/tailscale/native.go
internal/network/overlay/tailscale/nodes.go
internal/network/overlay/tailscale/provider.go
internal/network/overlay/tailscale/provider_test.go
internal/network/overlay/tailscale/schema.go
internal/network/overlay/tailscale/status.go
internal/network/protocol.go
internal/network/provider.go
internal/network/registry.go
internal/network/registry_test.go
internal/network/runtime.go
internal/network/runtime_container.go
internal/network/runtime_container_test.go
internal/network/service.go
internal/network/service_test.go
internal/network/store.go
internal/network/strings.go
internal/network/types.go
internal/network/workspace_probe.go
internal/userruntime/hub.go
internal/userruntime/hub_test.go
internal/userruntime/key.go
internal/userruntime/lifecycle.go
internal/userruntime/lifecycle_test.go
internal/userruntime/metadata.go
internal/userruntime/metadata_test.go
internal/userruntime/pipe.go
internal/userruntime/pipe_test.go
internal/userruntime/service.go
internal/userruntime/service_test.go
internal/userruntime/types.go
domains/runtime/bridge/client/client.go
domains/runtime/bridge/client/client_test.go
domains/runtime/bridge/client/errors.go
domains/runtime/bridge/client/errors_test.go
domains/runtime/bridge/client/reverse_http.go
domains/runtime/bridge/client/tls.go
domains/runtime/bridge/client/workspace_info.go
internal/workspace/bridge_tls.go
internal/workspace/bridge_tls_test.go
domains/runtime/bridge/pb/bridge.pb.go
domains/runtime/bridge/pb/bridge.proto
domains/runtime/bridge/pb/bridge_grpc.pb.go
domains/runtime/bridge/server/client_bridge_test.go
domains/runtime/bridge/server/command_cancel_fallback.go
domains/runtime/bridge/server/command_cancel_unix.go
domains/runtime/bridge/server/env_test.go
domains/runtime/bridge/server/exec_stream_test.go
domains/runtime/bridge/server/exit_code_test.go
domains/runtime/bridge/server/path_test.go
domains/runtime/bridge/server/reverse_http.go
domains/runtime/bridge/server/server.go
domains/runtime/bridge/server/server_test.go
domains/runtime/contract.go
domains/runtime/contract_test.go
domains/runtime/runtime.go
domains/runtime/runtime_test.go
internal/workspace/container_ref.go
internal/workspace/contract.go
internal/workspace/contract_test.go
internal/workspace/dataio.go
internal/workspace/dataio_test.go
internal/workspace/gpu_labels_test.go
internal/workspace/hooks.go
internal/workspace/identity.go
internal/workspace/identity_test.go
internal/workspace/image_preference.go
internal/workspace/image_preference_test.go
internal/workspace/image_pull.go
internal/workspace/image_pull_test.go
internal/workspace/manager.go
internal/workspace/manager_legacy_test.go
internal/workspace/manager_lifecycle.go
internal/workspace/metrics.go
internal/workspace/remote.go
internal/workspace/remote_test.go
internal/workspace/resource_limits.go
internal/workspace/resource_limits_test.go
internal/workspace/runtime.go
internal/workspace/target_context.go
internal/workspace/target_context_test.go
internal/workspace/template_bootstrap.go
internal/workspace/template_bootstrap_test.go
internal/workspace/versioning.go
```

最终用 `find` 生成source集合、从本节提取documented集合，`comm -23`（未覆盖）和
`comm -13`（陈旧项）都必须为0。

本快照集合结果：

```text
source=135
documented=135
未覆盖=0
陈旧项=0
```
