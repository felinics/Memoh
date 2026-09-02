# Workspace 依赖管理设计

## 1. 文档目的

1. 把「哪些 agent 可用」从 workspace 镜像内容变成可管理的运行时状态：agent CLI 的版本归 Server 所有（协议钉版），分发与对齐归依赖管理器；runtime/tool 类依赖由用户自由管理。
2. 明确 catalog、数据库、workspace 三者各自是什么的真相源，避免三处状态互相漂移。
3. 给出 direct runtime 启动路径、workspace contract 与依赖管理之间的边界——contract v3 目前用「镜像整体判定」承担的版本对齐职责，由依赖级版本门取代。

本文基于 PR #1110–#1112（External Agent Driver 架构、direct Codex、direct Claude Code）合入后的代码形态。引用这三个 PR 分支的行号在其合入后需校对；其余引用以当前 `main` 为准。

本文中「必须」表示合入条件，「可以」表示实现自行选择但结果必须可观察。

## 2. 问题边界

### 2.1 现状（direct runtime 世界）

#1110–#1112 用 direct runtime 取代了内置 ACP：Server 不再通过 ACP wrapper 与 agent 通信，而是把 CLI 的原生协议编译进自己。

- **协议是编译期耦合。** Codex 侧的 Go 协议代码由 CLI 0.151.0 的 schema 快照生成（`internal/agent/runtime/codex/protocolgen/schema/VERSION.json`，注明「diff 即表示钉住的二进制与快照不一致」）；Claude Code 侧钉住 CLI 2.1.250（`internal/agent/runtime/claudecode/protocolref/VERSION.json`），升级需人工对协议。**CLI 版本升级等于 Server 代码变更。**
- **launcher 硬编码。** `internal/agent/runtime/codex/config.go:27` 钉死 `/opt/memoh/toolkit/bin/codex`，`internal/agent/runtime/claudecode/process.go:15` 钉死 `/opt/memoh/toolkit/bin/claude`。不走 PATH，不做解析。
- **contract v3 把 CLI 版本对齐做成了镜像整体判定。** `internal/workspace/contract.go` 把 `bin/codex`、`bin/claude` 列为必需可执行文件，并靠抬升 `contract_version` 让旧镜像以「版本不匹配」而非「文件缺失」暴露。`docker/toolkit/install.sh` 钉 `CODEX_VERSION=0.151.0`、`CLAUDE_CODE_VERSION=2.1.250`。
- **版本不符目前只告警不拒绝。** Codex 握手后比较 CLI 版本与 `protocol.PinnedCodexVersion`，不符时 `Warn` 并继续服务（`internal/agent/runtime/codex/appserver.go:111-119`，注释明言「drifted binary usually still speaks a compatible superset … warn loudly instead of refusing service」）；Claude Code 在 system 消息里同样只告警（`internal/agent/runtime/claudecode/turn.go:160-163`）。协议解码对未知字段与方法容错。
- **通用 ACP 保留**，作为自定义 Agent 通道：命令由用户自管，解析为 PATH → toolkit 回落（`internal/agent/runtime/acp/client/process.go`）。旧的 pinned toolkit adapter 机制已随内置 ACP profile 一并移除。
- toolkit launcher wrapper 自带 PATH fallback（`docker/toolkit/bin/codex` 的 `fallback_codex`）：toolkit 自己的拷贝缺失时会执行 PATH 上的同名命令，且不做版本检查。

由此产生的问题：

1. **每次 Server 同步协议快照 = 重建并重新分发镜像。** 0.151 → 0.152 的升级要走完整的镜像发布链路，用户必须重建 workspace。
2. **contract 版本随之抬升 = 判所有存量镜像不兼容。** #1110 风险栏自认「workspace contract 升级到 v3，旧 workspace image 需要重建」。这正是引入依赖管理要消灭的判决，如今每个协议快照周期重演一次。
3. **`reconcileNativeWorkspace` 周期性放大判决。** 它在 `internal/workspace/manager_lifecycle.go:600` 调用 `InitializeNativeWorkspace`，后者做完整 contract 校验；Server 每次启动 reconcile 都会把旧镜像的 workspace 标记为 setup failure。
4. **remote target 无法运行 direct runtime。** launcher 硬编码 `/opt/memoh/toolkit`，用户真机上没有这个路径。

### 2.2 目标

- **镜像与 agent CLI 版本解耦。** Server 升级协议快照后，存量 workspace 通过依赖管理器把 CLI 对齐到新钉版，不重建镜像。
- **agent 类依赖的版本归 Server 所有。** 用户决定装不装，不决定装哪个版本；「更新」意味着对齐到 Server 当前钉版。
- **runtime/tool 类依赖按通用包管理。** node、python、uv 及后续通用工具由用户通过 UI 安装、更新、卸载。
- 启用 agent 时阻塞检查依赖，缺失或版本不符则询问用户是否安装/更新。
- 运行时缺失或版本不符时不阻塞，给稳定反馈码与可执行的补救动作。
- 定期检查更新：agent 类与 Server 钉版对比，tool 类查上游。

### 2.3 非目标

- 不做第三方依赖 registry。首版 catalog 只有内置条目。
- 不做 host 侧共享依赖挂载（见 §12.3）。
- 不改变 workspace 镜像的其余契约（bridge、tini、display）。
- 不承诺 direct runtime 在 remote target 可用。entrypoints 解析为其扫清了路径障碍（§9.2 附带收益），但 remote 支持是 runtime 侧的独立决策。
- Hermes 已随 #1112 从产品入口移除，不进 catalog。

## 3. 核心模型

三层，各自是不同东西的真相源。混层是本设计要防的主要失败模式。

| 层 | 内容 | 位置 | 是什么的真相源 |
| --- | --- | --- | --- |
| **Catalog** | 依赖定义、安装脚本、agent 类钉版 | Server 二进制（`//go:embed`） | 「系统支持哪些依赖，怎么装，agent 类装哪个版本」 |
| **Installation** | bot × target × dep 的状态记录 | `bot_dependency_installations` 表 | 「用户想要什么」 |
| **Workspace state** | 实际安装内容 | 容器内 `/data/.memoh/deps/` | 「实际有什么」 |

四条必须遵守的规则：

- **WD-MODEL-001**：Catalog 不落库。它是代码，随 Server 版本演进，需要与 driver 依赖声明一起做静态校验。
- **WD-MODEL-002**：数据库记录意图，不记录事实。容器重建、快照回滚、agent 自行安装都会让数据库与实际状态背离，数据库不得被当作可信来源。
- **WD-MODEL-003**：容器状态是最终事实。任何展示给用户的「已安装」都必须能追溯到一次 discovery，或一次刚完成的安装回执。
- **WD-MODEL-004**：agent 类依赖的目标版本由 Server 决定。catalog 钉版与协议快照（`protocolgen/schema/VERSION.json`、`protocolref/VERSION.json`）的一致性必须由测试强制，禁止两处手工维护同一个版本号。

## 4. Catalog

### 4.1 位置与形式

`internal/workspacedeps/catalog/deps/<dep-id>/` 下每个依赖一个目录，与加载它的 Go 包同目录：

```
internal/workspacedeps/catalog/
├── catalog.go                   # //go:embed deps
└── deps/
    ├── node/
    │   └── dependency.yaml      # source: image，无脚本
    ├── codex/
    │   ├── dependency.yaml      # category: agent，钉版与协议快照同源
    │   ├── install.sh
    │   ├── update.sh
    │   └── remove.sh
    └── claude-code/
        └── ...
```

整个 `deps/` 目录以 `//go:embed` 编进 Server 二进制。`//go:embed` 不能引用包目录之外的路径，所以 catalog 不放在 `conf/`——那里是运行时读取的部署配置，语义也不同。

**为什么 embed 而不是像 `conf/providers/` 那样运行时读目录**：provider YAML 是纯数据，依赖脚本是将在 workspace 内以 root 执行的代码。运行时可改等于多一个篡改面。embed 同时保证 `manifest_digest` 稳定、部署不会漏文件。可以提供一个默认关闭的 override 目录作为调试逃生舱。

改脚本需要发 Server 版本。对 agent 类这不是负担而是必然——钉版本来就跟着 Server 走；对 tool 类，这仍比「重建 workspace 镜像 + 全体用户重新拉取」轻两个数量级。

### 4.2 清单格式

```yaml
# internal/workspacedeps/catalog/deps/codex/dependency.yaml
id: codex
name: Codex
category: agent                  # agent | runtime | tool
icon: openai
description: OpenAI Codex CLI（direct runtime）

source: managed                  # managed | image
requires: [node]
provides:                        # 安装后必须可解析的命令
  - codex

platforms:
  - { os: linux,  arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64] }

version:
  pin: "0.151.0"                 # agent 类必须钉版，测试强制与协议快照一致

timeouts:
  install: 1200                  # 与镜像 MEMOH_APT_COMMAND_TIMEOUT 对齐
  remove: 300
  version: 30

scripts:
  install: install.sh
  update: update.sh
  remove: remove.sh
  # version 可选：省略时 discovery 执行 `<候选路径> --version` 取首个版本号；
  # 设置后改为执行该脚本，脚本把 {"version":"..."} 写入 $MEMOH_DEP_RESULT
  # version: version.sh
  # reinstall 省略时由 runner 编排 remove → install
  # agent 类不允许 check_update 脚本（见 WD-CAT-004）
```

tool 类条目可用 `version.channel: stable` 加 `check_update` 脚本走上游查询；`pin` 非空则锁定版本、不参与更新检查。

- **WD-CAT-001**：`source: image` 的条目（node、python、uv）不得有脚本，不可卸载。它们建模成依赖只为让 UI 有统一的呈现，并为将来支持升级留出路径。
- **WD-CAT-002**：`platforms` 用于准入。不匹配的平台必须在 API 层拒绝并在 UI 置灰，不得依赖脚本自行报错。
- **WD-CAT-003**：脚本必须是 POSIX `sh`，不得使用 bash 扩展或 GNU 专有的命令行为。remote target 的对端不保证是 Debian，甚至不保证是 Linux。此约束同样适用于 runner 注入的 prelude（§5.3）。
- **WD-CAT-005**：版本探测默认为 `<候选路径> --version`，取输出中首个 `\d+\.\d+\.\d+` 形态的 token。`--version` 不可用或输出格式特殊的依赖提供可选的 `scripts.version`，经 runner 执行，候选路径由 `MEMOH_DEP_CANDIDATE` 传入，结果写 `$MEMOH_DEP_RESULT`（`{"version":"..."}`）。两种方式都只在 discovery 内使用并进 §8.5 缓存。
- **WD-CAT-004**：`category: agent` 的条目必须钉版，且钉版与对应协议快照版本的一致性由 Go 测试强制（codex 的 `dependency.yaml` ↔ `protocol.PinnedCodexVersion`，即 `protocolgen/schema/VERSION.json` 的生成物；claude-code 的 `dependency.yaml` ↔ `claudecode.PinnedCLIVersion`）。agent 类不允许 `check_update` 脚本——它的「latest」是 Server 钉版，不需要也不允许查询上游（§10.1）。

### 4.3 六个动作

UI 暴露 install / update / reinstall / remove / rollback 五个通用动作，tool 类额外有 check-update。

`reinstall` 默认由 runner 编排 `remove → install`，清单里的 `scripts.reinstall` 是可选覆盖。

**理由**：独立的 reinstall 脚本是第三份与 install 高度重合的逻辑，必然随时间漂移。只有确实需要特殊处理（保留用户配置、清特定缓存）的依赖才提供覆盖。

`update` 保留独立脚本。增量更新（`npm update`）与全新安装的成本差别足够大，值得单独一份。清单省略时退化为 `install(target_version)`。

`rollback` 没有脚本，也不允许有：runner 检查 `versions/<previous>` 存在后，原子切换 `current` 符号链接并改写 `state.json`（§6）。回滚是「新版本坏了」时的补救动作，它必须是纯数据操作，才能保证自身不会因为脚本、网络或上游而失败。

## 5. 执行模型

### 5.1 脚本不落盘

脚本不写入 workspace 文件系统，而是读取内容后经 stdin 直接执行。

```
command = "exec sh -s"
stdin   = prelude + 包装后的脚本正文
```

**理由**：

- workspace 内零代码残留，快照与 `/data` 导出不会把脚本一起带走。
- remote target 上不往用户真机写任何脚本文件。
- 公共 prelude 变成字符串拼接，无需额外的文件同步机制。
- 脚本内容不进 argv，容器内 `ps` 看不到。安装脚本常带 npm token、私有 registry 凭据，在 remote target 上这个差别是实质的（native 容器内的边界见 WD-SEC-004）。

不采用 `sh -c "<整个脚本>"`：服务端已经是 `/bin/sh -c command`（`internal/workspace/bridgesvc/server.go:565`），实现更简单，但脚本内容会出现在 argv 中。

### 5.2 需要的 bridge 改动

`ExecStream` 目前缺半关闭能力。`Close()` 会先 cancel 整个 stream context（`internal/workspace/bridge/client.go:403-410`）——喂完脚本调用它会连带杀掉进程。

```go
// CloseSend half-closes the send side so the process sees stdin EOF while its
// output keeps streaming. Close() cancels the stream; this does not.
func (s *ExecStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.CloseSend()
}
```

服务端无需改动：`stream.Recv()` 返回 EOF 时已经会关闭 `stdinPipe`（`internal/workspace/bridgesvc/server.go:604-609`）。

调用顺序：`defer stream.Close()` → 发送脚本 → `CloseSend()` → 循环 `Recv()` 直到 `EXIT`。gRPC 的 `CloseSend` 幂等，defer 的 `Close()` 再触发一次是安全的。

gRPC 消息上限 16 MB（`internal/workspace/bridge/client.go:66`），脚本量级远低于此。

不使用非流式的 `ExecWithStdinEnv`：它 buffer 全部输出，安装过程数分钟内没有实时日志。

### 5.3 prelude 与函数包装

从 stdin 读取脚本时，脚本内任何读 stdin 的命令（`read`、npm 交互确认、`apt`）都会吃掉脚本剩余内容，症状是静默截断。runner 在拼接时统一消除：

```sh
# ---- prelude（runner 注入）----
set -eu
export DEBIAN_FRONTEND=noninteractive CI=1

dep_log()    { printf '%s\n' "$*" >&2; }
dep_result() { printf '%s' "$1" > "$MEMOH_DEP_RESULT"; }
dep_switch() {
  case "$MEMOH_DEP_OS" in
    darwin)
      # BSD 工具链没有 mv -T；ln -sfh 为 unlink+create，近原子。
      # remote 安装是用户显式确认的前台操作，该窗口可接受。
      ln -sfh "$1" "$MEMOH_DEP_HOME/current" ;;
    *)
      ln -sfn "$1" "$MEMOH_DEP_HOME/current.tmp"
      mv -Tf "$MEMOH_DEP_HOME/current.tmp" "$MEMOH_DEP_HOME/current" ;;
  esac
}

memoh_dep_main() {
# ---- 脚本正文 ----
...
}
memoh_dep_main < /dev/null
```

**为什么用函数包装而不是 `{ ...; } < /dev/null`**：`sh -s` 读取脚本源用的就是 fd 0，复合命令重定向 fd 0 是否影响 shell 的后续读取，各实现没有保证。函数体在定义时被完整解析，调用时重定向不影响已解析内容，这是确定安全的。

`dep_switch` 按 `MEMOH_DEP_OS` 分支：`mv -T` 是 GNU 扩展，darwin 与 busybox 均无；linux 分支保留 `mv -Tf` 的原子 rename，darwin 分支用 `ln -sfh`。

代价：报错行号偏移一个 prelude 高度。prelude 行数是编译期常量，回传日志时减去即可。

### 5.4 脚本环境变量

| 变量 | 含义 |
| --- | --- |
| `MEMOH_DEP_ID` | 依赖 ID |
| `MEMOH_DEP_HOME` | 该依赖的根目录，由 Server 计算后传入 |
| `MEMOH_DEP_BIN` | shim 目录，注入 PATH |
| `MEMOH_DEP_VERSION` | 目标版本。agent 类恒为钉版；tool 类为空表示 latest |
| `MEMOH_DEP_CURRENT_VERSION` | 当前版本，update / check-update 时有值 |
| `MEMOH_DEP_RESULT` | 结构化结果写入路径 |
| `MEMOH_DEP_CANDIDATE` | 仅 `scripts.version`：待探测的候选可执行文件绝对路径 |
| `MEMOH_DEP_OS` / `_ARCH` / `_LIBC` | 平台信息，由探测得出（见 §12.4） |
| `NPM_MIRROR` / `NODEJS_MIRROR` / `PYPI_INDEX_URL` | 镜像源，复用 `docker/toolkit/install.sh` 已有约定 |

`MEMOH_DEP_RESULT` 由 runner 在 target 的临时目录（`${TMPDIR:-/tmp}`）下生成唯一路径，执行结束读取后删除；删除失败不算错误。不放在 `MEMOH_DEP_HOME` 下——那里只允许数据（WD-FS-002），且临时文件不应进快照。

- **WD-EXEC-001**：脚本不得硬编码 `/data`。`MEMOH_DEP_HOME` 由 Server 按 target 计算，这是同一套脚本能同时服务 native 与 remote 的前提。
- **WD-EXEC-002**：结构化结果写入 `$MEMOH_DEP_RESULT`，不写 stdout。stdout 与 stderr 全部作为日志流转发给前端，不做任何解析，因此不受子进程输出污染。
- **WD-EXEC-003**：退出码只表达成功与失败。`check-update` 不得用退出码区分「有更新 / 无更新」，那会与网络错误、限流混淆。

结果格式：

```jsonc
// check-update（仅 tool 类）
{"installed":"1.7.1","latest":"1.8.0","update_available":true}

// install / update
{"version":"0.151.0",
 "entrypoints":{"codex":"/data/.memoh/deps/codex/current/bin/codex"}}
```

`entrypoints` 是关键回执。Server 据此建立 shim 并记录绝对路径，direct runtime 的 launcher 解析（§9.2）与通用 ACP 都不必依赖 PATH 顺序。

## 6. Workspace 内布局

```
/data/.memoh/deps/
├── bin/                        # shim，注入 PATH，排在 toolkit 之前
├── codex/
│   ├── state.json              # 真相源
│   ├── current -> versions/0.151.0
│   └── versions/
│       ├── 0.147.0/            # 保留上一版，支持回滚
│       └── 0.151.0/
└── .locks/codex.lock
```

- **WD-FS-001**：安装必须先写入 `versions/<new>`，成功后再原子切换 `current`。update 失败不得破坏可用的旧版本，也不得在 agent 进程运行中替换其二进制。
- **WD-FS-002**：`/data/.memoh/deps/` 下只允许出现数据。脚本零残留。

`state.json`：

```json
{
  "dependency_id": "codex",
  "version": "0.151.0",
  "installed_at": "2026-09-01T10:00:00Z",
  "manifest_digest": "sha256:...",
  "entrypoints": {"codex": "/data/.memoh/deps/codex/current/bin/codex"},
  "previous_version": "0.147.0"
}
```

**为什么必须在 `/data`**：容器数据在 writable layer（snapshot），除少数挂载点外无其他持久位置（`internal/workspace/manager.go:521`）。装在 `/opt` 下会在镜像升级时全部丢失。

**副作用**：`CreateVersion` / `RollbackVersion` 会把 deps 一并快照与回滚。这是 §8 对账机制必须存在的直接原因。

shim 的 PATH 注入是一个跨多处的集成点，实现时需逐一盘点 PATH 的构造位置：容器镜像 profile、bridge exec 环境、direct runtime 的 `containerPath` 常量（`codex/process.go:12`、`claudecode/process.go:14`）、通用 ACP 的 `defaultContainerPath`（`acp/client/process.go`）。direct runtime 与通用 ACP 因走 entrypoints 绝对路径而不依赖此注入；PATH 注入主要服务 agent 在终端里直接使用 `provides` 命令。

## 7. 数据库

```sql
CREATE TABLE IF NOT EXISTS public.bot_dependency_installations (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id              UUID        NOT NULL,
    workspace_target_id TEXT        NOT NULL,
    dependency_id       TEXT        NOT NULL,
    source              TEXT        NOT NULL,   -- image | managed，按实际提供方记录
    status              TEXT        NOT NULL,   -- 见 §8.1
    installed_version   TEXT        NOT NULL DEFAULT '',
    latest_version      TEXT        NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    last_error          TEXT        NOT NULL DEFAULT '',
    manifest_digest     TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_<hash> UNIQUE (team_id, id),
    CONSTRAINT bot_dependency_installations_identity_key
        UNIQUE (team_id, bot_id, workspace_target_id, dependency_id),
    CONSTRAINT bot_dependency_installations_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_dependency_installations_dependency_id_check
        CHECK (dependency_id <> '')
);
```

结构与 RLS 照 `bot_skill_package_installations` 执行，包括 `ENABLE`／`FORCE ROW LEVEL SECURITY` 与四条 team policy。

- 新增增量迁移 `NNNN_add_bot_dependency_installations.{up,down}.sql`，同时同步 `0001_init.up.sql`（编号以 #1110–#1112 的 0144/0145 合入后为准）。
- 在 FORCE RLS 表上加外键需使用 `NOT VALID`。
- `0001_init.up.sql` 的同步以文件尾 `ALTER` 追加，保持既有列序。
- 完成后运行 `mise exec sqlc@1.31.1 -- sqlc generate`。

版本语义说明：`missing` 后的「重装」与 botbackup 导入按 `installed_version` 安装（agent 类除外，恒为当前钉版）；rollback 依据 `state.json` 的 `previous_version`，不需要额外列。

## 8. 状态机与对账

### 8.1 状态

| status | 含义 |
| --- | --- |
| `installed` | 已安装且 discovery 确认 |
| `installing` / `updating` / `removing` | 操作进行中 |
| `missing` | 数据库有记录，workspace 中不存在 |
| `failed` | 上次操作失败，`last_error` 有值 |

- **WD-STATE-001**：容器重建后不得删除记录，改标 `missing`。记录表达的是用户意图，删除即丢失意图，用户需要重新逐个操作。保留则可以提供「一键重装全部」。
- **WD-STATE-002**：`installing` 等中间态必须有 stale 超时回收。容器重启会让 exec 中途断开，中间态卡死是必然发生的情况。

### 8.2 discovery 与三态对账

每个依赖的探测顺序：`/data/.memoh/deps/<id>/state.json` → toolkit 回落路径 → `command -v`（PATH 上的副本）。最后一环不可省略——agent 在 workspace 内 `npm i -g` 装出来的副本既没有 `state.json` 也不在 toolkit 路径。

agent 类依赖的 discovery 额外探测版本：`state.json` 直接记录；toolkit 或 PATH 副本按 WD-CAT-005 探测——默认 `<候选路径> --version`，清单设置了 `scripts.version` 时改走该脚本。结果进 §8.5 缓存。版本门（§9.2）依赖这个值。

| 数据库 | workspace | 处理 |
| --- | --- | --- |
| 有 | 有 | `installed`，用实际版本校正 `installed_version` |
| 有 | 无 | `missing`，UI 显示「待重装」 |
| 无 | 有 | 采纳：补一条 `installed` 记录，`source` 按实际提供方记（toolkit/镜像内容 → `image`，其余 → `managed`） |

- **WD-STATE-003**：第三态（采纳）不得省略。agent 在 workspace 内本就能自行安装，快照回滚也会带回未记录的依赖。不承认这个现实会让面板长期与实际不符。

### 8.3 各场景下的表现

| 场景 | `/data` | 结果 |
| --- | --- | --- |
| 重建，`preserve_data=true` | 导出后恢复（`internal/workspace/dataio.go:217`） | 记录有效；镜像可能已换，`source=image` 项需重新探测 |
| 重建，`preserve_data=false` | 丢失 | 全部 `managed` 项转 `missing` |
| `RollbackVersion` | 回到历史时点 | deps 一并回退，必须由 discovery 校正 |
| Server 升级（协议快照变更） | 不变 | agent 类 `installed_version` ≠ 新钉版，进入待更新集合（§10.1） |
| botbackup 导入 | 新容器 | 见下 |

- **WD-STATE-004**：botbackup 只备份依赖清单（`dependency_id` + `version`），不备份二进制内容。二进制跨平台不可移植，且会让备份包膨胀到数百 MB。导入后走一次批量安装（agent 类装当前钉版，忽略备份中的版本号）。

### 8.4 并发

- 按 `(bot_id, workspace_target_id, dependency_id)` 互斥。
- 内存锁为主，数据库中间态为兜底。
- workspace 内 `.locks/<id>.lock` 防御 Server 多实例。

### 8.5 缓存

在 workspace Manager 上维护 per-`(bot, target)` 的依赖状态缓存（含 agent 类的版本探测结果），容器重启或重建时失效（`m.grpcPool.Remove(botID)` 的各调用点是天然失效点）。

**理由**：命令解析与版本门都在 session 启动路径上。运行时探测必须走缓存，未命中才读一次 `state.json`（单次 `ReadRaw`，远低于 exec 开销）；`--version` 探测同理。

## 9. External Agent Runtime 集成

### 9.1 Driver 声明依赖

`external.Driver`（`internal/agent/runtime/external/external.go`）增加可选接口：

```go
// DependencyRequirer is implemented by drivers whose CLI is provisioned as a
// managed workspace dependency. Version is the server-pinned CLI version this
// build's protocol snapshot was generated from.
type DependencyRequirer interface {
	RequiredDependency() (depID, version string)
}
```

- codex driver 返回 `("codex", <protocolgen 快照版本>)`，claudecode driver 返回 `("claude-code", <protocolref 快照版本>)`——版本常量与快照文件同源，不另立数字。
- 装配点 `provideDirectAgentDrivers`（`cmd/internal/core/providers.go:611`）在启动时校验：声明的 dep 存在于 catalog、其 `provides` 包含 launcher 命令、其钉版与 driver 声明一致。校验失败即 panic——「driver 要 codex，catalog 里叫 openai-codex」这类漂移在 Server 启动时暴露，不拖到用户点安装时。（内置 ACP profile 的 `Register` panic 校验已随 #1110 移除，这里是它的继任者。）
- 通用 ACP driver 不实现该接口：命令由用户自管，解析保持 PATH → toolkit 回落，跳过全部依赖检查。

### 9.2 launcher 解析与版本门

**这是本设计对 runtime 侧唯一的硬性改动。**

现状：direct runtime 硬编码 launcher（`codex/config.go:27`、`claudecode/process.go:15`），CLI 版本与 Server 协议快照的对齐完全靠「contract v3 镜像 + install.sh 钉版」这一条链路保证。

改造为按序解析，候选以「钉版一致」为优先序：

1. `/data/.memoh/deps/<id>/state.json` 的 `entrypoints`，且记录版本 == 钉版；
2. toolkit 回落路径 `/opt/memoh/toolkit/bin/<cli>`，且 `--version` 探测（§8.5 缓存）== 钉版；
3. 任一可用副本（managed 优先于 toolkit）但版本 ≠ 钉版 → **仍然启动**，同时发出一次 `agent_dependency_version_mismatch` 反馈（§9.4），面板显示待对齐；
4. 无任何副本 → `agent_dependency_missing`，不启动。

- **WD-EXT-001**：依赖管理器只安装钉版，launcher 解析以钉版一致为优先序，但版本不符**不拒绝服务**。现有 runtime 在握手时已经选择「告警不拒绝」（§2.1），协议解码对未知字段与方法容错；硬拒绝只用于「无任何副本」。不做版本区间、不做「大概兼容」的断言——不符就是不符，如实告知并给出对齐动作。版本值由 discovery 提供，禁止在 runtime 内重复探测；握手上报的实际版本只用于校正缓存。

这与旧设计「不做任何版本信任」（原 WD-ACP-005）方向相反，理由记入 §16：旧论证依赖「ACP transcript 运行时校验兜底」，direct 协议把校验点前移到了编译期，版本一致从可选优化变成了运行前提——但「前提不满足」的处置沿用 runtime 既有的容错取向，由依赖管理器负责尽快对齐，而不是由 launcher 拒绝。

配套收敛：toolkit launcher wrapper 的 PATH fallback（`docker/toolkit/bin/codex` 的 `fallback_codex`）在本节落地后删除。它不做版本检查，会绕过 WD-EXT-001 执行任意 PATH 上的同名命令，与版本门直接冲突；两套回落语义也必然漂移。

附带收益：launcher 不再绑定 `/opt/memoh/toolkit`，remote target 运行 direct runtime 的路径障碍消除（是否支持见 §2.3）。

### 9.3 启用时的阻塞检查

入口是 `apps/web/src/pages/bots/components/bot-agents.vue:399` 的 `setAgentEnabled()` 与 `add-bot-agent-dialog.vue` 的创建流程。

流程：

1. 用户启用（或新建并启用）一个 direct agent。
2. 前端调用 `POST /bots/{bot_id}/dependencies/preflight`，**阻塞等待**。
3. 已满足（已装且版本 == 钉版）→ 正常写入 `enabled: true`。
4. 不满足 → 弹出确认对话框：「启用 Codex 需要在工作区安装 Codex 0.151.0（约 120 MB），是否现在安装？」版本不符时文案为更新：「Codex 需要从 0.147.0 更新到 0.151.0」。
5. 用户确认 → 打开安装进度对话框（SSE 日志流），完成后自动写入 `enabled: true`。
6. 用户取消 → 开关回弹，不写入。

- **WD-EXT-002**：preflight 未通过且用户未确认安装时，不得写入 `enabled: true`。开关状态必须回滚到启用前。
- **WD-EXT-003**：安装失败时开关必须回弹，错误在对话框内展示，并保留完整日志供复制。
- **WD-EXT-004**：workspace 未运行或未创建时，preflight 不得自行拉起容器，返回 `workspace_not_running` / `workspace_missing`，UI 呈现「启动工作区」/「创建工作区」引导。install 动作对 native target 可以自动 `EnsureNativeRunning`——用户已显式点了安装；remote target 必须在线，离线即拒绝。

配套 API 面：bot agents API 必须暴露 `dependency_id` 与 `required_version`（来自 driver 声明），前端才能发起 preflight 并渲染版本文案。SDK 随 swagger 再生成。

### 9.4 运行时兜底

配置时已满足的依赖仍可能在运行时缺失或版本不符（容器以 `preserve_data=false` 重建、快照回滚、Server 升级后钉版变更）。

session 启动前做一次廉价探测（走 §8.5 缓存）。不满足时**不阻塞**，返回稳定反馈：

```go
CodeAgentDependencyMissing         = "agent_dependency_missing"
CodeAgentDependencyVersionMismatch = "agent_dependency_version_mismatch"
```

`missing` 阻止本次启动；`version_mismatch` 不阻止（WD-EXT-001），作为会话内一次性通知附带在流上，并让面板与角标进入待对齐状态。两者都携带 `dep_id`、`required_version`，`missing` 另带 `install_task_id`，前端据此渲染进度与取消按钮。风格与 `internal/agent/decision/feedback/` 的既有稳定反馈对齐（该包在 #1110 中已按 External Agent 语义重构，落地时挂到合入后的常量表）。

同时把安装/更新投递到 `internal/agent/background/`，用户消息侧收到：

> Codex 需要更新到 0.151.0，正在后台更新（约 1-2 分钟），完成后请重发消息。

- **WD-EXT-005**：运行时路径不得阻塞式安装。用户发送消息期待的是秒级响应，卡住数分钟且无解释比报错更差；安装需要联网、会失败、多个 session 会并发触发同一依赖。
- **WD-EXT-006**：通用 ACP 路径的 `commandResolveWindow`（5 秒）不得用于等待安装。它的语义是等待刚写入的文件可见，不是等待长任务。
- **WD-EXT-007**：版本门拒绝启动、以及会话恢复因状态不符失败时，错误必须给出稳定错误码与用户可读文案，并列出可执行动作——「更新到 0.151.0」或「回滚到 0.147.0」必须是真能点的按钮，不是一句安慰。`versions/` 保留上一版（WD-FS-001）正是为此。会话恢复侧的具体挂点在 #1110 重构后的 checkpoint 体系（`agent_session_*` 表、各 runtime 的 `checkpoint.go`），以合入后代码为准。

需要全自动时，做成 bot 设置项「依赖版本不符时自动对齐并等待」，默认关闭。阻塞必须是用户的显式选择。

## 10. 更新检查

### 10.1 agent 类：与钉版对比

纯 Server 侧计算：discovery 得到的 `installed_version` 与 driver 钉版比较。无容器内脚本、无 registry 查询、无 TTL。

- **WD-UPD-A01**：Server 启动完成 reconcile 后，agent 类的「需要对齐」集合必须立即可见（依赖面板徽标、bot 详情页角标），不等周期任务。协议快照变更只发生在 Server 升级时，启动即是检查点。
- **WD-UPD-A02**：不自动更新（默认）。运行时兜底（§9.4）已覆盖「用户没理会徽标直接使用」的路径，自动对齐由 §9.4 的设置项显式开启。
- agent 类的更新确认对话框**必须**呈现版本要求的来源（「当前 Server 需要 Codex 0.151.0」）。这不是对上游语义的预测，是本 Server 构建的实现事实。

### 10.2 tool 类：上游检查

当前没有 Server 级的周期任务框架，`ReconcileContainers` 是 FX `OnStart` 时执行一次（`cmd/internal/core/providers.go`）。需要新增一个轻量周期 worker。

- **WD-UPD-001**：默认每 24 小时一轮，仅对 `status = installed` 且 `version.pin` 为空的 tool 类条目执行。
- **WD-UPD-002**：只检查处于运行状态的 native workspace。停止的容器跳过，remote target 跳过（对端是用户设备，不应被后台唤醒）。
- **WD-UPD-003**：同一依赖的检查结果按 `(dependency_id, target 平台)` 在 team 内共享缓存。10 个 bot 都装了同一工具时，不得产生 10 次上游查询。
- **WD-UPD-004**：检查失败只写 `last_error` 与 `last_checked_at`，不改变 `status`。网络抖动不得让已装依赖显示为异常。
- **WD-UPD-005**：不得自动更新。更新可能引入行为变化，用户需要知道自己动了什么。
- **WD-UPD-006**：tool 类的更新确认对话框只呈现版本对比与回滚保证，不对目标版本的兼容性做任何断言——依赖管理不解释 tool 类版本的语义。（agent 类相反，见 §10.1。）

结果写入 `latest_version` 与 `last_checked_at`。提醒对话框每个 `(bot, dep, latest_version)` 组合最多弹一次，用户忽略后不再重复，直到出现更新的版本。

### 10.3 手动刷新

面板提供刷新按钮：tool 类触发一次即时 check-update 绕过 TTL；agent 类触发一次 discovery 重探。

## 11. API

```
GET    /bots/{bot_id}/dependencies
       ?workspace_target_id=  列出 catalog ∪ 安装状态（含 image 来源项与 agent 类版本要求）

POST   /bots/{bot_id}/dependencies/preflight
       body: {dependency_ids: [...]}      启用 agent 前的阻塞检查
       响应含 workspace 可用性状态（WD-EXT-004）

POST   /bots/{bot_id}/dependencies/{dep_id}/install     SSE
POST   /bots/{bot_id}/dependencies/{dep_id}/update      SSE
POST   /bots/{bot_id}/dependencies/{dep_id}/reinstall   SSE
POST   /bots/{bot_id}/dependencies/{dep_id}/rollback    同步（纯数据操作，无日志流）
DELETE /bots/{bot_id}/dependencies/{dep_id}             SSE

POST   /bots/{bot_id}/dependencies/check-updates        手动刷新，同步
GET    /bots/{bot_id}/dependencies/{dep_id}/script      查看待执行脚本
```

SSE 事件序列复用容器创建的既有模式（`internal/handlers/containerd.go`）：

```
{"type":"started","dependency_id":"codex","version":"0.151.0"}
{"type":"log","stream":"stdout","data":"..."}
{"type":"done","version":"0.151.0","entrypoints":{...}}
{"type":"error","code":"...","message":"..."}
```

- **WD-API-001**：必须提供「查看待执行脚本」接口。脚本不落盘，出问题时用户无法进入容器自行查看，这个入口是必需项而非增强。

流程完成后按仓库的 API 开发工作流执行 `mise run swagger-generate` 与 `mise run sdk-generate`（含 bot agents API 的 `dependency_id` / `required_version` 字段扩展）。

## 12. 平台与 target

### 12.1 平台差异的处理层次

- **准入靠清单**：`platforms` 决定 UI 是否置灰、API 是否拒绝。
- **差异靠变量**：`MEMOH_DEP_OS` / `_ARCH` / `_LIBC` 注入，脚本内 `case` 分支。`docker/toolkit/bin/npm` 已是此写法（以 `ls /lib/ld-musl-*` 判 libc）。
- **按需下沉**：仅当 `case` 分支已无法阅读时，才允许 `install.darwin.sh` 覆盖同名默认。不预先铺开文件矩阵——多数依赖是 npm 包，跨平台逻辑相同，拆分会造成大量重复并逐渐漂移。

### 12.2 remote target

`WorkspaceTargetRemote`（`internal/workspace/remote.go:22`）指向用户自己的机器。

- **WD-PLAT-001**：remote target 一律不自动安装，必须显式确认。
- **WD-PLAT-002**：脚本在 remote 上不得使用 `sudo`、`apt` 或任何系统级包管理器。
- **WD-PLAT-003**：remote 不参与后台更新检查。

### 12.3 不做共享挂载

让 host 安装一份依赖供多个 bot 共享会撞上：bot 间隔离被打破、版本冲突、remote target 无法挂载、三个容器后端挂载语义不一致。这等于把 PR #852 刚拆掉的结构装回来。

需要节省带宽时，做 host 侧的 registry 镜像或缓存代理，通过 env 注入（`NPM_MIRROR` 等约定已存在），容器内仍各持独立副本。首版不做。

### 12.4 平台探测

deps 管理器需要 `MEMOH_DEP_OS`／`_ARCH`／`_LIBC` 注入脚本环境。不读镜像的 contract 自我声明，运行时探测：

- `uname -s` / `uname -m`
- `ls /lib/ld-musl-*.so.1` 判定 libc——`docker/toolkit/bin/npm` 已是此写法

- **WD-PLAT-004**：平台信息必须由探测得出，不得读取镜像的自我声明。探测反映实际情况，声明可能失真。remote target 本就没有 contract.json，必须探测；native 与 remote 走同一条平台识别路径，少一处分叉。

探测不依赖 §13 的 contract 移除——阶段 2 就需要它，而 contract 移除在最后。

## 13. workspace contract 的收敛

### 13.1 决定

目标不变：最终彻底移除 `internal/workspace/contract.go` 及其全部校验，能力可用性完全由依赖管理器负责。**时机重排：从先行止血改为最后一步**，且有明确前提（§13.3）。

v3 的注释主张「不兼容必须以 contract 版本差呈现，而不是令人困惑的 missing-file 错误」。这个诉求是对的，层次是错的——整体判定只能说「镜像不兼容，请重建」；依赖级版本门说「Codex 需要 0.151.0，当前 0.147.0，点此更新」。后者严格更好，但只有 §9.2 版本门与 §8 discovery 落地后才存在。该分歧已决定：contract 直接移除，不设额外对齐门槛；移除 PR 的描述需引用本节说明版本门如何承接 v3 注释的诉求。

### 13.2 为什么可以最终移除

原有校验分三类，各有更好的归属。

**fatal 类是冗余的。** 容器 `Cmd` 就是 `tini -g -- /opt/memoh/bridge`（`internal/workspace/manager.go:619`）。缺少 tini 或 bridge 时容器根本无法启动，`WaitForWorkspaceReady`（以 `Stat("/")` 探活）会先失败并返回明确错误；contract 校验自身经 bridge client 执行，bridge 不可达时它根本执行不到。这部分断言从未真正生效。

**agent CLI 版本对齐由版本门承接。** contract v3 为此而生，但它以镜像为粒度：CLI 差一个版本 = 整个镜像判死 = 重建。版本门以依赖为粒度：差哪个补哪个，且给出可点的补救动作。

**其余全是可选能力。** node、python、uv、a11y-cli、display scripts 的缺失只影响特定功能，应当在使用点报错。那里的错误信息天然更具体——「Computer Use 需要 a11y-cli，当前 workspace 未提供」远比「workspace image is incompatible」有用。

因此不采用分级保留（fatal / degraded）方案：三类各有更好的归属，中间层没有独立价值。

### 13.3 移除前提与清单

前提（三条全部满足）：

1. §9.2 版本门在 direct runtime 启动路径生效；
2. §8 discovery 上线，「这个 workspace 实际有什么」有事实来源；
3. §9.3 / §9.4 的启用检查与运行时兜底上线，失败有补救路径。

移除清单：原逐行清单因 #1110–#1112 重写大量文件而过期，届时按 `ErrWorkspaceImageIncompatible`、`workspace-contract`、`CurrentWorkspaceContractVersion` 全库检索重新盘点。已知类别：`contract.go` 本体与测试；`CodeWorkspaceImageIncompatible` 错误码及 i18n（前后端）；`bots/service.go`、`handlers/containerd.go`、`handlers/users.go` 的错误分支；`WorkspaceInitPath` / `WorkspaceBridgePath` 迁出到容器 spec 文件（它们是启动参数不是契约）；`docker/workspace-contract.json` 与 Dockerfile COPY；CI 的 contract smoke test 与 path filter。

- **WD-CONTRACT-001**：不得以任何形式保留「镜像不兼容」这一整体判定。能力检查必须针对具体能力，并给出可操作的补救路径。

### 13.4 接受的代价

不兼容镜像的失败会推迟到实际使用点。这是明确接受的：失败信息更具体，指向具体缺失或版本不符的能力；discovery 承担「实际有什么」的回答，比一次性校验更贴近事实；用户可以安装或对齐缺失依赖，而不是被判为不兼容后无路可走。

## 14. 安全边界

- **WD-SEC-001**：首版仅支持内置 catalog。不做第三方 registry。
- **WD-SEC-002**：逃生舱做成「自定义安装命令」，并在 UI 明示这会在 workspace 内执行任意代码。
- **WD-SEC-003**：native 容器内 agent 本就可执行任意命令，执行内置脚本不增加风险。remote target 不同，见 §12.2。
- **WD-SEC-004**：脚本内容与凭据不进 argv（§5.1）。注意边界：native 容器内所有进程同 UID，`/proc/<pid>/environ` 仍可读，env 注入的 token 对容器内进程并非不可见（按 WD-SEC-003 这不是新增风险）；argv 收益的实质在 remote target 与进程列表、日志采集面。

## 15. 落地顺序

每一步均可独立验证，任一步中断都不会留下无法使用 agent 的坏状态。

| 阶段 | 内容 | 说明 |
| --- | --- | --- |
| 0 | rebase 前提：#1110–#1112 合入 | 本设计的 runtime 侧引用全部以合入后代码为准 |
| 1 | §5.2 `ExecStream.CloseSend` | 独立，单测覆盖 |
| 2 | catalog 加载 + runner + discovery + §12.4 平台探测 | 此时镜像仍内置 CLI，可对照验证；含 WD-CAT-004 钉版同源测试 |
| 3 | §7 数据库 + §11 API + SSE + rollback | |
| 4 | §9.2 launcher 解析与版本门 + §9.4 反馈码 + wrapper fallback 收敛 | 依赖阶段 2 的 discovery |
| 5 | §9.3 启用时阻塞检查 + 前端依赖面板 + bot agents API 字段 | |
| 6 | §10 更新检查（agent 类对比钉版；tool 类上游检查） | |
| 7 | 从 `docker/toolkit/install.sh` 移除 codex/claude 与 toolkit wrapper，镜像瘦身 | 一步删除，不保留种子副本；依赖阶段 4/5，否则新 workspace 无 CLI 可用。新镜像首次启用 direct agent 需下载 CLI，由 §9.3 的安装对话框承接 |
| 8 | §13 contract 移除 | 前提见 §13.3 |

上表是逻辑阶段；实际提交按 `workspace-dependencies-plan.md` 合并为 5 个代码 PR 叠成一个栈（阶段 1–2 → core，3 与 6 → service-api，4 → runtime，5 → web，7–8 → image-contract）。阶段 1 不依赖其余阶段。与旧版设计的关键差异：contract 移除从阶段 0 挪到阶段 8——在 direct 世界里它承担着 CLI 版本对齐的守门职责，替代机制（版本门 + discovery + 兜底）就位之前不能删。

## 16. 决策记录

| 议题 | 决定 | 理由 |
| --- | --- | --- |
| workspace contract 是否保留 | 最终移除，时机从先行改为最后 | fatal 类断言被容器启动路径覆盖；v3 新增的「CLI 版本对齐」职责由依赖级版本门承接后，整体判定失去存在理由。见 §13 |
| 版本信任如何维护 | **推翻旧决定：agent 类只安装钉版，解析以钉版优先，不符告知不拒绝** | 旧设计禁止版本信任（原 WD-ACP-005），依据是 ACP transcript 运行时校验兜底；direct 协议由钉版 CLI 的 schema 快照编译期生成，版本一致是运行前提而非预测。「支持 0.151.0」是本构建的实现事实。处置上沿用 runtime 既有的「告警不拒绝」（`appserver.go:111-119`）：硬拒绝会让每次 Server 升级后所有旧镜像 workspace 停摆，直到用户更新。见 §9.2 |
| agent 类版本归谁 | Server（协议快照） | 用户只决定装不装；「更新」= 对齐到钉版。快照同步本就发生在 Server 仓库（`mise run codex-schema-sync`），版本随 Server 发布是既成事实，本设计把「分发」从镜像链路挪到依赖管理器 |
| 钉版如何防漂移 | catalog pin 与 `VERSION.json` 快照用测试强制一致（WD-CAT-004） | 同一个版本号出现两处即会漂移，静态校验是 driver 装配校验（§9.1）的同一取向 |
| 依赖管理是否解释版本语义 | tool 类不解释；agent 类必须解释 | tool 类的兼容性断言是预测（维持旧决定）；agent 类的版本要求是 Server 实现事实，隐瞒它才是错误。见 §10.1 |
| rollback 是否独立动作 | 是，第六动作，纯数据操作无脚本 | 旧设计把「回退」当作补救承诺却未定义动作；回滚在「新版本坏了」时执行，不得依赖脚本、网络或上游 |
| toolkit wrapper 的 PATH fallback | 随版本门落地删除 | 不做版本检查的回落会绕过 WD-EXT-001，两套回落语义必然漂移。见 §9.2 |
| Hermes | 不进 catalog | 已随 #1112 从产品入口移除 |
| 镜像内 CLI 是否保留种子副本 | 不保留，一步删除 | 保留种子等于两套分发路径并存，toolkit 回落会掩盖依赖管理器的缺陷；首次启用的下载成本由安装对话框显式承接 |
| 版本探测方式 | 默认 `--version`，清单可选 `scripts.version` 覆盖 | 多数 CLI 的 `--version` 足够；输出格式特殊或无该参数的依赖不应迫使 discovery 内置解析特例。见 WD-CAT-005 |
| `requires` 是否带版本区间 | 不带 | 镜像提供的 node／python 版本由镜像自身保证，再加一层区间只增加维护面 |
| reinstall 是否独立脚本 | 否，默认由 runner 编排 | 见 §4.3 |
| tool 类更新检查在何处执行 | workspace 内 | 可复用脚本的镜像源配置，无需为各生态重新实现版本查询；代价是容器须运行中，与 WD-UPD-002 一致。agent 类不适用（无上游查询，见 §10.1） |
| 依赖是否共享挂载 | 否 | 见 §12.3 |
