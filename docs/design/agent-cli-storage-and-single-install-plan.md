# Agent CLI 存储、单版本安装与自动恢复实施计划

日期：2026-09-14

状态：实施与验收进行中（见[验证记录](agent-cli-storage-validation.md)）

计划调研基线：Memoh OSS `3c29a2837`；实施基线：`58741b308`

范围：`felinics/Memoh`、Supermarket 依赖 recipe，以及 Memoh Cloud 的对应适配与 E2B 验证。

本文整合 2026-09-14 的 Agent CLI 负载与 Agent Home 存储提案 v3，以及后续关于 Home 配置、凭证持久化、移除 Rollback 的讨论。本文是后续实施依据；原提案中的整体 Home 迁移、凭证软链接、历史版本保留等方案不再作为目标。

用户后续已明确授权实施、本地真实 API/UI 验证（包含 rootfs 删除后的依赖恢复）、并提交相关 PR。该授权不包含生产发布、生产模板切换或生产数据清理。代码与生成物、运行环境、真实 UI 和性能验证的完成情况以独立验证记录为准，不因本文更新自动视为通过。

## 1. 已确定的决策

1. 将 CLI 负载、安装解压暂存和包缓存与持久安装记录分开。E2B 使用沙箱本地盘存放可重新安装的负载，OSS 默认保留现有数据根布局。
2. 每个 Bot 的每个依赖只维护一个已确认的期望安装版本。删除依赖 Rollback 功能，以及供用户回退的历史版本维护。
3. 安装、更新、重装、显式降级使用同一条安装事务。更新失败必须保住当前可用安装；失败且未发布的 candidate 在确认退出后可清理，已发布旧负载保留到完整 Workspace 重启的受控清理窗口。
4. 自动 repair 仅恢复已经授权的准确版本和冻结 recipe，不授权安装新依赖，不重新选择 latest，不从 discovery 推导授权。
5. `ResolveLauncher`、List、Preflight 保持查询职责。恢复请求由显式恢复协调入口或 workspace 就绪事件受理，不能在查询内部通过异步任务间接执行安装。
6. Codex 二进制安装目录与 `CODEX_HOME` 相互独立。`CODEX_HOME` 继续使用 `/data/.codex/agents/<bot_agent_id>`，`auth.json` 保留在其中，不增加凭证软链接，也不因本次优化切换凭证权威来源。
7. Claude Code 的 `HOME=/data`、`CLAUDE_CONFIG_DIR=/data/.claude` 和三种凭证模式保持原语义。保留已有数据库 transcript 检查点恢复，不新增 workspace 登录态入库功能。
8. 优先验证 Codex 原生 `sqlite_home`、`log_dir` 配置，而不是整体迁移 Home。SQLite 包含持久业务状态，不能按缓存直接丢弃；未经版本和持久性验证，不启用 SQLite 本地化。
9. `/opt` 不是技术要求。不引入一个同时控制依赖负载和所有 Agent Home 的全局 `LocalStateRoot`。需要本地存储的用途各自声明路径与生命周期。
10. 保留操作日志与审计身份不等于保留历史二进制。移除依赖版本回滚不影响数据库 down migration、workspace snapshot 回滚或其他业务事务恢复。
11. Linux kernel control 固定使用本地 `/run/memoh/deps`，与负载 store 配置解耦；操作锁、启动/运行租约和执行窗口锁不放在 NFS 上。state、receipt 和生命周期证明记录仍持久化到 `/data`，操作意图由数据库持有。Darwin 保留既有 data root 下的 `lockf` 路径。

## 2. 问题、证据与性能目标

### 2.1 原提案提供的实测

下表来自用户提供的 v3 提案，不是编写本文时重新执行的实验。两类 E2B 环境的模板、npm 版本、内存与地理位置不同，不能用于宣称一个集群普遍快于另一个。具体模板 ID、挂载参数和版本必须在复测时重新记录。

测试对象：`@openai/codex@0.154.0`，原提案记录 tarball 约 130 MB、解压后约 339 MB。自建测试使用 Debian 12、Node 24.14.0、npm 10.9.2、2 vCPU / 2 GB；官方测试为旧模板、手工配置 Node、npm 11.9.0、1 GB。

| 操作 | 自建本地盘 | 自建 `/data` | 官方本地盘 | 官方 `/data` |
| --- | --- | --- | --- | --- |
| 100 MB、1 MB 块写入并 fsync | 266 MB/s | 63 MB/s | 465 MB/s | 58 MB/s |
| 创建 2,000 个 4 KB 文件 | 4.5 s | 38.6 s | 2.9 s | 99.9 s |
| 删除上述文件 | 0.09 s | 16.3 s | 0.04 s | 771 s |
| Codex tarball 解压 | 2.5 s | 196 s | 未记录 | 866 s |
| 复制已解压目录到 `/data` | — | 7.2 s | — | 未记录 |
| npm install Codex | 5.9 s | 约 200 s | 未记录 | 超过 900 s |

| 二进制位置 | CODEX_HOME 位置 | 首次 initialize | 再次 initialize |
| --- | --- | --- | --- |
| `/data` | `/data` | 20.9 s | 3.3 s |
| 本地盘 | `/data` | 16.1 s | 2.3 s |
| `/data` | 本地盘 | 4.0 s | 1.2 s |
| 本地盘 | 本地盘 | 0.6 s | 0.16 s |

原提案还记录了下载 130 MB 约 1.3 s、500 次文件 stat 在 `/data` 约 1,011 ms / 本地约 20 ms，以及 Codex 临时目录清理出现 I/O error。它们支持继续排查存储访问，不足以排除所有网络、进程调度和初始化因素。

### 2.2 已核实与尚未证明的边界

- E2B 上游 NFS 配置包含 `sync`、`noac`、`lookupcache=none`；缓存禁用与 pause/resume 一致性有关。部署版本仍须现场核对。
- 小块写入与元数据操作可能放大远程存储往返开销。本文不把一次 syscall 等于一次 NFS RPC、每次固定 9 ms 作为已证实事实。
- SQLite 官方文档明确描述 WAL 与网络文件系统的限制。这是正确性调查项，保留现状不等于已经证明现有 NFS 部署安全。
- 原提案未测真实 turn 延迟；不能宣称本计划已保证每轮对话提速。
- 原提案的完整 Home 本地化数据不能直接作为“保留 Home，只移动部分目录”的预期结果。
- 约 6 秒只描述有利条件下 npm 安装，不包含 workspace 创建、恢复排队、基础依赖、凭证和会话准备。

### 2.3 验收目标

| 指标 | 目标 | 验证条件 |
| --- | --- | --- |
| E2B Codex recipe 全程安装 | < 30 s | 同模板、资源、版本和下载源；记录冷/热缓存 |
| 带卷 E2B 集成安装 | < 60 s | 环境门控测试；与常规单测分开 |
| app-server 首次 initialize | < 3 s | 目标，不提前保证；由原生目录配置实验决定是否达到 |
| app-server 再次 initialize | < 1 s | 同上，单独记录 warm 状态 |
| 完整 rebuild → Agent 可用 | 建立实测基线 | 包含 repair、握手、凭证、会话恢复 |
| 真实 turn | 报告首事件和总耗时分布 | 不以初始化数据代替 |

安装功能可以独立验收。启动优化若未达到目标，必须如实保留性能缺口，不能以静默丢失 goal、记忆或用户设置换取通过。

## 3. 当前代码边界与实现地图

以下路径以 OSS 基线为准。实施前重新检查 HEAD、局部 `AGENTS.md`、生成物和 Cloud 子模块差异，不套用旧提案中的 remote target 接口。

| 范围 | 主要文件 | 实施责任 |
| --- | --- | --- |
| 路径与 workspace | `internal/workspacedeps/layout.go`、`workspace_access.go`；`internal/workspace/bridge/workspace_info.go`；`internal/workspace/manager*.go`；`internal/workspace/vpath/vpath.go` | 本地负载根、native workspace 信息、恢复就绪事件 |
| 安装事务 | `internal/workspacedeps/service.go`、`runner.go`、`prelude.go`、`finalize.go`、`receipt.go`、`recovery.go`、`remove.go`、`lifecycle.go` | 单版本发布、同版本重装、崩溃恢复、清理 |
| 发现与恢复 | `discovery.go`、`resolver.go`、`resolver_authorized.go`、`cache.go`、`reaper.go`、`updates.go`；新增 `repair.go` 等职责明确的文件 | 观测与期望分离、受理、退避、状态对账 |
| 数据库 | `db/postgres/queries/workspace_dependencies.sql`、`workspace_dependency_catalog.sql`；`internal/workspacedeps/store*.go`；`db/postgres/migrations/` | 可信期望安装、CAS、恢复调度、RLS、定义保留 |
| App | `internal/apps/service.go`、`update.go`、`remove.go`、`reference_cleanup.go` 及依赖适配器 | 确认与引用边界、共享依赖、授权及撤销 |
| HTTP / API | `internal/handlers/workspace_dependencies.go`、`containerd.go`、App handlers、`internal/apperror/` | 移除 rollback、恢复确认、进度和错误 |
| 生成物 | `internal/db/postgres/sqlc/`、`spec/`、`packages/sdk/` | 从 SQL / OpenAPI 重新生成，不手改 |
| Web | `apps/web/src/pages/bots/components/bot-apps.vue`、`dependency-*.vue`；`composables/api/useWorkspaceDependencies.ts`、`useWorkspaceDependencyStream.ts`；`store/dependency-operations.ts`；`utils/workspace-dependency.ts`；`pages/home/components/dependency-missing*` | 单版本 UI、移除回滚、恢复状态和确认 |
| Codex | `internal/agent/runtime/codex/config.go`、`process.go`、`materialize.go`、`auth.go`、`driver.go`、`appserver.go` | 保留 Home 与凭证，原生配置评估、新负载生命周期 |
| Claude Code | `internal/agent/runtime/claudecode/process.go`、`driver.go`、`checkpoint.go` | 保持登录与 Home 语义，验证 CLI 更换和检查点恢复 |
| 配置 / 镜像 | `internal/config/`、`conf/`、`devenv/`、`docker/Dockerfile.workspace` | 配置解析、可写目录、兼容矩阵 |
| recipe | Supermarket 的全部 32 个官方 recipe：5 个 shell recipe 与 27 个生成式 recipe | 本地负载、准确版本、事务目录、失败清理、复合版本解析元数据 |
| 文档 | `docs/design/workspace-dependencies.md`、`docs/workspace-dependencies-upgrade.md`、`docs/configuration.md`、`docs/codebase-map.md`、相关 AGENTS 引用 | 删除旧契约、记录新升级方式与恢复边界 |

基线中 `WorkspaceAccess` 只操作 Bot 的 native workspace；`DataRoot()` 固定为 `/data`，没有 remote target 分支。不得为本计划重新引入 `workspace_target_id`。编写本文时检查到 Cloud 本地子模块仍有旧 `workspaceclient` / targetID 接口，Cloud 是适配工作包，不能直接假定源码相同。

## 4. 存储布局与配置

### 4.1 分开声明用途

依赖负载目录由版本固定，不提供路径配置：OSS 使用 `DepsRoot(dataRoot)`，标准工作区中为 `/data/.memoh/deps`；Cloud 使用 `/opt/memoh/deps`。

- Agent Home、凭据和依赖元数据继续使用 `/data` 下的原有位置。
- Cloud 镜像准备可写的 `/opt/memoh/deps`；不新增 `/opt/memoh/agents`。
- 已安装负载和未完成操作仍按记录中的实际路径定位与清理，不在这次变更中搬动已有文件。
- Linux control 根保持 `/run/memoh/deps`，不随负载目录变化；非默认 data root 使用内部独立 hash 命名空间。

### 4.2 目标布局

```text
/data/.memoh/deps/
  bin/                              # 稳定 shim
  .execution-window.json            # 持久生命周期、bridge owner 与执行受理记录
  .operations/<dep>/<operation_id>/  # 进行中事务 receipt、结果及恢复信息
  .operations/<dep>/.cleanup/       # 已登记负载的待清理记录
  <dep>/
    state.json                      # 当前已提交安装的观测快照
    current -> <store>/<dep>/installs/<installation_id>
    resolutions/                   # 复合版本的持久解析元数据（按 recipe 需要）

<store>/<dep>/
  installs/<installation_id>/       # 当前负载；ID 与版本号分开
  .staging-<operation_id>/           # 本次安装暂存（需要可搬移 staging 的 recipe）
  cache/                            # 包缓存，独立容量/过期策略

/run/memoh/deps/                     # Linux 本地 control，图示为默认 /data
  .locks/<dep>.lock                 # 操作与文件系统提交锁
  .leases/<dep>.lock                # 启动受理租约
  .leases/<dep>-<installation_id>.lock  # 运行安装租约
  .execution-window.lock           # Exec/PTY 与维护窗口受理锁

/data/.codex/agents/<bot_agent_id>/   # CODEX_HOME，保持
  auth.json
  config.toml
  ...
/data/.claude/                      # CLAUDE_CONFIG_DIR，保持
```

`installation_id` 使用受管操作生成的唯一身份，不把语义版本当物理目录唯一键。同版本重装、相同版本不同 recipe 都写入新目录，不覆盖仍在运行的负载。终态只有一个当前安装；已发布的旧目录在完整 Workspace 重启清理窗口到来前作为待清理资源保留，不出现在历史版本选择中。

默认 store root 与记录根相同只是物理共址，不把 state、锁、receipt 纳入负载 GC。Linux 锁 inode 在一个 Workspace 生命周期内保持稳定，不删除或替换锁文件来抢占所有权。Darwin 继续使用既有持久 data root 锁路径；上图的 `/run` 布局只描述 Linux。旧 `versions/<version>` 目录属于兼容读取布局；不再生成供回滚使用的历史链。

OSS 将宿主 `<workspace data root>/run/<bot>` 绑定到 `/run/memoh`，会遮蔽镜像预建目录；这个 host runtimeDir 必须位于本地文件系统，不能是 NFS。默认 root runtime 可在挂载内创建 `deps`；Cloud 在 `/run` 为 tmpfs 时由启动准备创建目录。control 文件可能通过运行时挂载跨 rootfs 重建保留，安全判断依赖完整 Workspace epoch 和 kernel locks，不依赖文件被删除。自定义非 root 镜像仍受既有挂载目录权限约束，本次不扩展任意 UID/GID 的支持契约。

### 4.3 state 与事务数据

新 `state.json` 使用格式版本 2，记录 `installation_id`、当前实际 `payload_path`、`store_root`、期望目标修订号，保留准确版本、entrypoints 与 publication 元数据；新写入不再包含 `previous_version` 和 `PreviousInstallation`。隔离布局的 entrypoints 在提交前验证并定位到具体 installation 的负载内，外部 PATH 使用稳定的受管 launcher。

`state.json` 是 workspace 可修改的观测材料，不是自动脚本授权。实际路径必须校验为该依赖由已授权操作创建/登记的目录；不能信任任意文件内容执行递归删除。

receipt 保存**本次事务**的 `before` 状态、实际 store、candidate 路径、稳定入口、publication、期望修订号与清理进度。`before` 不是长期历史版本，不通过 API 暴露“回到上一版”。成功后可清理 receipt，所以当前负载位置、恢复目标、审计记录不能只存在 receipt 中。

数据库 claim 原子写入可信 `operation_intent`；receipt 的不可变元数据必须与该意图全等。恢复仍验证 candidate 路径、准确版本和冻结定义的版本探针，不需要当前 catalog 的 `requires` 图。缺少可信旧意图或元数据不匹配时，在 kernel lock 下 fence 旧操作并标记 failed，保留原可用负载和 receipt 证据，不新增 desired 授权；后续执行需要重新经 Manage 确认。

## 5. 单版本安装事务

### 5.1 统一动作语义

| 用户/系统动作 | 版本选择 | 执行后状态 |
| --- | --- | --- |
| Install | 用户指定准确版本，或确认流程解析 latest/pin | 一个新的已确认目标 |
| Update | 在执行前冻结准确版本与定义；不在运行中重新解析 | 成功后替换目标 |
| Reinstall | 当前已确认准确版本，或用户明确选择 | 替换当前负载；上一代只作为待清理资源暂存 |
| 指定旧版本 | 显式 Manage 确认，复用安装/重装入口 | 当前目标改为该版本 |
| Repair | 当前授权目标的准确版本及缓存定义 | 目标不变，仅恢复其负载 |
| Remove | 无新版本 | 撤销目标、清理负载与记录 |

保留上层动作名称以表达用户意图，底层复用 provision/commit。不得为降级新增一套历史版本协议。manifest pin、支持的 CLI 版本范围仍生效；拒绝不满足约束的目标，不静默改装另一个版本。

### 5.2 事务阶段

1. 校验 Manage 授权或可信 repair 资格，冻结 publication、准确版本、platform 与目标修订号。
2. 按 Bot 内现有 App/依赖管理互斥约定受理，取得跨 Server 的数据库操作所有权并写入可信 operation intent，再通过 workspace kernel lock 约束脚本。Linux runner、probe、finalize、GC、启动/运行租约与执行窗口锁统一使用本地 control 命名空间；依赖关系按确定顺序处理，避免获取一组锁时死锁。
3. 写 durable receipt；准备独立 staging 与 candidate，当前入口仍指向原负载。
4. 下载、解压、构建或 npm 安装。检查全部声明入口、可执行性、真实版本以及最小运行能力；`-x` 检查不代表 ABI、libc、解释器或 toolkit 已兼容。
5. 可搬移的 shell recipe 在同一本地文件系统内将 staging 发布为唯一 candidate；venv、Conda 等含绝对前缀的生成式 recipe 直接安装到唯一且尚未发布的 candidate，不事后搬移目录。两类都先验证，再由 Server 原子切换稳定 `current`，不得就地覆盖当前安装。
6. 在锁和操作身份保护下写 state/shim，并以 CAS 完成数据库终态、期望目标和安装位置的提交。文件系统与 PostgreSQL 不是原子事务，receipt 必须支持断点对账。
7. 更新 launcher 观测，使用启动租约保护“已解析但未启动”的命令，运行命令继承安装租约保护 CLI 和子进程。原有进程继续完成工作，不因安装提交直接删除其模块或解释器目录。
8. 新状态提交后登记旧负载的清理任务；已发布旧目录要等完整 Linux Workspace 重启、bridge 证明该生命周期尚未接纳普通 Exec/PTY 的窗口才清理。清理失败保留记录并告警，不将安装结果改判失败。

### 5.3 失败与中断矩阵

| 中断位置 | 恢复行为 |
| --- | --- |
| 下载/解压/验证失败，尚未切换 | 旧安装继续可用；清理本次无引用 candidate；期望目标不变 |
| 同版本重装失败 | 原 installation ID 和原入口继续有效，不覆盖原目录 |
| 已切入口，state/DB 尚未提交 | 保留操作所有权；核对 receipt、结果、candidate 与入口；能证明成功则完成提交，否则恢复事务前入口/状态 |
| RPC 断开但脚本可能仍活着 | 标为结果不确定并等待现有 recovery，不能立即重跑或删 staging |
| DB 已提交，清理未完成 | 安装成功；恢复后仅继续清理，无需重复安装 |
| 本地 rootfs 丢失，卷上有成功 receipt | 不能只看 exit=0；重新验证负载存在，缺失则按已提交/待提交目标处理，不伪造 installed |
| 删除或更新已改变期望目标 | 旧请求取消，不恢复旧版本、不重新创建被删除的目标 |

移除用户 Rollback 后仍保留上述事务恢复；不得通过删除所有名为 previous/rollback 的符号破坏它。

### 5.4 旧负载与缓存 GC

- GC 只处理受管负载命名空间，排除 current、正在执行/收尾的 operation、运行进程引用、稳定锁和取消标记。
- 清理路径来自已登记安装身份，并再次校验父目录/软链接边界。禁止 `rm -rf` 一个未经验证的 state 路径。
- 已发布负载只在整个 Linux Workspace 的 PID namespace/PID 1 生命周期重启后，并由 bridge 确认普通 Exec/PTY 尚未受理时清理。Create/Setup、API Start 与 lazy start 都在 ready 前经过 Manager 的同步维护入口；只有 bridge 明确认可才发送删除脚本。
- 执行窗口的 kernel lock 位于本地 control 根，但 epoch/owner/used 记录仍保存在 `/data/.memoh/deps/.execution-window.json`。单独重启 bridge 不能抹去该生命周期已经接纳执行的证据。
- 单独重启 Server 或 bridge、扫描不到进程、固定等待时间，都不提供清理资格。旧 bridge、非 Linux、生命周期证据不足或已接纳普通执行时保留 pending，等待下一次合格维护窗口。
- 合格启动窗口内仍检查租约、bootstrap 进程引用和进程状态读取权限；任何证据不足都延后删除。没有受支持启动证明的 backend 需要运维受控维护，不宣称自动回收已发布负载。
- Node/Python/uv 也可能被 CLI 子进程或用户终端使用。依赖启动租约与 installation 租约保护引用，旧生命周期的 launcher 拒绝新启动；仍不承诺更新成功立即回收所有空间。
- npm、uv 等每依赖缓存由事务锁保护清理：`find -mtime +7` 的文件过期策略，超过 512 MiB 时丢弃缓存目录。持久 `resolutions/` 不属于缓存，不按该策略清理。
- 终态只有一个当前目标，待清理的旧实例是临时资源，不出现在版本选择 UI。旧 `versions/<version>` 仅按已登记明确路径清理；不扫描任意历史目录，也不引入未实现的批量清理 CLI。

## 6. 可信期望安装与数据库迁移

### 6.1 观测与期望分离

保留 `bot_dependency_installations` 的当前观测/操作职责；新增建议表 `bot_dependency_desired_installations`，每个 Team/Bot/Dependency 一条当前期望记录。独立表避免 `correctRecord` 和自动收养覆写授权目标。

| 字段组 | 建议内容 | 写入来源 |
| --- | --- | --- |
| 租户与对象 | `team_id`、`bot_id`、`dependency_id` | 已认证 Team/Bot 上下文 |
| 目标身份 | 不可复用的 `desired_revision`，每次目标修改重新生成 | 已确认管理动作的成功提交 |
| 准确目标 | `version`、`source_url`、`registry_id`、`definition_revision`、`manifest_digest` | 冻结定义与实测安装结果一致后提交 |
| 授权 | `auto_repair_authorized_at`、`authorized_by_operation_id`、可用的 actor 审计身份 | Manage 确认路径；收养不写 |
| 平台/安装位置 | 安装时 platform，当前 installation ID、实际 payload/store 路径 | 受管事务提交，不从任意 workspace 文件导入 |
| 恢复调度 | 待恢复目标、重试次数、下一次重试时间、最后公开结果码 | 恢复协调器；不修改授权身份 |
| 审计时间 | 创建/修改时间 | Server |

列名可在实现时按项目 SQL 命名统一；上述身份隔离、写入来源和 CAS 语义不能省略。新表字段不能被普通 `UpdateObserved` 更新。

### 6.2 授权与撤销规则

- 新 Install/Update/Reinstall 的确认明确包含“该安装丢失时恢复同版本”；成功才更新当前期望。仅表达安装意图、执行失败不能生成新的成功恢复目标。
- App 安装必须把将安装的依赖及冻结定义纳入确认；仅引用已有依赖不自动给该依赖增加 repair 授权。
- 旧记录和收养记录默认没有自动恢复资格。Manage 用户通过准备/确认流程授权，准备结果必须包含准确版本和冻结 publication；其他用户只能看到需要管理员处理。
- repair 不覆盖既有授权 actor，不通过本次自动操作扩大授权。
- Remove 受理时在数据库事务中撤销目标/使 revision 失效，再排入物理删除；删除失败不自动重新安装。用户明确取消删除或重新安装才重新建立目标。
- App 卸载仍遵守现有引用关系：其他 App/独立安装仍引用的依赖不能被连带删除。先决定是否删除依赖，再执行对应目标撤销。
- 所有 repair claim/finish 都比较同一 `desired_revision`。记录已删除、版本改变、授权撤销时 CAS 失败即停止；不能 upsert 出一个旧目标。

### 6.3 SQL 与生成物工作

1. 更新 `0001_init.up.sql`，并添加下一可用编号的成对增量 up/down migration；不提前硬编码迁移编号。
2. 增加期望记录读取、确认、条件替换、撤销、待恢复分页、CAS 受理/完成查询；物理操作仍复用现有 operation ownership。
3. 新表沿用 Bot 删除级联、Team 隔离、RLS 与 FORCE RLS 约束；所有 worker 查询必须显式进入对应 Team 上下文。
4. 新旧库初始化、增量升级、down/up 往返都测。旧观测不能自动 backfill 成“已授权”。
5. 更新 catalog definition GC：当前期望、未完成操作引用的冻结定义不能被清理；不再为不存在的历史回滚版本长期保留定义。
6. 审计用 operation ID 不能依赖成功后已删除的文件 receipt 作为唯一凭据；保留必要的数据库审计摘要或复用可靠的现有操作记录。
7. 执行 `mise run sqlc-generate`，只提交生成器产生的 `internal/db/postgres/sqlc/` 改动。

## 7. 自动 repair 协调流程

### 7.1 入口与只读边界

在 `workspacedeps` 内新增明确的恢复协调职责，复用现有 lifecycle/reaper/background 基础设施，不另外引入消息中间件。

- workspace native bridge 确认 ready 后发出恢复检查事件；bridge reset 只负责失效缓存，不能把“连接被驱逐”当作“已就绪”。
- Server 重启后分页扫描已授权目标，仅检查正在运行的 workspace；不会为后台扫描启动所有暂停的 Bot。
- 用户正常启动 workspace 的执行路径在启动完成后触发检查。为首次使用的竞态提供显式 `EnsureDependenciesReady` 协调入口，由运行准备层调用，不能隐藏在 resolver 内。
- List、Preflight、ResolveLauncher 只读当前观测与已受理操作，不投递任务、不拉取执行 recipe、不启动 workspace。
- 准确区分“需要恢复”与“恢复任务已经受理”。只有数据库已持久受理且目标仍有效时，才对外描述为 queued/installing。
- 不在用户仅查看模型或登录状态时偷偷扩展授权；已有授权目标的恢复可以由执行准备流程受理，新依赖必须走 Manage 确认。

### 7.2 判断规则

1. 读取可信期望记录与对应缓存定义，核对完整身份、retired 状态、platform 和准确版本。
2. 检查该目标实际登记的 managed 安装，兼容旧 home 负载；同时验证稳定 entrypoint/current。
3. managed 目标完整且可运行：无需 repair，即使目录仍是旧布局也不强制搬迁。
4. payload 完整但 current/shim 损坏：在受管锁与身份验证下修复入口，不重新下载。
5. payload 丢失/不可运行：受理准确目标的 repair。区分缺失、权限损坏与 ABI/解释器不兼容；同一制品不能解决的环境错误不得无限重装。
6. toolkit/PATH 有其他版本也不能替代用户明确要求的 managed 目标。观测可报告 fallback 存在，但执行准备必须说明目标未就绪，不能静默降级。
7. 没有期望/授权时继续现有 discovery fallback 行为；可用的手工安装或旧镜像 CLI 不因 catalog 不可用而被禁止。

`Observed.Present` 继续表示“存在候选命令”，不强改成“managed 目标满足”。增加清楚的目标满足/恢复状态投影，避免既丢失期望又重定义旧字段。

### 7.3 recipe 与依赖顺序

- Repair 必须调用 `StoredDefinition`，不能经过会重新选择/下载定义的普通 `operationCatalog` 路径。
- 缓存缺失、定义 retired、身份不匹配或平台不支持：不执行，返回明确的人工处理状态。
- `requires` 图使用当前冻结定义；检测环和缺失引用。每个需要安装的 prerequisite 都要有独立授权目标，不能继承上层依赖的无限安装许可。
- 无受管期望的 prerequisite 可以使用已存在且满足约束的 toolkit/PATH 版本；若不存在，转人工确认，不能自动安装 latest。
- 按拓扑顺序恢复目标；基础运行时恢复失败时停止依赖它的 Agent 安装。
- 同一 recipe 不等于无需联网：上游二进制下载仍可能失败。离线模式、recipe 缓存和制品缓存的含义必须分开展示。

### 7.4 并发、重试与生命周期

- 每个目标只允许一个有效受理请求；跨 Server 通过数据库 CAS 竞争，workspace kernel lock 防止重入执行。
- 出队、获取操作权、切换入口、提交结果各边界重新校验 revision 和 workspace 身份。Remove/Update 优先使旧请求失效；锁串行不能代替目标校验。
- 请求记录持久化，Server 重启不会丢任务或清空重试预算。使用可控时钟测试指数退避、上限与手动重试。
- 对暂时网络故障退避；授权、retired、平台不支持等确定性失败不自动重试。用户请求重试仍使用当前有效目标。
- workspace 不可达/暂停时等待下次 ready，不按超时强制宣布旧脚本死亡；继续沿用 receipt/process 所有权判定。
- 保存 `reason=repair`、desired revision、操作身份、触发原因、排队与执行耗时、重试次数。日志不包含 credential 内容。
- 自动任务默认没有向聊天发送新消息的授权；UI 通过状态查询/既有事件更新。若使用 origin session，沿用 Bot/session 校验，不跨 Bot 投递。

## 8. Supermarket recipe 协议

### 8.1 新旧 Server 兼容

新增 `MEMOH_DEP_STORE`，每个依赖指向自己的负载根；`MEMOH_DEP_HOME` 仍是记录目录，`MEMOH_DEP_BIN` 仍是稳定 shim 目录。

实际协议由 install/update 脚本前导注释 `# memoh-storage-layout: isolated` 声明，注释在不可变制品和 manifest digest 的覆盖范围内；不增加旧 Server 会拒绝的 manifest 字段。新 runner 仅对声明该协议的 recipe 提供隔离路径。

为唯一 candidate 提供 `MEMOH_DEP_INSTALL_DIR`：由 runner 为本次操作生成的最终负载目录；recipe 不自行把版本号当安装身份。recipe 采用兼容分支：

```sh
store="${MEMOH_DEP_STORE:-$MEMOH_DEP_HOME}"
target="${MEMOH_DEP_INSTALL_DIR:-$store/versions/$ver}"
```

- 新 Server + 新 recipe：`installs/<installation_id>` 布局，统一事务与 GC。
- 旧 Server + 新 recipe：保留旧 recipe 的版本路径和提交恢复方式，不使用旧 Server 不认识的目录契约。
- 新 Server + 旧冻结 recipe：仍允许原有布局，不通过改 `MEMOH_DEP_HOME` 破坏脚本；记录真实负载位置。对尚可执行的当前安装做同版本原地覆盖不安全时，要求 Manage 确认隔离 recipe；不宣称旧 recipe 自动获得本地盘性能。
- 旧 recipe 迁移到新 recipe 需要 Manage 确认新 revision，repair 不能为了提速擅自替换冻结脚本。

### 8.2 脚本修改清单

协议范围已从原先 codex、claude-code、node、python、uv 五个 shell recipe 扩展到全部 32 个官方 recipe。其余 27 个由共享 runtime 生成，安装到唯一未发布 candidate；不存在的动作沿用 manifest fallback，不虚构脚本。准确版本包含 recipe 的规范分发标签、四段版本和复合版本标识，不把三段 SemVer 正则等同于所有工具的完整版本协议。

1. 缓存与 staging 改到 store；使用唯一操作路径，不删除其他活跃任务的 staging。
2. 在下载前冻结准确版本；校验解析结果与请求目标一致。Repair 不允许把请求当 dist-tag 再解析。
3. 保持官方源校验、镜像 allowlist、npm `--ignore-scripts` 等既有供应链限制。
4. candidate 发布前执行版本/launcher 验证；Agent CLI 的解释器、native helper 和动态库也纳入运行检查。
5. recipe `dep_result` 的 entrypoints 仍以 `$MEMOH_DEP_HOME/current/bin/<cmd>` 交付，Server 提交时验证并转为具体 installation 内的路径；不把一次性 staging 路径写入长期状态。
6. `dep_switch` 保持接受明确绝对路径；runner/receipt 记录 candidate 和事务前目标。仅在本次脚本具备新协议时应用新发布规则。
7. 旧负载由 Server 统一 GC，recipe 不维护 previous version 清单。旧 Server 兼容分支仅保留其所需语义。
8. 隔离协议的 remove recipe 不自行删除已发布负载；Server 撤销目标和当前入口后按登记路径排队清理。权限不足、busy、路径不可信时不越界清理其他依赖。
9. 通过正常发布流程生成 artifact、release、manifest digest 和 revision；OSS 测试 fixture 用生成工具更新，不手改压缩包字节和摘要。

生成式 npm/Python 复合版本保留小型 `MEMOH_DEP_HOME/resolutions` 元数据，记录冻结软件包集合并校验版本映射摘要，便于 rootfs 丢失后重建相同集合。它不含负载或凭证，也不替代数据库授权；只有考虑当前目标和未完成确认后才能收集无引用解析元数据。健康探针还需验证声明命令和 bundle 模块导入，不能只验证解释器可运行。

可先行验证“本地解压 → 复制到持久卷 staging → 同文件系统发布”的过渡 recipe；这是可选的独立止血交付，不是实现双份长期存储，也不代替最终本地 store 验收。

## 9. Agent Home、凭证与原生目录配置

### 9.1 Codex：保留 Home 和凭证

- 保持 `CODEX_HOME=/data/.codex/agents/<bot_agent_id>`，不因为 launcher 位于其他目录就改变 Home。
- 保持 `auth.json` 普通持久文件；不创建本地到持久卷的软链接，不额外引入每次启动的双向文件复制。
- 保持当前数据库凭证解析、物化、device login 绑定、token CAS 回写和注销流程。二进制更新、重装、repair 不清理 Home。
- 覆盖 API key、ChatGPT 登录、换绑、注销、token 刷新、app-server 崩溃和 rootfs 重建。对“刷新后 DB 回写前中断”检查持久文件与 DB 的实际恢复顺序，不能用旧 DB 无条件覆盖仍可恢复的新 token。
- 若测试暴露既有凭证恢复缺陷，做针对性修正：遵守 credential version、撤销及账号身份，不能从残留文件恢复已撤销账号。不得把与本次安装无关的凭证重构作为提速前置要求。
- 注销验证的是账号访问已撤销/解绑及运行凭证已清理，不强制删除所有审计数据库行。

### 9.2 原生配置验证工作包

Codex 官方文档提供 `CODEX_HOME`、`sqlite_home`、`log_dir`。文档能力不等于所有被支持 CLI 版本都按同样方式工作。

实施 P0 时增加可重复实验与产物，逐项核实：

1. 使用提案的 `0.154.0` 及 Memoh 实际支持版本，检查配置 schema/源码并启动真实 app-server，确认是否识别这些键。
2. 记录每种配置下所有新建/写入文件的真实路径，特别是 state/goals/memories/queue/log SQLite、WAL/SHM、sessions、system skills、arg0 helper。
3. 比较“只移动二进制”“加 log_dir”“加 sqlite_home”三组收益，不能假定一个 sqlite_home 控制全部数据库。
4. 验证多 Bot Agent 路径隔离、权限、重启、pause/resume、rootfs 丢失、会话/goal/记忆恢复。SQLite 目录不得由多个沙箱并发写同一状态。
5. 仅对已证明可丢弃的日志/缓存接入独立本地路径配置；`materializeCodexConfig` 仍是集中物化入口，不在多个启动分支拼配置。
6. 不支持某键的 CLI 保留原行为并报告能力，不悄悄忽略配置后宣称优化已生效。

**本计划的交付默认不移动持久 SQLite。** 如果实验表明达到启动目标必须移动它，必须先提交具体的持久状态恢复设计和故障实验；在“无数据丢失”条件满足前，该优化保持未启用。简单复制活跃 DB、只复制主文件不处理 WAL，或者定期备份后声称零丢失都不可接受。

该验证工作包必须执行并给出结果，不是忽略启动问题。结果可以是“部分优化成立，启动目标未达成且受某类持久状态阻塞”，但不能把它写成完整性能验收通过。

### 9.3 Claude Code

- 保持 `HOME=/data` 与 `CLAUDE_CONFIG_DIR=/data/.claude`，避免改变 git/ssh 配置查找和工作区登录模式。
- API key、OAuth token 继续从 Memoh 凭证注入；workspace 模式继续使用现有工作区凭证。不得自动把 workspace 共享登录态变成某个 Bot Agent 的数据库凭证。
- 保持 settings、plugins、用户选择和 transcript 的持久性。
- 验证新 launcher 安装、更新、repair 后的初始化和真实 turn；保留 `checkpoint.go` 数据库恢复，并测试本地 transcript 丢失后的已发布检查点恢复。
- 不把 Codex 配置键类推给 Claude；若其运行缓存有独立原生配置，另以所支持版本的实际行为验证后纳入，不新增未证实的环境变量。

## 10. API、Web、错误与桌面边界

### 10.1 完整移除依赖 Rollback

- 删除 `RollbackWorkspaceDependency`、路由 `POST /bots/{bot_id}/dependencies/{dep_id}/rollback`、service 公共方法、`ActionRollback` 和只服务该操作的预览、超时及错误。
- 移除依赖响应的 `previous_version`、动作枚举的 rollback，以及新 state 的历史版本结构。
- 删除 `dependency-rollback-dialog.vue`、菜单入口、composable mutation、类型分支、确认/成功/失败文案和仅为回滚存在的测试。
- 更新 en/zh 及实际存在的其他 locale；搜索范围限定依赖功能，不删除 workspace snapshot、聊天事务等无关 rollback。
- 重新生成 OpenAPI 和 SDK。旧 rollback URL 不保留可执行别名，也不重定向到 reinstall。
- 文档、Apps 说明、脚本预览说明与贡献指南中的相关描述同步。旧 schema/历史 migration 作为升级记录可保留，不重写已发布迁移历史。

### 10.2 单版本与恢复界面

- 依赖行显示实际版本、期望版本差异和恢复状态；无差异时不额外增加复杂标签。
- 新安装确认说明丢失时恢复同版本；旧/收养安装的“启用自动恢复”先准备准确目标，再经 Manage 用户确认。
- 安装指定版本可以覆盖显式降级需求，复用现有版本输入和校验，不展示“上一版本”菜单。
- 区分：需要管理员授权、恢复已排队、安装中、退避待重试、需要人工处理。不得将所有 missing 都画成安装中。
- 修复中聊天沿用既有缺依赖反馈；受理成功后才携带 operation/task 身份。完成后提示可重试，不默认重放用户消息或自动发送新 turn。
- 复用 dependency-operations 状态跟踪与断流对账；刷新页面、SSE 断开后从服务器恢复终态，不能永远禁用按钮。
- 对无 Manage 权限用户，只展示状态和管理员操作提示；不显示可执行确认入口。

### 10.3 错误与生成流程

遵守 `.agents/skills/memoh-error-handling/SKILL.md`：领域错误在 handler 映射为稳定 code，HTTP 使用 Problem Details，SSE 使用既有 envelope，前端按 code 本地化，不匹配英文诊断。

优先复用语义准确的现有错误；需要新增时分别表达授权缺失、目标已变化、冻结定义不可用、平台不支持、恢复失败。公开 args 仅包含允许的依赖/任务身份与可操作信息，stderr、路径诊断及 credential 只进入受控日志。

执行 sqlc → Swagger → SDK 生成；检查 HTTP/SSE 各出口与错误本地化。复用客户端 SSE 重试边界，避免 SDK 自动重连无限重复执行管理操作。

Desktop 复用 Web 页面，不 fork；只做相同 UI 和 SDK 的 smoke 验证。若部署要求 Web/Desktop/Server 同步升级，在升级文档明确，不保留 Rollback 来兼容旧客户端。

## 11. 镜像、OSS 与 Cloud 交付

### 11.1 OSS 镜像与配置

- 空 store 配置下不强制创建或使用本地 Agent Home，不改变 CODEX_HOME/CLAUDE_CONFIG_DIR。
- 为镜像部署约定的 store 预创建可写目录；E2B 模板实际执行 uid/gid 由其构建流程验证，不把 uid 1000 当所有 OSS backend 的共同事实。
- Linux 镜像提供 `/run/memoh/deps` 默认目录；适配器的挂载与启动准备必须保证实际目录对运行用户可写且 `flock` 有效。OSS host runtimeDir 必须位于本地文件系统，Cloud 启动处理 tmpfs 目录；不把镜像内权限当作挂载后的权限，也不承诺 control 文件随 rootfs 销毁。
- 自定义目录由部署明确准备；对默认示例路径增加非 root 写入、同文件系统 rename、可执行性检查。
- 保持 toolkit 与 managed CLI 的职责：不为解决安装慢把 Agent CLI 固定预装入 toolkit。
- 覆盖 Docker、containerd、Apple 后端的路径信息与配置传递；不假定它们拥有相同 snapshot/卷生命周期。
- 更新 `docs/configuration.md`、配置模板、开发配置与 `docs/workspace-dependencies-upgrade.md`。

### 11.2 Cloud 必须实现的适配

1. 先检查 OSS 同步基线与 Cloud 原有 workspaceclient / targetID / storage adapter 差异，保留 native-only 管理边界。
2. 将有效依赖 store 配置传到真正启动 CLI/recipe 的执行端，并验证工作区 info 返回值，不能只修改 OSS 配置结构。
3. Cloud 迁移在其实际迁移链执行；新目标表、后台扫描和 CAS 保留 Team/RLS，禁止以单个请求 Team 覆盖全部任务。
4. E2B 模板准备本地 store 和 `/run/memoh/deps` control 的用户权限，验证本地锁行为，构建新模板；发布与模板切换按单独授权执行。检查现有 Bot 使用的实际 template/image，而不是只改默认字符串。
5. 检查 provider 配置、模板 ID 的选择来源和 runtime worker 代码后才能判断哪些配置无需改动；不沿用旧提案“provider 一定不变”的结论。
6. 在 `packages/runtime-worker/internal/integration/` 扩展既有 E2B lifecycle/volume 测试，使用 `E2B_API_KEY` 等现有门控，禁止测试操作生产 Bot 的卷。
7. 验证 pause/resume 保留本地负载；destroy/recreate 保留卷但丢失 rootfs 时，自动恢复准确目标并保留 Home/凭证。
8. 观察根盘用量、临时双份负载峰值、缓存、暂停快照大小及恢复时间。持久卷用量减少不等于资源成本消失。

## 12. 升级与删除旧数据

1. 发布前停止新依赖管理操作和 Agent 执行的受理，等待现有操作终态；检查旧 rollback receipt 是否存在。旧操作必须在旧协议下完成或明确取消并对账，不能在升级后直接删除 case 留下无人处理的任务。从旧 `/data` 锁切换到 Linux 本地 control 前，必须完整重启 Workspace 并确认旧脚本已退出；新锁空闲不能证明旧路径上的 owner 已退出，单独重启 Server/bridge 也不够。
2. 应用数据库迁移。旧观测记录保持可读且不自动获得新授权。
3. 部署匹配的 Server、bridge/workspace 镜像、Web/SDK，确认实际挂载后的 control 目录可写且本地锁有效，完成完整 Workspace 重启后再恢复受理；移除 Rollback 路由与入口，确认旧安装、旧 state 和冻结 recipe 仍能被 discovery 正常读取。
4. 先升级支持新 store 的 recipe，再经确认采用该 revision；已有冻结目标继续按原 revision repair，不能在后台升级它。
5. 启用具备正确权限的本地 store。旧当前负载继续可用，不因新位置为空而误判 missing。
6. 下一次成功安装/更新/重装后移到新负载位置；登记明确旧路径，等完整 Workspace 重启的受控窗口清理。新格式忽略旧历史字段，不再维护上一版定位。
7. 本次不提供扫描所有遗留历史目录的一次性清理 CLI。自动清理仅覆盖已登记且通过路径/生命周期检查的负载；不会再次更新的未知残留目录由运维在维护窗口检查归属、当前入口及活跃操作后处理，不冒称已自动回收。
8. 目录名不可信、归属无法确认或旧操作不明时保留并报告，不能把未知路径作为历史垃圾删除。
9. 旧 Server 可能不认识新状态和布局；发布回退采用匹配发布版本及一致性备份，不能宣称删掉历史负载后仍支持软件版本的一键回退。数据库 down migration 不会恢复已删除二进制。

所有破坏性清理先在可丢弃测试 Bot 验证。生产清理由明确的发布/维护授权执行，本计划本身不执行生产删除。

## 13. 自动化验证矩阵

| 层级 | 必测行为 |
| --- | --- |
| Layout | 空配置旧根、显式本地根、路径校验、home/store 共址、根切换后当前路径仍可定位；Linux control 与 store 解耦、非默认 data root 隔离、本地锁可用 |
| Runner / recipe | 新旧 Server/recipe 组合、准确版本、镜像变量、可执行验证、同版本重装、staging 隔离、禁止 lifecycle scripts |
| Transaction | 切换前失败、切换后崩溃、DB 失败、stream 不确定、receipt 证明成功但负载已丢失、清理失败不反转安装结果 |
| Store / migration | 新库、增量、down/up、无授权 backfill、RLS、多 Team、CAS、防旧任务重建目标、定义 GC 引用 |
| Discovery | managed 缺失但 toolkit 可用、旧布局正常、current 损坏但 payload 正常、版本探测失败、ABI/解释器不兼容 |
| Repair | 只用缓存冻结定义、retired/缺失拒绝、准确版本、前置依赖逐项授权、拓扑顺序、退避、Server 重启续作 |
| Concurrency | 两个 Server 同目标、repair 排队后 Remove、Update 替换目标、App 卸载共享引用、旧 CLI drain 与 GC；Create/Start 维护入口、bridge 单独重启不获得新窗口、旧锁迁移前完整 Workspace 重启 |
| Query contract | List/Preflight/ResolveLauncher 不启动 workspace、不入队、不执行脚本；冷 catalog 下已有 CLI 可用 |
| Rollback removal | 公共路由/SDK/action/UI 不再提供回滚；升级流程正确处理旧进行中操作，事务恢复仍工作 |
| Codex | Home/auth 持久路径、生成/刷新/换绑/注销、runtime 重启、原生配置能力、真实握手/turn、goal/记忆不静默丢失 |
| Claude | 三种认证模式、新 CLI、HOME 不变、设置/插件保留、检查点恢复含 compact 后 transcript 丢失 |
| Web / Desktop | 状态和权限、指定版本、无 rollback、断流对账、刷新恢复、三类入口 Apps/聊天/市场引用路径 |
| E2B | 同卷 pause/resume、rootfs recreate repair、无网与制品不可用、权限、根盘满、性能统计 |

并发测试使用确定性协调和可控时钟，不用长 sleep 证明互斥。测试放在行为边界，不为删除的文案、字面映射或简单路径包装堆叠镜像测试。

建议验证命令（实现后执行，按最终变更包缩放）：

```sh
mise run sqlc-generate
mise run swagger-generate
mise run sdk-generate
go test ./internal/workspacedeps/... ./internal/apps/...
go test ./internal/agent/runtime/codex/... ./internal/agent/runtime/claudecode/...
go test ./internal/apperror/... ./internal/server/... ./internal/handlers/...
go test -race ./internal/workspacedeps/...
pnpm --filter @memohai/web test
mise run lint
git diff --check
```

集成测试采用仓库既有数据库/容器环境与实际 build tags；实施前读取对应测试说明，不能把未运行的 gated 测试记为通过。若 Web 的 test 脚本与上例不同，以当时 package.json 的 Vitest 命令执行并记录。

## 14. 运行环境、真实 UI 与证据

每个修改实现的工作包按根 `AGENTS.md` 执行：

1. 使用 `mise run dev`；复用现有环境时验证实际挂载当前 checkout、依赖和配置，Server/Channel 健康且 Web 可访问。
2. 在真实 `http://localhost:18082` 打开测试 Bot 的 Apps，执行安装、更新、指定旧版本重装、Remove；核对 API 状态和文件系统结果。
3. 验证无 Rollback 入口；Manage 与只读成员的恢复确认/状态不同；刷新和断流后状态正确。
4. 在可丢弃 Bot 上注入负载丢失，保留 Home 与 data 卷，观察 repair 受理、安装、完成，然后运行真实 Codex/Claude turn。
5. 从 UI 验证登录仍有效、Claude 检查点恢复、Codex 原有持久会话/goal 行为。进程握手成功不等于真实 turn 或登录成功。
6. 截取当前版本关键状态并检查图片；记录环境、URL、Bot 测试身份、动作、预期与实际结果、截图和 runtime 日志对应关系。
7. 提交 issue/PR 时按模板上传 GitHub 可访问截图；自动化与 Agent 截图不代替 Human QA。没有人工确认时不勾选 Human QA passed。
8. 启动、浏览器、截图或权限失败必须恢复或明确记录阻塞，不能用单测替代真实 UI 验证。

文档编写时的现状 UI 截图只能说明当前流程和设计依据，不能作为未来“Rollback 已删除”或“repair 已实现”的证据。

## 15. 性能复测工具与方法

新增建议脚本 `scripts/bench-agent-storage.py`，只在明确指定的测试 workspace 内运行；脚本生成独立临时目录并只清理自己创建的资源。不在共享环境执行全局 drop_caches，冷缓存实验放入专用沙箱。

### 15.1 安装与文件系统

- 固定 CLI 精确版本、recipe revision、tarball URL/校验值、Node/npm 版本、CPU/内存、模板、OS/arch/libc、mount 参数。
- 分别测下载、npm view、解压/安装、校验、发布、state 提交、GC；区分 package cache 与页缓存。
- 比较同沙箱的本地 store 与带卷 data root；冷/热各重复多次，报告样本数、原始样本、p50/p95。
- 记录开始/结束根盘与卷空间、进程资源和 RPC/syscall 计数（能采集时）。无法采集就只报告测得耗时，不反推精确 NFS 往返次数。

### 15.2 握手计时器

使用 Python `subprocess.Popen`、独立 stdin/stdout、单调时钟和有界读取：

1. 启动 app-server 前记录 `time.monotonic_ns()`，stderr 写入独立日志并避免管道填满。
2. 发送带唯一 ID 的 initialize 并 flush。
3. 逐行解码 JSON-RPC，忽略其他通知，只匹配该 ID；收到 error 计为失败，匹配成功 response 才结束计时。
4. 不以输出文件非空、首字节出现、固定 sleep 或整个 shell 管道退出作为成功。
5. 使用硬超时和进程退出检测；超时/退出非零写入结构化失败样本，不能作为一个很慢的成功数据点。
6. 计时结束后单独关闭输入、终止/等待进程并在必要时 kill，清理时间另记。
7. 记录 cwd、CODEX_HOME、实际 launcher、sqlite_home/log_dir、生效版本和进程命令；所有路径均按 Bot Agent 隔离。

测试矩阵包含：二进制在卷/本地 × Home 原位置 × 原生目录覆盖组合。完整 Home 本地化可以作为专用实验对照，但不作为生产配置或默认迁移方案。

### 15.3 rebuild 与真实 turn

计时起点为受控重建请求，终点为准确目标 CLI 完成认证后的真实 turn。分解 workspace ready、repair 排队、前置依赖、下载、commit、initialize 和首个 Agent 事件；不要用 npm install 的耗时代替整个恢复时间。

## 16. 工作包与交付顺序

每项完成标准同时包含代码、对应测试、必要生成物和文档；独立 PR 只是组织方式，不表示授权自动创建或合并。

| 工作包 | 实现内容 | 依赖与完成标准 |
| --- | --- | --- |
| P0：能力与基线 | 原生 Codex 配置/写入路径实验、正确握手基准、现有 UI/API/卷基线、确认 Cloud 接口差异 | 给出原始性能样本和状态持久性结论；为后续设计提供真实边界 |
| P1：移除历史版本 | Rollback 全链路删除、旧 state 兼容读取、统一 provision 语义、升级旧操作检查 | 单版本 UI/API 完成，安装事务恢复没有退化 |
| P2：负载与事务 | store 配置/权限、唯一安装目录、runner/env、receipt、激活/提交/GC、runtime drain | 所有失败阶段与同版本重装测试通过，默认 OSS 保持可用 |
| P3：期望与授权 | 新表、CAS、RLS、定义 GC、Manage 准备/确认、App 授权引用关系 | 观测不会改写目标，旧记录不自动获得授权，sqlc/SDK 同步 |
| P4：recipe | 32 个官方 recipe 的 store 协议、兼容分支、准确版本、candidate/校验、解析元数据、artifact 发布准备 | 新旧组合验证，待部署制品和 revision 可追踪 |
| P5：恢复协调 | ready/执行准备入口、持久受理、依赖顺序、去重/退避/撤销、进度 | rootfs 丢失自动恢复；Remove/Update 能使旧请求失效；查询仍只读 |
| P6：运行时与 UI | Codex/Claude 新 launcher、Home/凭证不变、只启用通过 P0 的目录覆盖、单版本/repair UI | 真实认证/turn/会话恢复、权限和 SSE 对账验证 |
| P7：Cloud / E2B | 子模块同步、适配器/Team 迁移、模板与实际 rollout、E2B gated 测试 | 带卷重建、权限、安装性能和真实恢复证据齐全 |
| P8：发布收尾 | 已登记旧负载的受控清理、未知残留目录维护说明、完整升级文档、CI/格式、GitHub 截图、人工 QA 状态 | 所有目标分项报告；未达启动性能目标明确列出，不隐瞒缺口 |

P1/P2 的最终上线必须包含相互依赖的事务协议；不能先删除恢复所需结构再等待后续补齐。P4 的新 recipe 可先做旧 Server 兼容发布，但自动切换冻结定义仍需要明确确认。

## 17. 最终验收清单

2026-09-15 实施进度：勾选项已有对应源码与测试证据；OSS 默认/本地 store、Cloud 真实 API/UI 和 E2B 恢复已验证，四个草稿 PR 已提交。完整运行结果及尚未完成的认证、NFS Home 和最终 PR 检查见[验证记录](agent-cli-storage-validation.md)。

- [x] 没有依赖 Rollback 的公开路由、SDK、动作、菜单、对话框、previous_version 响应和长期历史负载维护。
- [x] 更新失败原安装仍可用；同版本重装不会原地破坏正在使用的目录。
- [x] state、receipt、数据库提交与清理的崩溃边界均可恢复；不确定操作不会重复执行。
- [x] 终态只有一个当前安装；已登记旧负载在合格完整 Workspace 重启窗口清理，证据不足时保留 pending；缓存有独立限制，活跃进程不被 GC 破坏。
- [x] 默认 OSS 和显式本地 store 两种配置均可运行；旧安装与旧冻结 recipe 功能兼容。默认布局的归档重建保留原负载，省略的 `current` 软链由一次入口修复恢复，不重新下载。
- [x] 自动恢复只执行可信授权的准确目标；toolkit fallback 不覆盖期望目标；查询无安装副作用。
- [x] Remove/Update/App 引用变化使旧 repair 无效；多个 Server 不会并发恢复同一目标。
- [x] 新表迁移/RLS、definition cache 保留、SQL/OpenAPI/SDK、i18n 与文档均完整。
- [ ] Codex Home/auth 和 Claude Home/登录/settings/plugins 的持久性未改变；rootfs 重建后真实 turn 可用。
- [x] 原生 SQLite/log 配置实验已完成；未经证明的状态迁移未启用，启动性能缺口按实记录。
- [x] E2B 安装目标、pause/resume、recreate repair、异常网络和根盘压力有可复查证据；持久 Home 的原生 Codex 启动另列为未通过项。
- [ ] 本地真实 UI、当前版本截图、运行时证据和必要 CI 通过；Human QA 状态准确。

## 18. 参考依据

- 用户提供的《Agent CLI 负载与 Agent Home 不再落在 `/data` 持久卷上》v3，2026-09-14；本文保留其性能背景，采用后续讨论确认的新范围。
- [现有依赖设计](workspace-dependencies.md)、[升级与恢复说明](../workspace-dependencies-upgrade.md)、[数据库规则](../database.md)、[开发环境](../development.md)。
- [Codex：配置与状态位置](https://learn.chatgpt.com/docs/config-file/config-advanced#config-and-state-locations)。
- [Codex：配置参考（sqlite_home、log_dir）](https://learn.chatgpt.com/docs/config-file/config-reference)。官方文档为当前能力，实际支持版本在 P0 核实。
- [SQLite WAL 限制](https://sqlite.org/wal.html)。
- [E2B NFS 配置源码](https://github.com/e2b-dev/infra/blob/main/packages/envd/internal/api/init.go)。main 会变化，实验记录需固定实际部署 commit/envd 版本。

源码检查结论不能替代生产部署与运行验证；历史实验数据不能标记为本文新增实现的测试结果。
