# Workspace 依赖管理实施计划

配套设计：`docs/design/workspace-dependencies.md`（下称「设计」）。本计划把设计 §15 的八个阶段合并为 **5 个代码 PR**，加上本文档层共 6 层，用 GitHub Stacked PRs（`gh stack`）叠成一个栈。每个 PR 是一个可独立评审、可独立回滚的完整单元。文件与行号以 `main@7c33ea831`（#1112 合入后）为准。

## 0. 基线与已定事项

- **基线**：#1110–#1112 已合入。direct runtime 的 launcher 硬编码在 `internal/agent/runtime/codex/config.go:27` 与 `internal/agent/runtime/claudecode/process.go:15`；钉版常量 `protocol.PinnedCodexVersion`（`codex/protocol/methods.gen.go:11`，0.151.0）与 `claudecode.PinnedCLIVersion`（`claudecode/protocol.go:25`，2.1.250）可直接作为 driver 依赖声明的版本来源。
- **版本门是软门**（设计 WD-EXT-001）：runtime 握手对版本不符只告警（`codex/appserver.go:111-119`、`claudecode/turn.go:160-163`），launcher 解析沿用——钉版一致优先，不符仍启动并通知，仅无副本硬失败。
- **版本探测**（设计 WD-CAT-005）：默认 `<候选路径> --version`；清单可选 `scripts.version` 覆盖，经 runner 执行，候选路径由 `MEMOH_DEP_CANDIDATE` 传入。
- **contract 直接移除**，不设对齐门槛（设计 §13.1）。
- **镜像内 CLI 一步删除**，不保留种子副本（设计 §16）。
- **catalog 位于 `internal/workspacedeps/catalog/deps/`**：`//go:embed` 不能引用包外路径。
- **新包 `internal/workspacedeps`**：被 `cmd/internal/core` 与 `internal/handlers` 引用；direct runtime 只通过 `internal/agent/runtime/external` 的端口接触它。

## 1. 用 gh-stack 叠 PR

### 1.1 一次性准备

```sh
gh extension install github/gh-stack      # 本机已装 v0.1.0
gh stack alias                            # 可选：gs = gh stack
```

约束（官方 FAQ 与 CLI 帮助，加本仓库实测）：

- 栈内所有分支必须在**同一个仓库**，不支持 fork。本仓库的 `aki`／`fodesu`／`tommy` 都是 fork 远端，栈只能推 `origin`（felinics/Memoh）；需要时显式 `--remote origin`。
- **`gh stack init` 与 `gh stack add` 会立即把新分支推到远端**（v0.1.0 实测）。不要为「以后再做」的层提前 `add`，只在开始写那一层代码时才加。
- 分支保护、required checks、CODEOWNERS 对栈内每个 PR 都按**最终目标 `main`** 评估；Actions 对每个 PR 都会跑。
- 单栈最多 100 个 PR；auto-merge 暂不可用；merge queue 自下而上逐个评估。
- squash 合并时每个 PR 产出一个 commit；合并部分栈后，最低未合并 PR 自动改 base 为 `main` 并级联 rebase。
- `push`／`sync` 用 `--force-with-lease`；**不要**对栈内分支手动 `git push -f`。
- 没有提交的空分支开不出 PR，`submit` 会跳过它们。

### 1.2 本仓库特有：用独立 worktree

主工作树常被多个会话同时使用，而 `gh stack init/add/up/down/checkout` 都会切分支。栈的全部操作放在专用 worktree：

```sh
git worktree add ../Memoh-workspace-deps main     # 已创建
cd ../Memoh-workspace-deps
```

### 1.3 建栈与加层

```sh
gh stack init --base main workspace-deps/00-docs   # 底层已存在（PR #1132），init 会直接收编
gh stack add workspace-deps/01-core                # 开始写哪一层，再 add 哪一层
```

### 1.4 日常开发

```sh
gh stack checkout workspace-deps/01-core    # 或 gh stack up / down / switch
# ... 改代码 ...
git add -A && git commit -m "feat(workspacedeps): catalog loader"
gh stack sync                               # fetch → 快进 main → 级联 rebase 上层 → force-with-lease 推送 → 同步 PR 状态
```

- 下层因评审改动后，在该层提交并 `gh stack sync`，上层自动 rebase；冲突时 `gh stack rebase`，解决后 `--continue`，放弃 `--abort`。
- 拆层、并层、重排、插层：`gh stack modify`（交互式，要求工作树干净）。

### 1.5 开 PR

```sh
gh stack submit          # 交互式：勾选哪些层开 PR、写标题/描述、选 draft 或 ready，Ctrl+S 提交
gh stack submit --auto   # 非交互：自动标题，新 PR 一律 draft；加 --open 直接 ready
```

- 每层 PR 的 base 自动指向下一层分支，最底层指向 `main`。
- PR 描述末尾按仓库规则附「⚠️ No human QA」行，人工确认后 `gh pr edit --body` 去掉。已有 PR 的标题/描述在 `submit` 编辑器里锁定，改用 `gh pr edit`。
- 描述末尾附 `🤖 Generated with [Claude Code](https://claude.com/claude-code)` 与会话链接；commit 末尾附 `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` 与 `Claude-Session:` 行。

### 1.6 评审与落地

- 评审者只看本层 diff；跨层讨论放在最底层相关 PR。
- 自下而上合并：网页上点某层 PR 的合并按钮会把它及其下方全部未合并 PR 一起合并；或 `gh stack merge --squash`（原子，支持 merge queue）。只合底部几层也可以，上层自动 rebase 到 `main`。
- 合并后 `gh stack sync --prune` 清掉本地已合并分支。

## 2. 栈结构

| 层 | 分支 | 内容 | 对应设计 §15 阶段 | 规模 |
| --- | --- | --- | --- | --- |
| 0 | `workspace-deps/00-docs` | 设计文档 + 本计划（PR #1132，draft） | — | S |
| 1 | `workspace-deps/01-core` | `ExecStream.CloseSend`；bridge 重置钩子；`workspacedeps` 的 catalog（含 agent 清单与脚本）、runner、平台探测、discovery、缓存 | 1、2 | L |
| 2 | `workspace-deps/02-service-api` | 迁移/queries/sqlc/store；service 状态机与并发；HTTP API 与 SSE；agent 类对齐扫描；tool 类更新 worker；swagger/SDK | 3、6 | L |
| 3 | `workspace-deps/03-runtime` | `external` 依赖端口与反馈码；`background.SpawnManaged`；resolver；codex/claudecode 集成；装配与启动校验；toolkit wrapper fallback 删除；bot agents API 的 `dependency` 字段 | 4 | M |
| 4 | `workspace-deps/04-web` | SSE composable、进度对话框、依赖 tab、启用前 preflight、徽标与提醒、聊天侧反馈渲染、i18n | 5、6（前端） | L |
| 5 | `workspace-deps/05-image-contract` | install.sh 删 CLI、删 toolkit wrapper、删 `contract.go` 及全部引用、CI | 7、8 | M |

- 层 1 与层 2 可并行开发（层 2 先对接 fake runner），合入顺序仍是 1 → 2。
- 层 5 合入前必须有层 3 与层 4 的人工 happy path：新镜像不再自带 CLI，首次启用全靠层 4 的安装对话框。
- 每层合入条件：`gofmt`、`mise run lint`（含 `scripts/check-ui-contract.mjs`）、本层新增测试、全量 `go test ./...`；层 2 与层 3 的生成物（sqlc、swagger、SDK）随 PR 提交且 CI 无漂移。

## 3. 各层实施细节

### 层 1 `01-core`

**bridge**

- `internal/workspace/bridge/client.go`：在 `Close()`（L402-410）旁新增 `CloseSend()`，持 `sendMu`，只调 `s.stream.CloseSend()`。
- `client_test.go` 三条用例：`exec sh -s` + `SendStdin("cat; echo done")` + `CloseSend()` → 收到 `done` 与 `EXIT 0`（证明 `bridgesvc/server.go:602-613` 在 Recv EOF 后关 stdin）；`CloseSend` 后 `Close` 无 panic；不 `CloseSend` 仅 `Close` 则进程被 cancel。
- `internal/workspace`：新增 `func (m *Manager) resetBridge(botID string)` 替换 `manager.go:245,365`、`dataio.go:302,540`、`versioning.go:438,457,459` 的直接 `grpcPool.Remove`，并加 `OnBridgeReset(fn func(botID string))`。行为不变，只是集中失效点。

**catalog**（`internal/workspacedeps/catalog/`）

- `catalog.go`（`//go:embed deps`；`Load/Validate/Get/List/MustGet`）、`manifest.go`（设计 §4.2 结构、yaml 解码、`ManifestDigest()`）、`deps/{node,python,uv,codex,claude-code}/`。
- `Validate()`：id 唯一且等于目录名；`requires` 指向存在条目；`source: image` 无脚本；`category: agent` 必有 `version.pin` 且无 `check_update`；`platforms`、`provides` 非空；`scripts.*` 引用文件存在。Server 启动时在 FX provider 跑一次，失败 panic。
- codex/claude-code 脚本：npm 参数照 `docker/toolkit/install.sh:575-606`，去掉 `--os/--cpu/--libc`：
  ```sh
  target="$MEMOH_DEP_HOME/versions/$MEMOH_DEP_VERSION"
  npm install -g --prefix "$target" --include=optional --omit=dev --no-audit --no-fund \
      --registry "${NPM_MIRROR:-https://registry.npmjs.org}" "@openai/codex@$MEMOH_DEP_VERSION"
  dep_switch "$target"
  dep_result "{\"version\":\"$MEMOH_DEP_VERSION\",\"entrypoints\":{\"codex\":\"$MEMOH_DEP_HOME/current/bin/codex\"}}"
  ```
  `update.sh` 与 install 同形；`remove.sh` 删 `$MEMOH_DEP_HOME`；两者 `--version` 可用，不配 `scripts.version`。
- 钉版同源测试放 `cmd/internal/core/providers_test.go`（同时能看到 catalog 与 runtime 常量，避免 workspacedeps 反向依赖 runtime）：`codex` 的 pin == `protocol.PinnedCodexVersion`，`claude-code` 的 pin == `claudecode.PinnedCLIVersion`。
- CI：既有 lint job 追加 `shellcheck -s sh internal/workspacedeps/catalog/deps/**/*.sh`。

**runner**（`internal/workspacedeps/`）

- `prelude.go`：设计 §5.3 文本与 `preludeLines`；`dep_switch` 按 `MEMOH_DEP_OS` 分支（darwin `ln -sfh`，其余 `ln -sfn` + `mv -Tf`）；并发锁 `mkdir "$MEMOH_DEP_HOME/../.locks/$MEMOH_DEP_ID.lock" || exit 75`。
- `runner.go`：`Run(ctx, client, RunSpec, LogSink) (Result, error)`：`ExecStreamWithOptions(ctx, "exec sh -s", workDir, timeout, ExecOptions{Env})` → `SendStdin(prelude + 函数包装脚本)` → `CloseSend()` → 循环 `Recv()` 转发日志（行号减 `preludeLines`）直到 `EXIT` → `ReadRaw(result)` 解析 → `Exec("rm -f <result>")`。结果路径 `<tmp>/memoh-dep-<id>-<nonce>.json`。
- `shim.go`：为每个 entrypoint 生成 `$MEMOH_DEP_BIN/<cmd>`（`exec "<abs>" "$@"`），agent 类复制 `docker/toolkit/bin/claude:4-8` 的 `SSL_CERT_FILE` 设置。
- `layout.go`：native home = `config.DefaultDataMount + "/.memoh/deps/<id>"`（`internal/config/config.go:30`）；remote 用 `ResolvedWorkspaceTarget.Info.DefaultWorkDir`（`internal/workspace/remote.go:79-87`）。

**平台探测、discovery、缓存**

- `platform.go`：单次 exec `uname -s; uname -m; ls /lib/ld-musl-*.so.1 2>/dev/null; printf '%s' "${TMPDIR:-/tmp}"`。
- `discovery.go`：**一次 exec 探完全部依赖**——对每个 dep 输出分隔标记 + `cat state.json` + `test -x /opt/memoh/toolkit/bin/<cmd>` + `command -v <cmd>`；版本按 WD-CAT-005：默认对每个候选执行 `<path> --version` 取首个 `\d+\.\d+\.\d+`，有 `scripts.version` 的依赖改为对每个候选经 runner 执行该脚本（`MEMOH_DEP_CANDIDATE=<path>`）。解析为 `Observed{Source, Version, Entrypoints, StateDigest}`，优先级 state.json → toolkit → PATH。
- `cache.go`：per-(bot,target) 存 `Platform` 与 `map[depID]Observed`；`Invalidate` 注册到 `OnBridgeReset`；暴露 `ObserveVersion(botID, depID, version)` 供层 3 的握手回写。

**测试**：catalog 正反例（`fstest.MapFS`）；runner 用 fake bridge（仿 `internal/agent/runtime/acp/client/process_test.go:338` 的 `recordingBridgeServer`）验证 stdin 内容、`CloseSend` 时机、行号偏移、结果读取与删除，关键用例「脚本含 `read x` 与 `cat` 不会吃掉后续内容」；discovery fixture 覆盖三种来源、两种版本探测、损坏 state.json；平台三组输出。

### 层 2 `02-service-api`

**数据库**

- `db/postgres/migrations/NNNN_bot_dependency_installations.{up,down}.sql`，编号取合入时最大值 +1（当前 0145）。表结构照设计 §7，RLS 四条 policy 照 `0001_init.up.sql:2444-2490`；FK `NOT VALID`；索引 `(team_id, bot_id, workspace_target_id)`；`0001_init.up.sql` 文件尾追加。
- `db/postgres/queries/workspace_dependencies.sql`（风格照 `skill_packages.sql`）：`Get…`、`List…ForTarget`、`List…`、`Upsert…Intent`、`Update…Status`、`Update…Observed`、`ListStale…Operations`、`Delete…`，全部带 `team_id = public.memoh_current_team_id()`。`mise run sqlc-generate`。
- `store.go`：`Store` 接口包装 sqlc（照 `internal/skillpackages/service.go:36`）。

**service**（`internal/workspacedeps/service.go`）

- 依赖 `*workspace.Manager`（`bridge.WithWorkspaceTarget(ctx, targetID)` 后 `MCPClient`，`manager.go:267-283`）、`Store`、catalog、runner、discovery、cache。
- `List`（catalog ∪ discovery ∪ DB 三态对账并回写；agent 类附 `RequiredVersion`/`NeedsAlignment`）、`Preflight`（先判 native 未运行 / remote 离线）、`Install/Update/Reinstall/Remove(…, sink)`、`Rollback`（`Stat versions/<prev>` → 经 runner 跑一段只含 `dep_switch` 与改写 state.json 的 sh）、`CheckUpdates`、`ScriptPreview`、`ReapStale`（中间态超过 `timeout+5min` → `failed`，启动与每小时各一次）。
- 并发：`sync.Map` 键 `(bot,target,dep)` 互斥。

**更新检查**

- agent 类：`startContainerReconciliation`（`providers.go`）之后对运行中 native 各做一次 discovery，`latest_version = 钉版`；不新增周期任务。
- tool 类：`updates.go` 的 `Worker{interval: 24h}`，FX `OnStart`/`OnStop`（ticker 写法参照 `internal/agent/runtime/session/reaper.go`）；每轮 `status=installed` 且 tool 类无 pin → 过滤 running native → 按 `(dep, platform)` 去重跑 `check-update` → 扇出到同 team 同键记录 → 失败只写 `last_error/last_checked_at`。

**HTTP API**

- `internal/handlers/workspace_dependencies.go`：`*ContainerdHandler` 方法族 + `SetWorkspaceDependencyService`（照 `mcp_tools.go:29`），在 `cmd/agent/http_providers.go` 构造处调用；路由在 `ContainerdHandler.Register`（`containerd.go:293`）追加，路径照设计 §11；target 用 `h.pinCurrentWorkspaceTarget`（`skills.go:323-334`）；鉴权 `h.requireBotAccessWithPermission(c, "manage")`（`containerd.go:1205`）；SSE 用 `writeSSEJSON`（`message.go:138`），事件结构体标 `// codesync(workspace-dependency-stream)`。
- `internal/apperror/error.go` 新增 `workspace_dependency.{not_found,platform_unsupported,busy,workspace_not_running,remote_offline,rollback_unavailable}` + `internal/i18n/locales/*.json`。
- swaggo 注解 → `mise run swagger-generate && mise run sdk-generate`。

**测试**：service 用 fake Store/runner/discovery 覆盖状态机全部迁移、stale 回收、missing 对账、采纳、互斥；worker 用 fake clock 验证去重扇出与失败不改 status；handler 用 echo 覆盖 preflight 三种返回、SSE 事件序列、403/404；迁移由 CI 的 PostgreSQL up/down 验证。

### 层 3 `03-runtime`

**端口**（`internal/agent/runtime/external/external.go`，纯新增）

```go
type DependencyRequirer interface { RequiredDependency() (depID, version string) }
type Launcher struct { Path, Version, Source string; Mismatch bool }   // Source: managed|toolkit|path
type LauncherResolver interface {
    ResolveLauncher(ctx context.Context, botID, depID, requiredVersion string) (Launcher, error)
}
var ErrDependencyMissing = errors.New("workspace dependency is not installed")
type DependencyMissingError struct{ DepID, RequiredVersion, TaskID string }  // Is(ErrDependencyMissing)
func (Drivers) RequiredDependencies() map[string]Requirement
```

- `internal/agent/decision/feedback/feedback.go`：`CodeAgentDependencyMissing = "agent_dependency_missing"`、`CodeAgentDependencyVersionMismatch = "agent_dependency_version_mismatch"`。
- `internal/agent/background`：`SpawnManaged(parentCtx, botID, sessionID, description string, run func(ctx, log func(stream, chunk string)) error) (taskID string)` 与 `TaskKindDependency`，复用 `Task`/`TaskEvent`/`RecordOutput`（现有 `Spawn` 绑定 shell 命令，`manager.go:138-150`）。

**resolver**（`internal/workspacedeps/resolver.go`）

- `Service` 实现 `external.LauncherResolver`，顺序照设计 §9.2（钉版一致的 managed → 钉版一致的 toolkit → 任一 managed → 任一 toolkit → PATH → missing），走层 1 缓存。
- `EnsureInstalledAsync(ctx, botID, targetID, depID) (taskID, err)` 用 `SpawnManaged` 投递安装，同键已在跑则返回既有 taskID；missing 时 `ResolveLauncher` 返回 `DependencyMissingError{TaskID}`。

**codex**

- `driver.go`：字段 `launchers` + `SetLauncherResolver`；`RequiredDependency() ("codex", protocol.PinnedCodexVersion)`；在 `startAppServerSession` 之前（`driver.go:478-506`）解析；`DependencyMissingError` → `agentfeedback.New(CodeAgentDependencyMissing, …)`（args `dep_id`/`required_version`/`install_task_id`，i18n `chat.externalAgent.dependencyMissing`，HTTP 409）。
- `process.go`：`startAppServer` 加 `launcher string` 参数，命令串 `escapeShellArg(launcher) + " app-server"`；删 `config.go:26-27` 的 `launcherPath`。
- mismatch 通知：`appServer` 记 `launcherMismatch`，`Prompt` 每 thread 首轮发一次事件，机制复用 `toollessThreads`（`appserver.go:237-246`）；握手上报版本（`appserver.go:110`）回写 `ObserveVersion`。

**claudecode**

- `driver.go:127`（模型目录）与 `:252`（Prompt）解析 launcher；`process.go:37` 的 `startCLI` 加 `launcher` 参数，删 `process.go:15` 常量；`RequiredDependency() ("claude-code", PinnedCLIVersion)`；`turn.go:160-163` 处发 mismatch 事件并回写版本。

**装配与收敛**

- `cmd/internal/core/providers.go`：`provideCodexDriver`（L580）、`provideClaudeCodeDriver`（L599）注入 `*workspacedeps.Service`；`provideDirectAgentDrivers`（L611）内 `validateDriverDependencies(drivers, catalog)`：dep 在 catalog、`provides` 含 launcher 命令、`version.pin` 等于声明版本，失败 panic。
- `docker/toolkit/bin/codex`、`docker/toolkit/bin/claude`：删 `fallback_*` 函数及调用，缺失直接 `exit 127`。
- `internal/botagents/types.go`：`BotAgent` 加 `Dependency *DependencyRequirement \`json:"dependency,omitempty"\``，`CreateRequest` 加 `Enabled *bool`（service 默认仍 `true`，`service.go:137`）；`internal/handlers/bot_agents.go` 按 `agent.Runtime` 从 `h.runtimes.RequiredDependencies()` 填充；swagger/SDK 再生成。

**测试**：codex/claudecode 各用 fake resolver 覆盖三种返回（managed 路径进命令串；mismatch 首轮一次通知、次轮不重复；missing 得到 feedback 且 args 齐全）；`validateDriverDependencies` 对「dep 不在 catalog」「pin 不一致」panic；`SpawnManaged` 事件与完成态。人工：改 `state.json` 版本触发通知；删掉两处副本触发后台安装。

### 层 4 `04-web`

- `apps/web/src/composables/api/useWorkspaceDependencyStream.ts`：照 `useDisplayPrepareStream.ts:42-79`（`client.sse.post` + `fetchSSEProblem` + `localizeSSEErrorEvent` + `normalizeSSEFailure`），事件 `started | log | done | error`，type guard 标 `codesync(workspace-dependency-stream)`。
- `apps/web/src/pages/bots/components/dependency-progress-dialog.vue`：`Dialog` + 日志区（仿 `bot-create-terminal.vue` 的 `role="log"` 与自动滚底，**只用语义 token**——原件的 `bg-zinc-950`/`text-emerald-400` 是老债，新文件零配额）+ 错误区 + 「复制日志」（`useClipboard().copyText`）+ 取消。
- `apps/web/src/pages/bots/components/bot-dependencies.vue`：`PageShell variant="tab"` + `SettingsSection`/`SettingsRow`；版本 `Badge font="mono"`；状态 `StatusDot`/`Badge`（installed=success、missing=warning、failed=destructive、待对齐=info）；操作 `DropdownMenu`（更新／重装／卸载／回滚／查看脚本）；target 选择与刷新；行内「有更新／需对齐」徽标。query key `['bot-dependencies', botId, targetId]`，mutation 走 `useDialogMutation`。
- `apps/web/src/pages/bots/detail.vue`：`tabList`（L395-431）加 `dependencies`（lucide `Package`）；`searchIndex`（L433-460）；`groupedTabs.runtimeKeys`（L521-535）；侧栏 `NavItem`（L186-206）传计数。`NavItem` 的 `trailing` 插槽渲染 `BadgeCount`（`packages/ui/src/components/badge/BadgeCount.vue`）——`packages/ui` 是子模块，先在 felinics/ui 出 PR，再在本层更新指针。
- `bot-agents.vue:399 setAgentEnabled`：`enabled && agent.dependency` → `preflight` → 未通过弹确认 `Dialog`（缺失／不符两种文案）→ 进度对话框 → 成功后 `updateAgent({enabled: true})`；取消或失败不写 enabled；`workspace_not_running` 提示并链到容器 tab。`add-bot-agent-dialog.vue`：direct 类型创建传 `enabled: false`，成功后走同一流程再 PATCH。
- 提醒对话框按 `(bot, dep, latest_version)` 去重，键存 `localStorage`。
- 聊天侧：渲染 feedback 错误的组件对 `agent_dependency_missing` 展示后台任务进度（`install_task_id`），对 `agent_dependency_version_mismatch` 展示「去对齐」入口。
- i18n（en/zh/ja，`i18n.test.ts` 会校验）：`bots.tabs.dependencies`、`bots.dependencies.*`、`chat.externalAgent.dependencyMissing`、`chat.externalAgent.dependencyVersionMismatch`；`apps/web/AGENTS.md` 目录树补 `bot-dependencies.vue`。
- 测试：vitest 覆盖 type guard 与状态→徽标映射；`mise run lint`；人工 happy path：启用 → 确认 → 日志流 → 开关变绿；取消回弹；容器停止提示。

### 层 5 `05-image-contract`

- `docker/toolkit/install.sh`：删 `CODEX_VERSION`/`CLAUDE_CODE_VERSION`（L25-26、L45-46）、`install_agent_packages*`（L575-635）及调用、`agents/` 产出。删除 `docker/toolkit/bin/codex`、`docker/toolkit/bin/claude`。
- 移除 contract（合入前按 `ErrWorkspaceImageIncompatible`、`workspace-contract`、`CurrentWorkspaceContractVersion` 全库 grep 校对）：

| 位置 | 处理 |
| --- | --- |
| `internal/workspace/contract.go`、`contract_test.go` | 删除 |
| `WorkspaceInitPath`、`WorkspaceBridgePath` | 迁到 `manager.go`（`buildWorkspaceContainerSpec` L573、L619 在用） |
| `internal/workspace/manager.go:385` | 删 `validateWorkspaceContract` 调用 |
| `internal/bots/service.go:601` | 删 `ErrWorkspaceImageIncompatible` 分支 |
| `internal/handlers/containerd.go:115-116`、`users.go:523-524` | 删对应 case |
| `internal/apperror/error.go:28`、`:213-216` | 删 `CodeWorkspaceImageIncompatible` 与 i18n |
| `apps/web/src/i18n/locales/{en,zh,ja}.json` | 删 `workspace.image_incompatible` |
| `bots/service_test.go`、`handlers/error_pilot_test.go`、`handlers/users_create_bot_stream_test.go` | 移除相关断言 |
| `docker/workspace-contract.json`、`docker/Dockerfile.workspace:82` COPY | 删除 |
| `.github/workflows/docker.yml:64`（path filter）、`:200-210`（smoke test 中 contract 与 codex/claude 检查） | 删除 |

- `internal/handlers/workspace.go:29` 的 `InitializeNativeWorkspace` 接口保留（模板 bootstrap 仍在）。
- PR 描述引用设计 §13.1，说明依赖级版本门如何承接 v3 注释「版本不匹配要清晰呈现」的诉求。新镜像首次启用 direct agent 走层 4 的安装对话框下载 CLI。

## 4. 横切事项

- **arch 守卫**：`internal/workspacedeps` 只被 `cmd/internal/core`、`internal/handlers` 直接引用；runtime 走 `external` 端口。它可 import `internal/workspace`、`internal/workspace/bridge`、`internal/config`、`internal/db/*`、`internal/agent/background`。不得被 `internal/channel`、`internal/chat/*`、`internal/agent/context`、`native`、`tool` 引用。
- **codesync**：Go SSE 事件结构体与前端 type guard 互标 `codesync(workspace-dependency-stream)`。
- **迁移编号**：合入前核对最大编号；与并行 PR 冲突时后合者重编号。
- **生成物**：`mise run sqlc-generate`、`swagger-generate`、`sdk-generate` 产物随对应层提交。
- **QA 披露**：每层 PR 描述末尾附「⚠️ No human QA」，人工确认后移除；层 3、4、5 必须有人工 happy path。

## 5. 已决与遗留

已决：contract 直接移除（层 5）；镜像内 CLI 一步删除（层 5）；版本探测默认 `--version`、可选 `scripts.version`（层 1）；创建 direct agent 由前端显式传 `enabled: false`（层 3、4）；tab 计数角标做 `NavItem` 插槽（层 4）。

遗留：`docs/design/workspace-dependencies-ux.html` 仍有 ACP/Hermes 时代的文案与条目，随层 4 的实际 UI 一起更新。
