# Composition / Build Profile / Release 迁移账本

状态：Direction Update Accepted；详细release映射仍为Discussion Draft

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

本账本是 composition/release surface 的唯一 primary ledger。Channel、Agent、API、Memory、
Runtime 等模块账本仍拥有业务实现；本账本只拥有进程入口、装配函数、build profile、构建、
镜像、开发拓扑和发布动作。对其他账本中的 domain symbol 只记录“由哪个 module 提供”，
不重复认领实现。

## 1. 结论与阻断项

当前目录已经落地编译期 profile 与对应构建资产，但完整隔离仍取决于 composition dependency
closure：

1. `cmd/agent/profile_embedded.go` 与 `profile_split.go` 已通过唯一 `split` tag选择拓扑，
   `internal_rpc.shared_secret` 不再决定 profile。
2. `docker/Dockerfile.server` 内的 `/app/memoh-server` 已使用 `-tags split`；Compose继续用
   原路径和镜像名，secret只认证Server/Channel RPC。
3. 双进程 devenv 使用的 `.air.toml` 已显式以 `-tags split` 热重载Server。
4. `scripts/release.sh` 保留embedded `memoh-server`兼容文件名，新增
   `memoh-server-split`和`memoh-channel`，并生成profile manifest与checksum。
5. `.github/workflows/release.yml` 当前只上传 source bundle、npm 和 Desktop artifacts，
   **没有调用** `scripts/release.sh`，因此脚本能构建 archive 不等于 GitHub Release 会发布它。
6. Go CI 已增加default/split Server与Channel的独立test/build gate；Docker CI仍只证明镜像
   能构建，没有直接证明 split Server 的依赖闭包已排除 child implementation。
7. 当前 `ValidateServerRuntime` / `ValidateChannelRuntime` 共用
   `InternalRPCConfig.Validate`，两端都被要求同时配置 `server_target` 和 `channel_target`；
   build profile 落地后应按进程只校验自己真正消费的 target，并在 profile/config 不匹配时
   fail fast。

因此，build tag 方案合理，但只有 Go 文件、Docker、dev、release、CI 和配置语义同时迁移
才算完成。只新增两个 tagged 文件会产生名称正确、编译内容错误的发布物。

## 2. 目标产物矩阵

全局只使用一个 topology tag：`split`。它只允许出现在
`cmd/agent/profile_embedded.go` 与 `cmd/agent/profile_split.go`；OS、cgo、integration 等
既有 build constraint 不受此规则影响。

| 产物/场景 | 构建命令 | 编译内容 | 不得编译 | 配置要求 |
| --- | --- | --- | --- | --- |
| Bare-metal embedded Server | `go build -o memoh-server ./cmd/agent` | API、Agent、Memory、Model、Runtime、Media + Channel concrete | split-only RPC client/server | 建议shared secret为空且不要求RPC target；non-empty时hard error或过渡warning待批准 |
| Split Server | `go build -tags split -o memoh-server ./cmd/agent` | API、Agent、Memory、Model、Runtime、Media + `internal/rpc/channel/client/server`所需Server host | `domains/channel/internal`和外部平台SDK | secret、Server RPC listen、Channel target必填 |
| Channel child | `go build -o memoh-channel ./cmd/channel` | Channel concrete + `internal/rpc/channel/client/server` | Server owner concrete/internal | secret、Channel RPC listen、Server target必填 |
| Migration command | 使用发行介质中的统一Migrator执行`migrate` | Goose owner Providers + v1 legacy upgrader | 不应启动任何service module | 获取全局advisory lock；只校验DB/migration config，不校验RPC live target |
| Docker Server container | 镜像内 `/app/memoh-server` 必须为 `-tags split` | Split Server | Channel implementation | Compose 必须注入同一 secret/targets |
| Docker Channel container | 镜像内 `/app/memoh-channel` | Channel child | Server implementation | 同上 |

当前 `memohai/server` 镜像可在首轮继续同时携带两个**独立二进制**，以保持 Compose、镜像名
和安装脚本兼容；“Split Server 不编译 child”约束针对 `/app/memoh-server` 的 Go dependency
closure，不要求首轮就拆成两个 OCI image。独立 `memohai/channel` image 是后续发布决策，
不能阻塞代码隔离。

### 2.1 Bare-metal archive 兼容建议（待批准）

推荐一个过渡版本内同时提供：

```text
memoh-server          # 默认 embedded，保持现有裸机文件名和启动方式
memoh-server-split    # -tags split；显式供裸机双进程部署
memoh-channel         # child binary
```

当前 archive 只有前后两个文件，但 `memoh-server` 通过 secret 运行时切换。直接把它改成 split
会破坏 all-in-one；只保留 embedded 又会破坏现有裸机双进程。因此新增明确的 split 文件名
是兼容迁移，待一个 deprecation window 后再决定是否拆 archive 或重命名。

## 3. 目标 composition 目录

```text
cmd/
  agent/                     # Server composition root（最终保留）
    main.go                  # 命令分派；无业务实现
    common.go                # load config + Server-owned host composition
    commands.go              # migrate/account/version command adapter
    http.go                  # 仅跨 domain HTTP registrar 组合
    profile_embedded.go      # //go:build !split
    profile_split.go         # //go:build split
  channel/                   # Channel composition root（最终保留）
    main.go                  # Channel child composition root

domains/
  api/                       # Server-local business owner
  agent/
  channel/                   # only detached business owner in initial topology
  memory/
  model/
  runtime/
  media/

internal/
  config/                    # typed config；不再决定 topology
  database/                  # pool + migration runner only
  logger/
  rpc/channel/
    client/                  # only process-boundary transport
    server/
    pb/
```

`domains/` 表示代码 owner/领域隔离，不是 Go module、进程或部署单元。Server 与 Channel 的
最终 composition root 分别保留在 `cmd/agent` 与 `cmd/channel`；禁止建立 `internal/composition`
或长期 `cmd/internal/process`，也不新建共享 wiring 巨包。`cmd` 只组合各 domain 公开
contract/constructor，不承载业务 policy。

`cmd/internal/core` 和 `cmd/internal/channel` 是迁移期混合 composition package，目标状态均
删除。不能让 `domains/channel` 包装 `cmd/internal/channel`：Go 的 `internal` 可见性
不允许 `domains/**` 导入 `cmd/internal/**`。必须把 provider symbol 先 Move/Split 出去，再让
两个 command root 导入新 package。

## 4. `cmd/agent` 逐文件映射（8/8 Go + 1 mise）

| 当前文件（package `main`） | 当前代码 | 目标位置/动作 | 前置条件 |
| --- | --- | --- | --- |
| `cmd/agent/main.go` | `main`、`runAccount`、`runMigrate`；默认 `serve` | `main.go` Keep；命令 adapter 可拆 `commands.go`，仍在 `cmd/agent` | 维持 CLI、exit code、usage 和 `memoh-server migrate` 兼容 |
| `cmd/agent/module.go` | `runServe`、`optionsFor`、`splitOptions`、`embeddedOptions`、`commonOptions` | Split/Delete：`runServe/commonOptions` -> `common.go`；两套 options -> tagged profiles；删除 `optionsFor` 和运行时 topology branch | Channel Store/transaction、API/Agent ports、typed RPC closure和dependency guard全部ready |
| `cmd/agent/http_providers.go` | 13 个 provider/start symbol；直接聚合 API/Agent/Channel/Runtime/Model concrete types | Split：constructors下沉各owner；`cmd/agent/http.go`只组合registrar与consumer ports | 完整HTTP文件owner由`api-http.md`冻结；本账本不移动handler实现 |
| `cmd/agent/rpc.go` | Channel conn/client、local-first Channel runtime、Webhook status、Agent/server runtime gRPC host | Split/Delete：真实边界全部归`internal/rpc/channel`；删除local-first shim，不建立owner grpc | Web/CLI已归API/Chat；final typed RPC contract获批 |
| `cmd/agent/support.go` | `provideConfig`、migration FS/command、account recovery、version | `common.go` + `commands.go` Keep/Split；migration implementation调用`internal/database`统一Goose Migrator和v1 legacy upgrader，account use case调用API owner | `serve`才做profile validation；`migrate`不要求RPC或service graph |
| `cmd/agent/module_test.go` | 用 config secret 驱动两套 `fx.ValidateApp` | Split/Rewrite：同名测试分别在 `!split`、`split` graph编译；不再构造两种 runtime config分支 | CI必须分别运行两次 tag matrix |
| `cmd/agent/rpc_runtime_test.go` | 固定 Web local、外部 Channel remote 的 `localFirstChannelRuntime` | Delete/Replace：Web/CLI API/Chat routing测试 + Channel gRPC parity测试 | `localFirstChannelRuntime` 删除后不能留下行为缺口 |
| `cmd/agent/support_test.go` | account recovery输入先校验、后开 DB | Keep/Move 到 `commands_test.go`；语义不变 | 无 |
| `cmd/agent/mise.toml` | `start = go run cmd/agent` | Keep default embedded；修正为 `go run ./cmd/agent`，新增 `start:split = go run -tags split ./cmd/agent` | split task需明确 secret/targets来源，禁止用 secret自动选 profile |

### 4.1 `http_providers.go` symbol 级拆分

| 当前 symbol | 目标 owner / 目标 wiring |
| --- | --- |
| `provideServerHandler`、`serverParams`、`provideServer`、`startServer` | `domains/api/http/server` 提供 server module；跨 domain health/start hook可留 `cmd/agent/http.go` |
| `provideAuthHandler`、`provideUsersHandler`、两个 ACP OAuth pass-through、`provideProviderOAuthHandler` | API HTTP registrar；只消费 API/Agent/Model/Channel ports，不直接消费 concrete service |
| `provideMessageHandler`、`provideSessionHandler`、`provideWebHandler`、`webSpeechModelResolver` | API/Chat HTTP；Web/CLI 不再依赖 Channel Manager/Store/Registry |
| `provideMemoryHandler` | API/Memory HTTP；embedded/split 都消费相同 Memory port |
| `provideEmailOAuthHandler` | 管理 API 留 API HTTP，Channel Email capability通过 port；外部 Email webhook不在此进程的 split profile注册 |
| `startServer` 中 admin bootstrap | API owner bootstrap constructor；原`EnsureAdminUser`事务语义保持 |
| `startServer` 中 Bot/Runtime/health checker 注入 | 按 Runtime、Agent、Channel、Model health adapters拆开，由 common composition聚合 |

## 5. `cmd/channel` 逐文件映射（3/3）

| 当前文件（package `main`） | 当前代码 | 目标位置/动作 |
| --- | --- | --- |
| `cmd/channel/main.go` | health handler、config、HTTP server lifecycle、`options()`；当前组合 `core.FoundationModule` + `channel.FoundationModule/RuntimeModule` | Keep/Split：只组合Channel公开constructors与`internal/rpc/channel`；删除对`cmd/internal/core`依赖 |
| `cmd/channel/rpc.go` | Server conn、Turn client、generic runtime client/server、Channel gRPC lifecycle | Rewrite：client/server均归`internal/rpc/channel`；Channel -> Server只提交final typed inbound/control |
| `cmd/channel/main_test.go` | `fx.ValidateApp(options())` | Keep/Rewrite：验证 closed Channel graph，并新增 forbidden-dependency断言 |

Channel child最终只可import Channel公开contract/concrete和`internal/rpc/channel`transport；不得
import`domains/{api,agent,memory,model,runtime,media}/internal`或Server业务实现。

## 6. `cmd/internal/core` 逐文件与 symbol 映射（4/4）

| 当前文件（package `core`） | 当前职责 | 目标动作 |
| --- | --- | --- |
| `module.go` | `FoundationModule`混合DB、API、Agent Chat基础；`ServerModule`混合Runtime/Memory/Model/Agent | Split/Delete；各owner提供公开constructor/composition入口，`cmd/agent/common.go`组合；Channel child不再引入它 |
| `providers.go` | 约 70 个 provider/start/method，横跨全部 domain | 必须按下表 symbol族拆分；禁止整文件 Move到新的 `common`/`wiring` 巨包 |
| `bootstrap_test.go` | 实际测试 provider template definition保真，不是 core bootstrap | Move -> Model template/catalog owner测试；文件名按被测行为重命名 |
| `providers_test.go` | ACP tools、Agent limits、Memory lazy LLM三组测试 | Split -> Agent owner两组 + Memory owner一组；test doubles跟随测试 |

### 6.1 `providers.go` symbol 族

| 当前 symbol 族 | 目标 package / 动作 |
| --- | --- |
| `provideLogger` | `internal/logger` constructor，由 command root调用 |
| `provideDBConn`、`providePostgresStore`、`provideDBQueries` | `internal/database` 只保留 pool/migration；giant Queries随 owner迁出后删除 |
| `provideAccountStore`、`provideAccountService`、`EnsureAdminUser` | API owner identity/bootstrap constructors |
| Runtime、Memory、Model、Agent provider symbol族 | 对应`domains/<owner>`公开constructors + owner internal实现；全部由Server common直接装配，不建立`local`层 |
| `provideSessionService`、`provideMessageService` | Agent Chat/postgres；Channel passive writer是独立Channel-owned port，不复用giant service |
| `provideContainerdHandler`、两个 ACP OAuth Handler constructors | application port由 Runtime/Agent owner提供；HTTP transport registrar归 API，不把 Handler放进 Runtime/Agent local |
| `provideBotBackupService` | API owner backup composition；接受owner ports，不再构造14个concrete deps bundle |
| `provideMediaService`、`mediaAssetResolverAdapter.*`、`gatewayAssetLoaderAdapter.*` | Media owner + Agent consumer adapter；Runtime提供container file capability port |
| `skillLoaderAdapter.*` | Agent consumer adapter；skill execution归 Agent，runtime FS通过 port |
| `resolverBotPermissionChecker.*` | API access port adapter；Agent flow不直接依赖 ACL concrete implementation |
| cleanup/reconciliation lifecycle hooks | 跟随owning service constructor；`cmd/agent/common.go`只组合，不枚举内部goroutine |

## 7. `cmd/internal/channel` 逐文件与 symbol 映射（3/3）

| 当前文件（package `channel`） | 当前职责 | 目标动作 |
| --- | --- | --- |
| `module.go` | `FoundationModule`、`ServerLocalModule`、`RuntimeModule`、`EmbeddedModule`及local/remote adapter providers | Split/Delete；Channel implementation -> `domains/channel` constructors；transport -> `internal/rpc/channel`；ServerLocalModule删除 |
| `providers.go` | Channel registry/inbound/lifecycle、Email、Webhook、以及大量 API/Agent/Runtime concrete adapter | 按下表拆；Channel-owning部分 Move，其余替换为 client/consumer port |
| `providers_test.go` | `newSessionCreatedByUserID` fallback | 行为改由Server boundary handler/Agent session测试覆盖；Channel不保留Session adapter |

### 7.1 `module.go` symbol 级动作

| 当前 symbol | 目标 |
| --- | --- |
| `FoundationModule` | Channel contract/store基础移`domains/channel`；不得在split Server common中加载 |
| `RuntimeModule` | Channel公开constructors + `internal/rpc/channel`，由`cmd/channel`组合 |
| `EmbeddedModule` | Channel公开constructors，只由`profile_embedded.go`导入 |
| `ServerLocalModule` | Delete；Web/CLI归 API/Chat，split Server不保留一小块 Channel implementation |
| `Module` | Delete transitional facade |
| local runtime/interface providers | concrete满足consumer port则直接注入；否则仅保留微型composition adapter |
| `provideRemoteCommandHandler`、`provideRemoteSkillResolver`、`provideRemoteChannelAudio` | 删除细粒度clients；Channel提交final inbound/control，Server本地调用这些能力 |

### 7.2 `providers.go` symbol 族

| 当前 symbol 族 | 目标 package / 动作 |
| --- | --- |
| `providePipeline`、EventStore、DiscussDriver、route、registry、router、manager、lifecycle、manager start | Channel公开constructors，再按`internal/{inbound,outbound,route,adapter}`具体实现注入 |
| `channelRegistryParams` | local composition parameter；adapter列表跟随 Channel，不泄漏给 split Server |
| public media base provider、Webhook tunnel start/listener | Channel owner webhook composition；HTTP endpoints由Channel host注册 |
| `provideCommandHandler`、command skill/FS adapters | 移回Server Agent/command composition；Channel不执行业务command |
| `sessionEnsurerAdapter.*`、settings/audio/permission readers | 收缩为单一typed inbound/Turn boundary；Server本地调用owner |
| Email registry/gateway/trigger/manager start | Channel owner；Email触发通过final inbound/Turn boundary进入Server |
| `inboundTranscriptionResult` | Channel adapter value；若 wire传输则由 typed protobuf mapping替代 |

## 8. Build tag 与配置 fail-fast 契约

### 8.1 Tagged 文件唯一形态

```go
// cmd/agent/profile_embedded.go
//go:build !split

package main

const buildProfile = "embedded"

// profileOptions 直接装配 Channel concrete。
// validateProfile 只在 serve 前执行；配置了 shared_secret 时返回明确错误。
```

```go
// cmd/agent/profile_split.go
//go:build split

package main

const buildProfile = "split"

// profileOptions 只装配 internal/rpc/channel client/server所需边界。
// validateProfile 要求 shared_secret、Server RPC listen 和 Channel target。
```

建议`common.go`调用`validateProfile(cfg)`后再构造FX graph。无论团队最终选择hard error或
一次过渡warning，都不得通过reflection、环境变量或空client在运行时回退到另一profile。

### 8.2 Config 当前 -> 目标

| 当前位置/行为 | 目标动作 |
| --- | --- |
| `internal/config/config.go:Config.SplitChannelRuntime` | Delete；topology由 build tag确定 |
| `InternalRPCConfig.Validate` 同时要求两个 target | Split为按 caller/host capability验证；Server只要求 Channel target，Channel只要求 Server target |
| `ValidateServerRuntime` | 仅split `serve`调用；校验Server listen、secret、Channel target/deadline |
| `ValidateChannelRuntime` | Channel child `serve`调用；校验 Channel listen、secret、Server target/deadline |
| `Config.validate` | 保持通用静态配置校验；不得使 `migrate`依赖 RPC |
| `MEMOH_INTERNAL_RPC_*` env override | Keep；语义从“开启 split”改为“为已编译的 split/child配置 transport” |
| embedded binary + non-empty secret | 建议fail fast并指出应使用split binary或清空secret；是否允许一个release warning过渡期待批准，但绝不能静默切换topology |
| split binary + empty secret/target | fail fast；不能回退 local |

`conf/app.example.toml`、Apple、Windows 的注释必须删除“设置 secret 切换 topology”；Docker、
Kata、devenv 配置继续包含 secret/targets，但说明它们服务于已确定的 split artifact。

## 9. 构建、Docker 与 Compose 逐文件映射

| 当前文件 | 当前事实 | 目标动作 |
| --- | --- | --- |
| `docker/Dockerfile.server` | 已用 `-tags split` 构建 `/app/memoh-server`，Channel保持无tag；两个runtime target复制同一split Server | Keep；后续由dependency guard证明child implementation闭包已排除 |
| `.air.toml` | 已显式用 `-tags split` 构建Server，专供当前双进程devenv | Keep；若未来增加embedded devenv，新增独立air配置，不复用此文件产生含糊语义 |
| `devenv/docker-compose.yml` | Server用 `.air.toml`，Channel `go run ./cmd/channel`，共同读取带secret的 app.dev | Server改用 split air config；migrate明确不启动 profile；Channel不变；health/dependency顺序保持 |
| `devenv/docker-compose.minify.yml` | 与完整版同拓扑 | 同上 |
| `devenv/docker-compose.kata.yml` | 覆盖三服务镜像/config，不改 command | Keep overlay；继承 base split air command；Kata CI需覆盖新 split config路径 |
| `devenv/docker-compose.selinux.yml` | 仅 security labels | Keep；无 profile行为 |
| `devenv/docker-compose.webhook-tunnel.yml` | 仅 Channel tunnel/profile | Keep；确保只依赖 Channel child |
| `docker-compose.yml` | production固定同时启动 Server/Channel，secret为required，targets显式 | Keep split topology；增加 image contract说明/health；不得依赖 secret驱动 binary切换 |
| `scripts/release.sh` | 已构建embedded `memoh-server`、split `memoh-server-split`与`memoh-channel`，archive含profile manifest/checksum | Keep；正式GitHub Release是否上传该archive仍需单独批准和接线 |

### 9.1 关联 Docker/安装兼容面（非 primary owner）

| 文件族 | 必须同步的动作 |
| --- | --- |
| `devenv/Dockerfile.server`、`devenv/Dockerfile.server.kata`、`devenv/server-entrypoint.sh` | 镜像只提供工具/runtime；确认新 air config被 bind mount；entrypoint继续透传 Compose command |
| `docker/server-entrypoint.sh` | 继续固定启动 `/app/memoh-server serve`；该路径现在必须是 split artifact |
| `docker-compose.kata.yml` | 继承 root split topology；`server-kata` target必须使用同一 split binary |
| `docker/docker-compose.yml` | 本地 build overlay继续使用 `docker/Dockerfile.server`；无需另传 runtime topology变量 |
| `docker/docker-compose.cn.yml` | 若继续一个 server image携带两个二进制则只保留 image镜像替换；未来拆 Channel image时必须新增对应覆盖 |
| `docker/Dockerfile.web`、`docker/nginx-app.conf` | split Web仍把 QR/webhook转发 Channel、其余API转发 Server；embedded裸机不使用这套双 upstream镜像 |
| `scripts/install.sh` | Docker install继续生成 secret并部署 split image；升级读取旧 secret不再承担 topology选择；未来 image拆分时同步 pin逻辑 |
| `scripts/db-up.sh`、`scripts/db-drop.sh` | 调用同一统一Migrator；不得因embedded/split选择不同owner migration集合，也不能构造service modules |
| `README.md`、`README_CN.md`、`README_JA.md`、`AGENTS.md` | 删除“secret切换模式”的说明，改成“选择对应 artifact；secret只认证 RPC”；三语同步 |

## 10. CI 与 release workflow 逐文件映射（11/11）

| Workflow | 当前关系 | 目标动作 |
| --- | --- | --- |
| `.github/workflows/go-ci.yml` | 已有默认全仓test，并增加default/split Server与Channel profile test/build job | 后续补dependency closure guard；paths已包含profile/release配置资产 |
| `.github/workflows/docker.yml` | 构建/publish `server` image但不检查 binary profile | 保持 image名称；build后 smoke `memoh-server version/profile`与 `memoh-channel`；路径过滤加入 compose/profile config；发布前证明 split closure |
| `.github/workflows/migrations.yml` | 当前无tag build Agent后执行v1 migrate up/down | 改测v2 fresh baseline、v1 upgrade、resume/failure、owner status/verify；profile不改变migration manifest |
| `.github/workflows/kata-runtime.yml` | 路径触发 Kata runner readiness | paths加入新 air/profile/build config；Kata实际 compose继承 split Server |
| `.github/workflows/release.yml` | 不调用 `scripts/release.sh`，只 source/npm/Desktop | 若二进制 archive是正式资产，新增 OS/arch matrix调用脚本并上传 checksum；否则明确脚本为手工工具，不能在蓝图里声称已发布 |
| `.github/workflows/agents-md-updater.yml` | 无构建关系 | Keep；仅当根结构说明改变时更新输入 |
| `.github/workflows/electron-ci.yml` | Desktop build | Keep；Hosted Server contract不因 profile改变 |
| `.github/workflows/eslint.yml` | Web lint/test | Keep；若 upstream路由配置改变，Web proxy测试由对应 owner补充 |
| `.github/workflows/runtime-ci.yml` | TS remote runtime | Keep；与 Go process profile不同 |
| `.github/workflows/rust-ci.yml` | Rust workspace | Keep |
| `.github/workflows/sync-model-capabilities.yml` | Model capability同步 | Keep；目标 Go路径变化时由 Model ledger更新触发路径 |

建议 Go CI gate：

```bash
go test ./cmd/agent
go test -tags split ./cmd/agent
go test ./cmd/channel
go build -o /tmp/memoh-embedded ./cmd/agent
go build -tags split -o /tmp/memoh-server ./cmd/agent
go build -o /tmp/memoh-channel ./cmd/channel

go list -deps -tags split ./cmd/agent > /tmp/split-deps.txt
! rg '^github.com/memohai/memoh/domains/channel/internal(/|$)' /tmp/split-deps.txt
! rg '^github.com/memohai/memoh/internal/(channel|email|webhooktunnel)(/|$)' /tmp/split-deps.txt
```

不要加入Memory/Model/Runtime/Media forbidden pattern；它们是Server owner，在两个profile中
始终编译。Graph guard只证明Channel implementation被split Server排除。

## 11. mise tasks 映射（58/58）

两个 mise 文件共 58 个 task，全部已分类；只有下列任务需要 profile相关修改：

| Task | 目标动作 |
| --- | --- |
| `cmd/agent:start` | 保持 embedded；新增 sibling `start:split`，命令显式带 `-tags split` |
| `grpc-generate` | 更新为唯一Channel boundary proto目标；Bridge data-plane proto保持独立 |
| `dev`、`dev:webhook-tunnel`、`dev:minify`、`dev:minify:webhook-tunnel`、`dev:selinux`、`dev:kata` | 使用更新后的 Compose；Server必须走 split air build |
| `dev:down*`、`dev:logs*`、`dev:restart*` | Compose路径不变，Keep；若新增 compose文件必须同步命令参数 |
| `bridge:build*`、`dev:workspace-image` | Keep；Bridge data-plane不是 split topology child |
| `db-up`、`db-down` | Rewrite；统一Goose Migrator不初始化profile graph；production不提供模糊的全量down |
| `release` | 当前只运行 `pnpm release`，与 Go archive无关；若正式发布二进制需显式串联 release workflow/脚本 |
| `build-embedded-assets` | Keep；它只调用 `scripts/release.sh --prepare-assets`，不得因此构建任何 profile binary |
| `lint:go`、`lint:go:fix` | Keep默认 graph；CI额外跑 split build/test，避免开发 lint任务变慢 |

其余 32 个当前任务没有 Go profile语义：`submodule-init`、`pnpm-install`、`go-install`、
`swagger-generate`、`sdk-generate`、`sqlc-generate`、`icons-generate`、`dev:kata:status`、
`bridge:mtls:gen`、四个 `kata:*`、`install-socktainer`、两个 `a11y-cli:*`、
`docker:workspace:build`、`lint`、`lint:contract`、`lint:fix`、`lint:clean`、两个
`lint:es*`、`setup` 以及八个 `desktop:*`。它们保持原命令；目录移动导致的生成路径更新
分别由 API/Persistence/Runtime/Model账本负责。

## 12. 测试、发布验证与 rollback

### 12.1 Characterization（移动前）

1. 保存 `go list -deps ./cmd/agent`、`go list -deps ./cmd/channel` 和二者 binary size。
2. 运行现有 embedded/split FX validation、Web local routing、Channel closed graph测试。
3. 对 embedded 与当前 secret-driven split各跑一次 smoke：health、Web SSE、外部 Channel
   inbound、outbound、Email webhook、Weixin QR、graceful shutdown。
4. 保存 production/devenv `docker compose config`输出，确认 service、entrypoint、target、secret。

### 12.2 迁移后的必须验证

| Gate | 验证 |
| --- | --- |
| Compile | default Server、split Server、Channel三种binary独立build |
| Graph | `go list -deps -tags split`不包含Channel business implementation或外部adapter；允许Server-side inbound RPC handler |
| Config | embedded+secret、split无secret、split无target、Channel无Server target均精确失败；migrate不做RPC校验 |
| Parity | 仅跨Channel边界的capability以同一fixture走direct与RPC，比较结果、错误、deadline/cancel和副作用 |
| RPC deploy | Server与Channel来自同一build/version及generated contract并原子升级；D12冻结的identity检测必须把mismatch作为typed deployment error；mixed version、dual-register、旧service fallback均为禁止项 |
| Docker | production及Kata target内的 Server为split；Channel可执行；migrate up/down不连接child |
| Dev | full、minify、SELinux、Kata、Webhook overlay均能解析且Server热重载使用split tag |
| Release | archive解包后核对三个binary名、target OS/arch、version/profile、checksum |
| Compatibility | 旧 bare-metal空secret继续运行embedded；Docker install/upgrade继续使用同一image/config路径 |

建议让 `version` 输出包含 `profile=embedded|split`，并由 profile文件提供编译期常量；这样
CI、镜像 smoke 和用户诊断无需猜测 binary 是如何构建的。

### 12.3 Rollback

- build-profile提交不得包含 schema migration；回滚只需切回上一镜像/二进制。
- Split回滚必须同时回滚Server与Channel；不支持通过RPC兼容层滚动混用新旧进程。
- 第一版 archive保留 embedded `memoh-server` 旧文件名，避免回滚服务管理配置。
- Compose继续使用现有 secret、ports、targets和同一 `memohai/server` image名；回滚到旧 tag
  不需要改 `.env`。
- 在至少一个安装包兼容窗口内保留 `memoh-server-split` 与 `memoh-channel` 文件名；这只保护
  installer/service-manager路径，不代表RPC协议支持mixed version。删除/重命名必须另立release note
  和installer migration。
- 如果split dependency guard失败，不得用运行时分支作为临时修复；修正Channel import graph
  后重新构建两个profile。

## 13. 推荐迁移提交顺序

1. 增加profile/config/dependency characterization tests和三产物CI骨架；在capability正式切换前，
   现有generic实现与runtime topology switch只作为当前行为基线保留，不与typed service同时注册。
   任一capability接入production composition时，必须在同一阶段删除对应generic caller、handler和
   registration；不得形成双调用、fallback或按请求分流。该characterization提交可在Store工作并行准备。
2. 等待Channel owner Store/transaction、API/Agent窄ports与typed RPC spec通过全局Gate；在此
   之前本账本不得合并tag、Docker或release行为变化。
3. 将`cmd/internal/channel`的composition symbol Move/Split到`domains/channel`公开constructors
   与`internal/rpc/channel`；两个command root切到新边界。
4. 将Web/CLI移出Channel implementation，删除`ServerLocalModule`和
   `localFirstChannelRuntime`；domain实现仍可留旧namespace。
5. 新增两个tagged profile文件，删除`SplitChannelRuntime/optionsFor`；default保持embedded，
   dependency guard必须同时证明split closure。
6. 更新Dockerfile、Air、devenv和production Compose，再更新release脚本、workflow、installer
   和三语文档；发布形态按第15节批准答案执行。
7. Profile验证通过后，各domain账本再机械移动implementation；最后删除空的
   `cmd/internal/{core,channel}` facade。Database Epoch v2与SQLC/generated cleanup由owner
   persistence Gate串行完成。
8. Agent worker等任何未来新进程都必须另立durable journal、routing、fencing、resume/recovery
   设计和部署决策；不得自动复制本次Channel profile或预造transport。

## 14. 机械覆盖 manifest

### 14.1 Primary manifest（40/40）

```text
.air.toml
.github/workflows/agents-md-updater.yml
.github/workflows/docker.yml
.github/workflows/electron-ci.yml
.github/workflows/eslint.yml
.github/workflows/go-ci.yml
.github/workflows/kata-runtime.yml
.github/workflows/migrations.yml
.github/workflows/release.yml
.github/workflows/runtime-ci.yml
.github/workflows/rust-ci.yml
.github/workflows/sync-model-capabilities.yml
cmd/agent/http_providers.go
cmd/agent/main.go
cmd/agent/mise.toml
cmd/agent/module.go
cmd/agent/module_test.go
cmd/agent/rpc.go
cmd/agent/rpc_runtime_test.go
cmd/agent/support.go
cmd/agent/support_test.go
cmd/channel/main.go
cmd/channel/main_test.go
cmd/channel/rpc.go
cmd/internal/channel/module.go
cmd/internal/channel/providers.go
cmd/internal/channel/providers_test.go
cmd/internal/core/bootstrap_test.go
cmd/internal/core/module.go
cmd/internal/core/providers.go
cmd/internal/core/providers_test.go
devenv/docker-compose.kata.yml
devenv/docker-compose.minify.yml
devenv/docker-compose.selinux.yml
devenv/docker-compose.webhook-tunnel.yml
devenv/docker-compose.yml
docker-compose.yml
docker/Dockerfile.server
mise.toml
scripts/release.sh
```

审计集合由以下机械规则生成：

```bash
{
  find cmd/agent cmd/channel cmd/internal/core cmd/internal/channel -type f \
    \( -name '*.go' -o -name 'mise.toml' \)
  find .github/workflows -maxdepth 1 -type f -name '*.yml'
  find devenv -maxdepth 1 -type f -name 'docker-compose*.yml'
  printf '%s\n' .air.toml docker/Dockerfile.server docker-compose.yml \
    mise.toml scripts/release.sh
} | sort -u
```

当前结果：`current=40`、`mapped=40`、`missing=0`、`extra=0`。

### 14.2 Direct compatibility surface（26/26，非 primary）

这些文件因 profile语义直接受影响，但其业务/平台 owner在其他账本；本账本只记录同步动作：

```text
AGENTS.md
README.md
README_CN.md
README_JA.md
conf/app.apple.toml
conf/app.docker.toml
conf/app.example.toml
conf/app.kata.docker.toml
conf/app.windows.toml
devenv/Dockerfile.server
devenv/Dockerfile.server.kata
devenv/app.dev.toml
devenv/app.kata.dev.toml
devenv/bridge-build.sh
devenv/server-entrypoint.sh
docker-compose.kata.yml
docker/Dockerfile.web
docker/docker-compose.cn.yml
docker/docker-compose.yml
docker/nginx-app.conf
docker/server-entrypoint.sh
internal/config/config.go
internal/config/config_test.go
scripts/db-drop.sh
scripts/db-up.sh
scripts/install.sh
```

上表实际为 26 项；其中 `internal/config/{config.go,config_test.go}` 由 Persistence/Platform
账本 primary，README/AGENTS由文档 owner，Docker Web/NGINX由 Web发布面，installer/DB脚本
由发布工具 owner，`devenv/bridge-build.sh` 由 Runtime账本 owner。校验口径以 manifest实际
行数 `26/26` 为准，不能把它们加入40项主分母。

## 15. 与其他账本的交界与待团队决策

### 已冻结

- `split` 只选 composition profile，不定义业务边界。
- default `!split` 是 embedded；Docker topology使用 split Server。
- Split Server不得编译Channel business implementation/adapter；可编译无业务policy的Server-side boundary handler。
- Web/CLI local Chat属于 API/Chat，不属于 standalone Channel。
- `cmd/internal/core`、`cmd/internal/channel` 最终删除，不建立长期 facade；也不建立 `internal/composition` 或长期 `cmd/internal/process`。
- 只有跨Channel边界的direct与RPC实现同一consumer-owned contract并做parity test。

### 仍需团队批准

1. Bare-metal 是否采用本账本推荐的三 binary过渡 archive，还是拆成 embedded/split两个
   archive；不能只发布一个含义模糊的 `memoh-server`。
2. GitHub Release是否正式发布 Go binary archives；当前 workflow没有这么做。
3. 首轮是否继续一个 `memohai/server` OCI image携带 Server+Channel两个独立binary，还是同时
   引入 `memohai/channel` image。前者更兼容，也不违反编译隔离。
4. Embedded binary发现 non-empty secret时是 hard error还是一次过渡期 warning。本账本推荐
   hard error；warning仍可能让操作者误以为正在split。
5. `version` 是否增加 machine-readable profile字段。本账本推荐增加，用于CI和诊断。

## 16. 文档自检

```bash
git diff --check -- docs/architecture/migration/composition-release.md
pattern='TO''DO|TB''D|待''补|稍''后'
rg -n "$pattern" docs/architecture/migration/composition-release.md
```

本账本没有修改任何生产代码、配置、Dockerfile、workflow或release脚本；所有目标路径和
命令都是后续实施契约。
