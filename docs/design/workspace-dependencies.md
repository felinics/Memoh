# Workspace 依赖管理设计

## 1. 文档目的

1. 把「哪些 agent 可用」从 workspace 镜像内容变成可管理的运行时状态：agent CLI 与其他依赖同等管理——默认安装最新版、可指定版本、可查上游更新、可回滚；runtime 类依赖（node、python、uv）以「镜像底座＋可管理覆盖层」纳入同一套管理；tool 类依赖由用户自由管理。
2. 明确 catalog、数据库、workspace 三者各自是什么的真相源，避免三处状态互相漂移。
3. 给出 direct runtime 启动路径、workspace contract 与依赖管理之间的边界——contract v3 目前用「镜像整体判定」承担的「CLI 是否可用」职责，由依赖级 discovery 与使用点报错取代。

本文基于 PR #1110–#1112（External Agent Driver 架构、direct Codex、direct Claude Code）合入后的代码形态。引用这三个 PR 分支的行号在其合入后需校对；其余引用以当前 `main` 为准。

本文中「必须」表示合入条件，「可以」表示实现自行选择但结果必须可观察。

## 2. 问题边界

### 2.1 现状（direct runtime 世界）

#1110–#1112 用 direct runtime 取代了内置 ACP：Server 不再通过 ACP wrapper 与 agent 通信，而是把 CLI 的原生协议编译进自己。

- **协议是编译期耦合。** Codex 侧的 Go 协议代码由 CLI 0.151.0 的 schema 快照生成（`internal/agent/runtime/codex/protocolgen/schema/VERSION.json`，注明「diff 即表示钉住的二进制与快照不一致」）；Claude Code 侧钉住 CLI 2.1.250（`internal/agent/runtime/claudecode/protocolref/VERSION.json`），升级需人工对协议。**协议快照同步是 Server 代码变更**——但它只决定 Server 会说哪一版协议，不决定 workspace 里装哪一版 CLI；本设计不把二者绑定（§2.2）。
- **launcher 硬编码。** `internal/agent/runtime/codex/config.go:27` 钉死 `/opt/memoh/toolkit/bin/codex`，`internal/agent/runtime/claudecode/process.go:15` 钉死 `/opt/memoh/toolkit/bin/claude`。不走 PATH，不做解析。
- **contract v3 把「CLI 在不在、是哪一版」做成了镜像整体判定。** `internal/workspace/contract.go` 把 `bin/codex`、`bin/claude` 列为必需可执行文件，并靠抬升 `contract_version` 让旧镜像以「版本不匹配」而非「文件缺失」暴露。`docker/toolkit/install.sh` 钉 `CODEX_VERSION=0.151.0`、`CLAUDE_CODE_VERSION=2.1.250`。
- **版本不符目前只告警不拒绝。** Codex 握手后比较 CLI 版本与 `protocol.PinnedCodexVersion`，不符时 `Warn` 并继续服务（`internal/agent/runtime/codex/appserver.go:111-119`，注释明言「drifted binary usually still speaks a compatible superset … warn loudly instead of refusing service」）；Claude Code 在 system 消息里同样只告警（`internal/agent/runtime/claudecode/turn.go:160-163`）。协议解码对未知字段与方法容错。
- **通用 ACP 保留**，作为自定义 Agent 通道：命令由用户自管，解析为 PATH → toolkit 回落（`internal/agent/runtime/acp/client/process.go`）。旧的 pinned toolkit adapter 机制已随内置 ACP profile 一并移除。
- toolkit launcher wrapper 自带 PATH fallback（`docker/toolkit/bin/codex` 的 `fallback_codex`）：toolkit 自己的拷贝缺失时会执行 PATH 上的同名命令，且不做版本检查。

由此产生的问题：

1. **CLI 换版本 = 重建并重新分发镜像。** 用户想用 0.152 的 Codex，必须等镜像发布链路走完并重建 workspace；镜像里写死的版本让 CLI 与镜像同寿命。
2. **contract 版本随之抬升 = 判所有存量镜像不兼容。** #1110 风险栏自认「workspace contract 升级到 v3，旧 workspace image 需要重建」。这正是引入依赖管理要消灭的判决，如今镜像内 CLI 每换一次版本就重演一次。
3. **`reconcileNativeWorkspace` 周期性放大判决。** 它在 `internal/workspace/manager_lifecycle.go:600` 调用 `InitializeNativeWorkspace`，后者做完整 contract 校验；Server 每次启动 reconcile 都会把旧镜像的 workspace 标记为 setup failure。
4. **remote target 无法运行 direct runtime。** launcher 硬编码 `/opt/memoh/toolkit`，用户真机上没有这个路径。

### 2.2 目标

- **镜像与 agent CLI 解耦。** CLI 由依赖管理器在 workspace 内安装、更新、回滚，不再随镜像发布；镜像最终不内置 CLI（§15）。
- **agent CLI 与其他依赖同等管理。** 安装默认最新版，也可指定版本；可查上游更新；可回滚。依赖管理器不为 agent CLI 设推荐版本、不做版本门；Server 协议快照版本与依赖管理无关——runtime 握手对版本差异的告警照旧，那是 runtime 自己的事。
- **runtime 类依赖是「镜像底座＋可管理覆盖层」。** node、python、uv 由镜像底座保证始终可用；用户可以在其上安装一个受管理的覆盖版本并升级、回滚，「卸载」即移除覆盖层、回到镜像版本。npm 随 node 提供，不单列。
- **tool 类依赖按通用包管理。** 后续通用工具由用户通过 UI 安装、更新、卸载。
- 启用 agent 时阻塞检查依赖，缺失则询问用户是否安装。
- 运行时缺失时不阻塞，给稳定反馈码与可执行的补救动作。
- 定期检查更新：所有配置了 `check_update` 且未锁定版本的 managed 依赖统一查上游。

### 2.3 非目标

- 不做第三方依赖 registry。首版 catalog 只有内置条目。
- 不做 host 侧共享依赖挂载（见 §12.3）。
- 不改变 workspace 镜像的其余契约（bridge、tini、display）。
- 不为 agent CLI 维护推荐版本或兼容矩阵。CLI 与 Server 协议快照之间的版本差异由 runtime 握手告警呈现，依赖管理器不介入、不转述。
- 不承诺 direct runtime 在 remote target 可用。entrypoints 解析为其扫清了路径障碍（§9.2 附带收益），但 remote 支持是 runtime 侧的独立决策。
- Hermes 已随 #1112 从产品入口移除，不进 catalog。

## 3. 核心模型

三层，各自是不同东西的真相源。混层是本设计要防的主要失败模式。

| 层 | 内容 | 位置 | 是什么的真相源 |
| --- | --- | --- | --- |
| **Catalog** | 依赖定义、安装脚本 | Server 二进制（`//go:embed`） | 「系统支持哪些依赖，怎么装」 |
| **Installation** | bot × target × dep 的状态记录 | `bot_dependency_installations` 表 | 「用户想要什么」 |
| **Workspace state** | 实际安装内容（镜像底座与 managed 覆盖层） | 容器内 `/opt/memoh/toolkit`（底座）与 `/data/.memoh/deps/`（managed） | 「实际有什么」 |

三条必须遵守的规则：

- **WD-MODEL-001**：Catalog 不落库。它是代码，随 Server 版本演进，需要与 driver 依赖声明一起做静态校验。
- **WD-MODEL-002**：数据库记录意图，不记录事实。容器重建、快照回滚、agent 自行安装都会让数据库与实际状态背离，数据库不得被当作可信来源。
- **WD-MODEL-003**：容器状态是最终事实。任何展示给用户的「已安装」都必须能追溯到一次 discovery，或一次刚完成的安装回执。

## 4. Catalog

### 4.1 位置与形式

`internal/workspacedeps/catalog/deps/<dep-id>/` 下每个依赖一个目录，与加载它的 Go 包同目录：

```
internal/workspacedeps/catalog/
├── catalog.go                   # //go:embed deps
└── deps/
    ├── node/
    │   ├── dependency.yaml      # source: image，镜像底座；脚本安装覆盖层
    │   ├── install.sh
    │   ├── update.sh
    │   └── remove.sh            # 移除覆盖层，回到镜像版本
    ├── codex/
    │   ├── dependency.yaml      # category: agent，与其他 managed 依赖同等管理
    │   ├── install.sh
    │   ├── update.sh
    │   ├── remove.sh
    │   └── check-update.sh      # npm view 取 latest
    └── claude-code/
        └── ...
```

整个 `deps/` 目录以 `//go:embed` 编进 Server 二进制。`//go:embed` 不能引用包目录之外的路径，所以 catalog 不放在 `conf/`——那里是运行时读取的部署配置，语义也不同。

**为什么 embed 而不是像 `conf/providers/` 那样运行时读目录**：provider YAML 是纯数据，依赖脚本是将在 workspace 内以 root 执行的代码。运行时可改等于多一个篡改面。embed 同时保证 `manifest_digest` 稳定、部署不会漏文件。可以提供一个默认关闭的 override 目录作为调试逃生舱。

改脚本需要发 Server 版本。脚本变更远少于依赖自身的版本变更——版本由脚本在运行时向上游查询或由用户指定，不写死在脚本里；这仍比「重建 workspace 镜像 + 全体用户重新拉取」轻两个数量级。

### 4.2 清单格式

```yaml
# internal/workspacedeps/catalog/deps/codex/dependency.yaml
id: codex
name: Codex
category: agent                  # agent | runtime | tool；仅供 catalog 校验与后端逻辑，不在 UI 分组
icon: openai                     # icon 库标识（@memohai/icon 组件名的 kebab-case），与 provider 一致
description: OpenAI Codex CLI（direct runtime）

source: managed                  # managed | image
requires: [node]
provides:                        # 安装后必须可解析的命令
  - codex

platforms:
  - { os: linux,  arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64] }

# version:
#   pin: "0.151.0"               # 任何依赖都可选：锁定版本、不参与更新检查。内置条目一律不 pin

timeouts:
  install: 1200                  # 与镜像 MEMOH_APT_COMMAND_TIMEOUT 一致
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
  check_update: check-update.sh  # 可选：查上游 latest；配置后进入 §10 的周期检查
```

任何 managed 条目都可以配置 `check_update` 脚本走上游查询，agent 类（codex、claude-code）用 `npm view <pkg> version` 取 latest，与 tool 类同等；`version.pin` 对任何依赖都可选，非空则锁定版本、不参与更新检查，内置条目一律不 pin。

展示类字段的语义：

- `icon` 是 icon 库标识——`@memohai/icon` 组件名的 kebab-case（`openai`、`anthropic`、`nodejs`、`python`、`uv`），与 LLM provider 的 `icon` 字段是同一套标识。SVG 进 `packages/icons`，前端按标识映射组件；不接受 URL 或内联 SVG。标识与依赖 `id` 独立（`node` 条目的图标是 `nodejs`）。首版需补 `nodejs`／`python`／`uv` 三个图标，`openai`／`anthropic` 已在库中。
- `category` 仅供 catalog 校验与后端逻辑（driver 依赖校验、日志、测试断言），不在 UI 分组——依赖面板平铺（§9.5）。
- `name`／`description` 是回退文案。前端按依赖 `id` 走 i18n，只对 i18n 中不存在的 id 显示 catalog 文本（WD-UI-002）。

- **WD-CAT-001**：`source: image` 表示镜像提供该依赖的底座（`/opt/memoh/toolkit`），底座不可删除、不由依赖管理器变更。条目可以带 `scripts`，此时脚本安装的是覆盖层——装到 `/data/.memoh/deps/<id>/versions/<v>`、经 `dep_switch` 切 `current`，通过 §6.1 的 PATH 优先级盖过底座；覆盖层支持 update 与 rollback，`remove` 只移除覆盖层、回到镜像版本。无脚本的 `source: image` 条目只展示，不可安装。node、python、uv 首版均带脚本；npm 随 node 提供，不单列条目。
- **WD-CAT-002**：`platforms` 用于准入。不匹配的平台必须在 API 层拒绝并在 UI 置灰，不得依赖脚本自行报错。
- **WD-CAT-003**：脚本必须是 POSIX `sh`，不得使用 bash 扩展或 GNU 专有的命令行为。remote target 的对端不保证是 Debian，甚至不保证是 Linux。此约束同样适用于 runner 注入的 prelude（§5.3）。
- **WD-CAT-005**：版本探测默认为 `<候选路径> --version`，取输出中首个 `\d+\.\d+\.\d+` 形态的 token。`--version` 不可用或输出格式特殊的依赖提供可选的 `scripts.version`，经 runner 执行，候选路径由 `MEMOH_DEP_CANDIDATE` 传入，结果写 `$MEMOH_DEP_RESULT`（`{"version":"..."}`）。两种方式都只在 discovery 内使用并进 §8.5 缓存。对 `source: image` 条目，discovery 对 toolkit 副本与覆盖层副本分别探测，两者版本都进缓存。

### 4.3 六个动作

UI 暴露 install / update / reinstall / remove / rollback 五个通用动作；配置了 `check_update` 的依赖额外有 check-update。install／update／reinstall 都接受可选的目标版本，省略即最新（§11）。

对 `source: image` 的底座依赖（node、python、uv），五个动作全部作用于覆盖层：`install` 装覆盖层，`update`／`rollback` 在覆盖层版本间切换，`remove` 删除覆盖层整目录后回到镜像版本——底座本身没有任何动作。

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
| `MEMOH_DEP_VERSION` | 目标版本；空或 `latest` 表示最新。脚本必须把实际安装的版本写回 `$MEMOH_DEP_RESULT` 的 `version`，Server 只信回执不信请求 |
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
// check-update（配置了 check_update 的依赖，含 agent 类）
{"installed":"1.7.1","latest":"1.8.0","update_available":true}

// install / update
{"version":"0.151.0",
 "entrypoints":{"codex":"/data/.memoh/deps/codex/current/bin/codex"}}
```

`entrypoints` 是关键回执。Server 据此建立 shim 并记录绝对路径，direct runtime 的 launcher 解析（§9.2）与通用 ACP 都不必依赖 PATH 顺序。

## 6. Workspace 内布局

```
/data/.memoh/deps/
├── bin/                        # shim，PATH 中排在 toolkit 之前（§6.1）
├── node/                       # source: image 条目的覆盖层，布局与 managed 相同
│   ├── state.json
│   ├── current -> versions/24.16.0
│   └── versions/…
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

### 6.1 PATH 优先级

覆盖层要盖过镜像底座，唯一的机制是 PATH 顺序：`/data/.memoh/deps/bin` 必须排在 `/opt/memoh/toolkit/bin` 之前。这不只服务 agent 在终端里敲 `node`——codex／claude 的入口是 `#!/usr/bin/env node` 脚本，它们跑在哪个 node 上同样由这条 PATH 决定。

PATH 的构造点有三处，全部前置：

1. **bridge exec 环境**（`internal/workspace/bridgesvc/server.go` 的 `execEnv`／`execPTYEnv`）：bridge 统一把 `/data/.memoh/deps/bin` 前置到子进程 PATH——目录存在才前置，不存在时 PATH 保持原样。所有经 bridge 执行的命令（agent 工具的 exec、终端、依赖脚本自身）都从这里进入，这是唯一需要感知目录是否存在的位置。
2. **direct runtime**：`internal/agent/runtime/codex/process.go:12` 与 `claudecode/process.go:14` 硬编码的 `containerPath` 同步前置 `/data/.memoh/deps/bin`。
3. **通用 ACP**：`internal/agent/runtime/acp/client/process.go:20` 的 `defaultContainerPath` 同步前置。

- **WD-FS-003**：三处 PATH 必须以 `/data/.memoh/deps/bin` 开头，顺序为 managed → toolkit → 系统路径。这是 `source: image` 覆盖层生效的前提；缺任何一处，用户装了新 node 而 agent 仍跑在旧 node 上，且没有任何报错。direct runtime 与通用 ACP 通过 entrypoints 绝对路径找 launcher 本体，不依赖 PATH；但 CLI 内部再起的 node／python 子进程走的是这条 PATH。

`MEMOH_DEP_BIN`（§5.4）与此处是同一个目录。remote target 的目录位置随 `MEMOH_DEP_HOME` 按 target 计算而不同，首版只要求 native 生效。

## 7. 数据库

```sql
CREATE TABLE IF NOT EXISTS public.bot_dependency_installations (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id              UUID        NOT NULL,
    workspace_target_id TEXT        NOT NULL,
    dependency_id       TEXT        NOT NULL,
    source              TEXT        NOT NULL,   -- image | managed，按当前生效副本记录（底座依赖装了覆盖层即 managed）
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

版本语义说明：`missing` 后的「重装」与 botbackup 导入按 `installed_version` 安装，对所有依赖一致；rollback 依据 `state.json` 的 `previous_version`，不需要额外列。

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

每个依赖的 discovery 都探测版本：`state.json` 直接记录；toolkit 或 PATH 副本按 WD-CAT-005 探测——默认 `<候选路径> --version`，清单设置了 `scripts.version` 时改走该脚本。对 `source: image` 的底座依赖，`Observed` 同时给出镜像版本（toolkit 副本）与覆盖层版本（`state.json`），当前生效副本按 §6.1 的 PATH 优先级判定——有覆盖层即覆盖层。结果进 §8.5 缓存，供面板展示与 launcher 解析（§9.2）使用。

| 数据库 | workspace | 处理 |
| --- | --- | --- |
| 有 | 有 | `installed`，用实际版本校正 `installed_version` |
| 有 | 无 | `missing`，UI 显示「待重装」 |
| 无 | 有 | 采纳：补一条 `installed` 记录，`source` 按实际提供方记（toolkit/镜像内容 → `image`，其余 → `managed`） |

- **WD-STATE-003**：第三态（采纳）不得省略。agent 在 workspace 内本就能自行安装，快照回滚也会带回未记录的依赖。不承认这个现实会让面板长期与实际不符。

### 8.3 各场景下的表现

| 场景 | `/data` | 结果 |
| --- | --- | --- |
| 重建，`preserve_data=true` | 导出后恢复（`internal/workspace/dataio.go:217`） | 记录有效；镜像可能已换，底座依赖的镜像版本需重新探测（覆盖层随 `/data` 保留） |
| 重建，`preserve_data=false` | 丢失 | 全部 `managed` 项转 `missing` |
| `RollbackVersion` | 回到历史时点 | deps 一并回退，必须由 discovery 校正 |
| botbackup 导入 | 新容器 | 见下 |

- **WD-STATE-004**：botbackup 只备份依赖清单（`dependency_id` + `version`），不备份二进制内容。二进制跨平台不可移植，且会让备份包膨胀到数百 MB。导入后走一次批量安装，按备份中的版本号安装，对所有依赖一致；版本在上游已不可得时该项 `failed`，用户可改装最新。

### 8.4 并发

- 按 `(bot_id, workspace_target_id, dependency_id)` 互斥。
- 内存锁为主，数据库中间态为兜底。
- workspace 内 `.locks/<id>.lock` 防御 Server 多实例。

### 8.5 缓存

在 workspace Manager 上维护 per-`(bot, target)` 的依赖状态缓存（含版本探测结果），容器重启或重建时失效（`m.grpcPool.Remove(botID)` 的各调用点是天然失效点）。

**理由**：命令解析在 session 启动路径上。运行时探测必须走缓存，未命中才读一次 `state.json`（单次 `ReadRaw`，远低于 exec 开销）；`--version` 探测同理。

## 9. External Agent Runtime 集成

### 9.1 Driver 声明依赖

`external.Driver`（`internal/agent/runtime/external/external.go`）增加可选接口：

```go
// DependencyRequirer is implemented by drivers whose CLI is provisioned as a
// managed workspace dependency. It names the dependency only; which version is
// installed is the user's call and is never checked here.
type DependencyRequirer interface {
	RequiredDependency() (depID string)
}
```

- codex driver 返回 `"codex"`，claudecode driver 返回 `"claude-code"`。driver 不声明版本——协议快照版本（`protocolgen/schema/VERSION.json`、`protocolref/VERSION.json`）是 runtime 的实现细节，依赖管理不引用它。
- 装配点 `provideDirectAgentDrivers`（`cmd/internal/core/providers.go:611`）在启动时校验：声明的 dep 存在于 catalog、其 `provides` 包含 launcher 命令。校验失败即 panic——「driver 要 codex，catalog 里叫 openai-codex」这类漂移在 Server 启动时暴露，不拖到用户点安装时。（内置 ACP profile 的 `Register` panic 校验已随 #1110 移除，这里是它的继任者。）
- 通用 ACP driver 不实现该接口：命令由用户自管，解析保持 PATH → toolkit 回落，跳过全部依赖检查。

### 9.2 launcher 解析

**这是本设计对 runtime 侧唯一的硬性改动。**

现状：direct runtime 硬编码 launcher（`codex/config.go:27`、`claudecode/process.go:15`），CLI 是否可用完全靠「contract v3 镜像 + install.sh 内置」这一条链路保证。

改造为按来源优先级解析，不做任何版本判断：

1. `/data/.memoh/deps/<id>/state.json` 的 `entrypoints`（managed 副本）；
2. toolkit 路径 `/opt/memoh/toolkit/bin/<cli>`（镜像内置副本；§15 移除镜像内 CLI 后此环为空）；
3. PATH 上的同名命令（`command -v`，agent 在 workspace 内自行 `npm i -g` 的副本）；
4. 无任何副本 → `agent_dependency_missing`，不启动。

- **WD-EXT-001**：launcher 解析只按来源优先级 managed → toolkit → PATH 取第一个存在的副本，不做版本判断、不做版本门。CLI 与 Server 协议快照之间的版本差异由 runtime 握手自行告警（§2.1，`appserver.go:111-119`、`turn.go:160-163`），那是 runtime 的实现细节，依赖管理器不介入、不转述、不设推荐版本。硬失败只用于「无任何副本」。解析走 §8.5 缓存，禁止在 runtime 内重复探测；握手上报的实际版本可以回写缓存校正显示，但不参与解析。

这与旧设计「不做任何版本信任」（原 WD-ACP-005）取向一致，且更彻底：旧设计仍保留 toolkit wrapper 的隐式回落，本设计把三处来源显式排序、由 discovery 统一提供。

配套收敛：toolkit launcher wrapper 的 PATH fallback（`docker/toolkit/bin/codex` 的 `fallback_codex`）在本节落地后删除。它是第二套解析逻辑，与本节的来源顺序必然漂移；解析只能有一处。

附带收益：launcher 不再绑定 `/opt/memoh/toolkit`，remote target 运行 direct runtime 的路径障碍消除（是否支持见 §2.3）。

### 9.3 启用时的阻塞检查

入口是 `apps/web/src/pages/bots/components/bot-agents.vue:399` 的 `setAgentEnabled()` 与 `add-bot-agent-dialog.vue` 的创建流程。

流程：

1. 用户启用（或新建并启用）一个 direct agent。
2. 前端调用 `POST /bots/{bot_id}/dependencies/preflight`，**阻塞等待**。每个依赖只返回 `satisfied`／`missing`／`platform_unsupported`／`unknown_dependency` 之一，不含任何版本判断。
3. `satisfied`（任一来源存在副本）→ 正常写入 `enabled: true`。
4. `missing` → 弹出确认对话框，只有「安装」一种文案：「启用 Codex 需要在工作区安装 Codex（最新版，约 120 MB），是否现在安装？」可展开指定版本。`platform_unsupported`／`unknown_dependency` 直接报错，不给安装入口。
5. 用户确认 → 打开安装进度对话框（SSE 日志流），完成后自动写入 `enabled: true`。
6. 用户取消 → 开关回弹，不写入。

- **WD-EXT-002**：preflight 未通过且用户未确认安装时，不得写入 `enabled: true`。开关状态必须回滚到启用前。
- **WD-EXT-003**：安装失败时开关必须回弹，错误在对话框内展示，并保留完整日志供复制。
- **WD-EXT-004**：workspace 未运行或未创建时，preflight 不得自行拉起容器，返回 `workspace_not_running` / `workspace_missing`，UI 呈现「启动工作区」/「创建工作区」引导。install 动作对 native target 可以自动 `EnsureNativeRunning`——用户已显式点了安装；remote target 必须在线，离线即拒绝。

配套 API 面：bot agents API 必须暴露 `dependency_id`（来自 driver 声明），前端才能发起 preflight。SDK 随 swagger 再生成。

### 9.4 运行时兜底

配置时已满足的依赖仍可能在运行时缺失（容器以 `preserve_data=false` 重建、快照回滚、用户手动卸载）。

session 启动前做一次廉价探测（走 §8.5 缓存）。不满足时**不阻塞**，返回稳定反馈：

```go
CodeAgentDependencyMissing = "agent_dependency_missing"
```

`missing` 阻止本次启动，携带 `dep_id` 与 `install_task_id`，前端据此渲染进度与取消按钮。只有这一个反馈码：版本差异不是依赖管理器的判断对象（WD-EXT-001）。风格与 `internal/agent/decision/feedback/` 的既有稳定反馈一致（该包在 #1110 中已按 External Agent 语义重构，落地时挂到合入后的常量表）。

同时把安装投递到 `internal/agent/background/`，用户消息侧收到：

> 工作区中没有 Codex，正在后台安装最新版（约 1-2 分钟），完成后请重发消息。

- **WD-EXT-005**：运行时路径不得阻塞式安装。用户发送消息期待的是秒级响应，卡住数分钟且无解释比报错更差；安装需要联网、会失败、多个 session 会并发触发同一依赖。
- **WD-EXT-006**：通用 ACP 路径的 `commandResolveWindow`（5 秒）不得用于等待安装。它的语义是等待刚写入的文件可见，不是等待长任务。
- **WD-EXT-007**：因「无副本」拒绝启动、以及会话恢复因 CLI 已换版本而失败时，错误必须给出稳定错误码与用户可读文案，并列出可执行动作——「安装」或「回滚到上一版」必须是真能点的按钮，不是一句安慰。`versions/` 保留上一版（WD-FS-001）正是为此。会话恢复侧的具体挂点在 #1110 重构后的 checkpoint 体系（`agent_session_*` 表、各 runtime 的 `checkpoint.go`），以合入后代码为准。

需要全自动时，做成 bot 设置项「依赖缺失时自动安装并等待」，默认关闭。阻塞必须是用户的显式选择。

### 9.5 面板与 Supermarket

依赖的用户界面分两处，职责不重叠：

| 位置 | 职责 | 数据来源 |
| --- | --- | --- |
| Bot 详情页「依赖」tab | 管理**已安装**的依赖：状态、版本、更新／重装／卸载／回滚／查看脚本 | `GET /bots/{bot_id}/dependencies`，前端只保留有安装记录或 discovery 探测到副本的条目（含镜像底座、`failed`／`missing` 记录） |
| Supermarket「依赖」tab | **发现与安装**：列出 catalog 全部可安装依赖 | `GET /workspace-dependencies/catalog`，与 bot 无关 |

- **Bot 页平铺，不按 `category` 分组。** 按本地化名称排序，需要处理的条目（`failed`、`missing`、有更新、进行中）排前。catalog 里未安装的条目不出现；列表为空时空态引导去 Supermarket。`GET /bots/{bot_id}/dependencies` 本身仍返回 catalog ∪ 状态（§11）——过滤在前端做，Supermarket 选定 Bot 后复用同一接口取 `platform_supported` 与当前状态。
- **「依赖」tab 位于 bot 详情页的 capability 组，MCP 之后**，不在 runtime 组：依赖决定 bot 能做什么，与 Skills、MCP 同类；容器、网络、资源限制才是 runtime。
- **Supermarket 负责发现与安装。** 条目点击后选择 Bot（与 workspace target）、可选填版本，调用 `POST /bots/{bot_id}/dependencies/{dep_id}/install`，走与 Bot 页同一条 SSE 进度流；成功后引导到该 Bot 的依赖 tab。`platforms` 不匹配（WD-CAT-002）与不可安装条目（无脚本的 `source: image`）在选定 Bot 后置灰；已安装的条目直接给「去依赖页」。
- **底座与 managed 条目同形。** 未装覆盖层的 node、python、uv 与其他条目是同一行结构、同一组动作位置，差别只体现在动作集——底座没有卸载与回滚（没有覆盖层可移除）；装了覆盖层后与其他条目完全一致。`image_version`／`overlay` 字段只用于决定动作集与确认框的事实陈述，不渲染为标签。

规范：

- **WD-UI-001**：同类条目同形同动作集。不得用标签、徽标或描述解释条目「来自哪里」（镜像／managed／PATH）或「不能做什么」；限制通过动作集自然表达——不可用的动作不出现，而不是出现后附一句说明。确认框只陈述事实结果（如「将移除 24.20.0，工作区回到 24.14.0」），不解释机制。
- **WD-UI-002**：面向用户的依赖名称与描述走 i18n，按依赖 `id` 取本地化文案；仅对 i18n 中不存在的 id 回退到 catalog 的 `name`／`description`。catalog 文案是回退，不是 UI 真相源。
- **WD-UI-003**：安装／更新／重装对话框提供可选的版本输入（留空＝最新），不展示由后端决定的「目标版本」——请求发出前前端不知道也不猜最新版本是什么；实际版本以 SSE `done` 事件的回执为准（§11）。

## 10. 更新检查

### 10.1 统一上游检查

所有配置了 `check_update` 且 `version.pin` 为空的 managed 依赖——agent 类的 codex／claude-code 与 tool 类同等，`source: image` 上已装的覆盖层也算——走同一个周期 worker。agent 类的 `check-update.sh` 用 `npm view <pkg> version` 取 latest；不与 Server 协议快照比较，不存在「需要更新到某个特定版本」的集合。

当前没有 Server 级的周期任务框架，`ReconcileContainers` 是 FX `OnStart` 时执行一次（`cmd/internal/core/providers.go`）。需要新增一个轻量周期 worker。

- **WD-UPD-001**：默认每 24 小时一轮，仅对 `status = installed`、配置了 `check_update` 且 `version.pin` 为空的 managed 条目执行，不按 `category` 区分。
- **WD-UPD-002**：只检查处于运行状态的 native workspace。停止的容器跳过，remote target 跳过（对端是用户设备，不应被后台唤醒）。
- **WD-UPD-003**：同一依赖的检查结果按 `(dependency_id, target 平台)` 在 team 内共享缓存。10 个 bot 都装了同一工具时，不得产生 10 次上游查询。
- **WD-UPD-004**：检查失败只写 `last_error` 与 `last_checked_at`，不改变 `status`。网络抖动不得让已装依赖显示为异常。
- **WD-UPD-005**：不得自动更新。更新可能引入行为变化，用户需要知道自己动了什么。
- **WD-UPD-006**：更新确认对话框只呈现版本对比（当前 → 最新）与回滚保证，不对目标版本的兼容性做任何断言——对所有依赖一致，agent 类也不例外。CLI 与 Server 协议快照的关系由 runtime 握手告警呈现，不在这里预告。

结果写入 `latest_version` 与 `last_checked_at`。提醒对话框每个 `(bot, dep, latest_version)` 组合最多弹一次，用户忽略后不再重复，直到出现更新的版本。

### 10.2 手动刷新

面板提供刷新按钮：触发一次即时 check-update 绕过 TTL，并重跑 discovery。

## 11. API

```
GET    /bots/{bot_id}/dependencies
       ?workspace_target_id=  列出 catalog ∪ 安装状态（含 image 底座项）

POST   /bots/{bot_id}/dependencies/preflight
       body: {dependency_ids: [...]}      启用 agent 前的阻塞检查
       响应含 workspace 可用性状态（WD-EXT-004）

POST   /bots/{bot_id}/dependencies/{dep_id}/install     SSE   body 可选 {"version": "..."}，省略为最新
POST   /bots/{bot_id}/dependencies/{dep_id}/update      SSE   同上
POST   /bots/{bot_id}/dependencies/{dep_id}/reinstall   SSE   同上
POST   /bots/{bot_id}/dependencies/{dep_id}/rollback    同步（纯数据操作，无日志流）
DELETE /bots/{bot_id}/dependencies/{dep_id}             SSE

POST   /bots/{bot_id}/dependencies/check-updates        手动刷新，同步
GET    /bots/{bot_id}/dependencies/{dep_id}/script      查看待执行脚本

GET    /workspace-dependencies/catalog                   与 bot 无关：catalog 全部条目，供 Supermarket「依赖」tab（§9.5）
```

列表项字段（节选）：`id`、`category`、`source`、`status`、`installed_version`、`latest_version`、`platform_supported`，以及两个只对底座依赖有意义的字段——`image_version`（镜像底座的版本，非 `source: image` 条目为空）与 `overlay`（当前生效副本是否为 managed 覆盖层）。不含任何「要求版本」字段。列表返回 catalog ∪ 状态的全集；「只显示已安装」是 Bot 页的前端过滤（§9.5），接口不带 `installed_only` 之类参数。

`GET /workspace-dependencies/catalog` 条目字段：`id`、`name`、`description`、`icon`、`category`、`provides`、`platforms`、`installable`（有 `scripts.install`）、`has_image_baseline`（`source: image`）、`version_pin`（可选，非空即锁定版本）、`actions_supported`（该条目在 catalog 层面支持的动作集，如 `[install, update, reinstall, remove, rollback, check_update]`）。只要求登录，不需要 bot 权限；不含任何 bot 状态——平台准入与已安装判定在用户选定 Bot 后经 `GET /bots/{bot_id}/dependencies` 得出，install 接口仍按 WD-CAT-002 兜底拒绝。

SSE 事件序列复用容器创建的既有模式（`internal/handlers/containerd.go`）：

```
{"type":"started","dependency_id":"codex","requested_version":"latest"}
{"type":"log","stream":"stdout","data":"..."}
{"type":"done","version":"0.152.3","entrypoints":{...}}
{"type":"error","code":"...","message":"..."}
```

`started` 带的是请求值，`done` 才带脚本回执的实际版本（§5.4）。

- **WD-API-001**：必须提供「查看待执行脚本」接口。脚本不落盘，出问题时用户无法进入容器自行查看，这个入口是必需项而非增强。

流程完成后按仓库的 API 开发工作流执行 `mise run swagger-generate` 与 `mise run sdk-generate`（含 bot agents API 的 `dependency_id` 字段扩展）。

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

v3 的注释主张「不兼容必须以 contract 版本差呈现，而不是令人困惑的 missing-file 错误」。这个诉求是对的，层次是错的——整体判定只能说「镜像不兼容，请重建」；依赖级 discovery 与使用点报错说「工作区中没有 Codex，点此安装」。后者严格更好，但只有 §9.2 launcher 解析与 §8 discovery 落地后才存在。该分歧已决定：contract 直接移除，不设额外门槛；移除 PR 的描述需引用本节说明依赖级 discovery 与使用点报错如何承接 v3 注释的诉求。

### 13.2 为什么可以最终移除

原有校验分三类，各有更好的归属。

**fatal 类是冗余的。** 容器 `Cmd` 就是 `tini -g -- /opt/memoh/bridge`（`internal/workspace/manager.go:619`）。缺少 tini 或 bridge 时容器根本无法启动，`WaitForWorkspaceReady`（以 `Stat("/")` 探活）会先失败并返回明确错误；contract 校验自身经 bridge client 执行，bridge 不可达时它根本执行不到。这部分断言从未真正生效。

**agent CLI 的可用性由依赖级 discovery 与使用点报错承接。** contract v3 为此而生，但它以镜像为粒度：CLI 缺失或换版 = 整个镜像判死 = 重建。依赖管理以依赖为粒度：缺哪个装哪个，且给出可点的补救动作。

**其余全是可选能力。** node、python、uv、a11y-cli、display scripts 的缺失只影响特定功能，应当在使用点报错。那里的错误信息天然更具体——「Computer Use 需要 a11y-cli，当前 workspace 未提供」远比「workspace image is incompatible」有用。

因此不采用分级保留（fatal / degraded）方案：三类各有更好的归属，中间层没有独立价值。

### 13.3 移除前提与清单

前提（三条全部满足）：

1. §9.2 launcher 解析在 direct runtime 启动路径生效；
2. §8 discovery 上线，「这个 workspace 实际有什么」有事实来源；
3. §9.3 / §9.4 的启用检查与运行时兜底上线，失败有补救路径。

移除清单：原逐行清单因 #1110–#1112 重写大量文件而过期，届时按 `ErrWorkspaceImageIncompatible`、`workspace-contract`、`CurrentWorkspaceContractVersion` 全库检索重新盘点。已知类别：`contract.go` 本体与测试；`CodeWorkspaceImageIncompatible` 错误码及 i18n（前后端）；`bots/service.go`、`handlers/containerd.go`、`handlers/users.go` 的错误分支；`WorkspaceInitPath` / `WorkspaceBridgePath` 迁出到容器 spec 文件（它们是启动参数不是契约）；`docker/workspace-contract.json` 与 Dockerfile COPY；CI 的 contract smoke test 与 path filter。

- **WD-CONTRACT-001**：不得以任何形式保留「镜像不兼容」这一整体判定。能力检查必须针对具体能力，并给出可操作的补救路径。

### 13.4 接受的代价

不兼容镜像的失败会推迟到实际使用点。这是明确接受的：失败信息更具体，指向具体缺失的能力；discovery 承担「实际有什么」的回答，比一次性校验更贴近事实；用户可以安装缺失依赖，而不是被判为不兼容后无路可走。

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
| 2 | catalog 加载 + runner + discovery + §12.4 平台探测 | 此时镜像仍内置 CLI，可对照验证 |
| 3 | §7 数据库 + §11 API + SSE + rollback | |
| 4 | §9.2 launcher 解析 + §9.4 反馈码 + wrapper fallback 收敛 | 依赖阶段 2 的 discovery |
| 5 | §9.3 启用时阻塞检查 + 前端依赖面板 + bot agents API 字段 | 面板最终形态见 §9.5，在阶段 7 收敛 |
| 6 | §10 更新检查（统一上游检查 worker） | |
| 7 | 去钉版与覆盖层：catalog 去 `pin`、agent 类 `check_update` 脚本；node／python／uv 覆盖层脚本；API `version` 参数与 `image_version`／`overlay` 字段、`GET /workspace-dependencies/catalog`；§6.1 PATH 前置（bridge + 三处 `containerPath`）；前端按 §9.5 收敛——可选版本输入、平铺且只显示已安装、tab 挪到 capability 组、Supermarket「依赖」tab、依赖图标 | 阶段 2–6 中以早期形态合入的版本判断在此收敛；此后所有依赖同等管理 |
| 8 | 从 `docker/toolkit/install.sh` 移除 codex/claude 与 toolkit wrapper，镜像瘦身 | 一步删除，不保留种子副本；依赖阶段 4/5/7，否则新 workspace 无 CLI 可用。新镜像首次启用 direct agent 需下载 CLI（默认最新版），由 §9.3 的安装对话框承接 |
| 9 | §13 contract 移除 | 前提见 §13.3 |

上表是逻辑阶段；实际提交按 `workspace-dependencies-plan.md` 合并为 6 个代码 PR 叠成一个栈（阶段 1–2 → core，3 与 6 → service-api，4 → runtime，5 → web，7 → versions-overlays，8–9 → image-contract）。阶段 1 不依赖其余阶段。与旧版设计的关键差异：contract 移除从阶段 0 挪到最后——在 direct 世界里它承担着「CLI 在不在」的守门职责，替代机制（launcher 解析 + discovery + 兜底）就位之前不能删。

## 16. 决策记录

| 议题 | 决定 | 理由 |
| --- | --- | --- |
| workspace contract 是否保留 | 最终移除，时机从先行改为最后 | fatal 类断言被容器启动路径覆盖；v3 新增的「CLI 是否可用」职责由依赖级 discovery 与使用点报错承接后，整体判定失去存在理由。见 §13 |
| agent CLI 版本如何管理 | **不钉版、不设推荐版本，与其他依赖同等：默认最新、可指定版本、可查上游更新、可回滚（用户决定）** | Server 协议快照版本是 runtime 实现细节，握手告警照旧（`appserver.go:111-119`、`turn.go:160-163`）；依赖管理器不做任何版本门或推荐。维持旧设计「不做任何版本信任」（原 WD-ACP-005）的取向。见 §9.2、§10 |
| agent 类版本归谁 | 用户，与其他依赖一致 | 快照同步仍发生在 Server 仓库（`mise run codex-schema-sync`），但那只决定 Server 会说哪一版协议，不决定 workspace 装哪一版 CLI；二者差异由 runtime 握手告警呈现 |
| node／python／uv 如何建模 | 镜像底座＋可管理覆盖层 | 底座保证始终可用且不可删；覆盖层装在 `/data`，支持升级回滚，卸载即回到镜像版本。npm 随 node 提供不单列。见 WD-CAT-001 |
| 覆盖层如何生效 | PATH 优先级由 bridge 注入 | bridge exec 环境把 `/data/.memoh/deps/bin` 前置（目录存在才前置）；direct runtime 与通用 ACP 的 `containerPath` 同步前置。见 §6.1、WD-FS-003 |
| 依赖管理是否解释版本语义 | 否，对所有依赖一致 | 兼容性断言是预测；agent 类也不例外——CLI 与协议快照的关系由 runtime 握手告警呈现，不由依赖管理器预告。见 §10 |
| rollback 是否独立动作 | 是，第六动作，纯数据操作无脚本 | 旧设计把「回退」当作补救承诺却未定义动作；回滚在「新版本坏了」时执行，不得依赖脚本、网络或上游 |
| toolkit wrapper 的 PATH fallback | 随 launcher 解析落地删除 | 第二套解析逻辑与 §9.2 的来源顺序必然漂移；解析只能有一处 |
| Hermes | 不进 catalog | 已随 #1112 从产品入口移除 |
| 镜像内 CLI 是否保留种子副本 | 不保留，一步删除 | 保留种子等于两套分发路径并存，toolkit 回落会掩盖依赖管理器的缺陷；首次启用的下载成本由安装对话框显式承接 |
| 版本探测方式 | 默认 `--version`，清单可选 `scripts.version` 覆盖 | 多数 CLI 的 `--version` 足够；输出格式特殊或无该参数的依赖不应迫使 discovery 内置解析特例。见 WD-CAT-005 |
| `requires` 是否带版本区间 | 不带 | 底座版本由镜像保证，覆盖层版本由用户决定；再加一层区间只增加维护面，且会重新引入版本判断 |
| reinstall 是否独立脚本 | 否，默认由 runner 编排 | 见 §4.3 |
| 更新检查在何处执行 | workspace 内 | 可复用脚本的镜像源配置，无需为各生态重新实现版本查询；代价是容器须运行中，与 WD-UPD-002 一致。对所有依赖一致 |
| 依赖是否共享挂载 | 否 | 见 §12.3 |
| 依赖 tab 是否分类 | 不分类，平铺；按名称排序，需处理的排前 | 首版条目不足十个，分组只增加视觉层级；`category` 留给 catalog 校验与后端逻辑。见 §4.2、§9.5 |
| Bot 页显示范围与 Supermarket 分工 | Bot 页只显示已安装（含镜像底座与 `failed`／`missing` 记录），空态引导去 Supermarket；Supermarket 新增「依赖」tab，列出 catalog 全部可安装依赖并发起安装（`GET /workspace-dependencies/catalog`） | 管理与发现是两种任务；把未安装条目混进 Bot 页会让「已安装」列表被 catalog 淹没。接口仍返回全集，过滤在前端。见 §9.5、§11 |
| icon 来源 | icon 库标识（`@memohai/icon` 组件名的 kebab-case），与 LLM provider 一致；SVG 进 `packages/icons`，首版补 `nodejs`／`python`／`uv` | 复用既有 provider 图标管线与映射方式；catalog 不携带图片资源，也不引入 URL 图标的外链与尺寸问题。见 §4.2 |
| 「依赖」tab 所在分组 | capability 组，MCP 之后 | 依赖决定 bot 能做什么，与 Skills、MCP 同类；runtime 组是容器、网络、资源限制。见 §9.5 |
| UI 文案规则 | 同形同动作集、不贴来源标签、不写限制说明；名称与描述本地化、回退 catalog；版本输入可选、不展示后端决定的目标版本 | 「镜像自带」标签与「由 Workspace 镜像提供，不可卸载。」是把实现机制翻译给用户；限制由动作集表达、结果由确认框陈述即足够。见 WD-UI-001–003 |
