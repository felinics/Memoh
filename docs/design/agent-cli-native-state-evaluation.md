# Codex 原生状态目录与初始化实验

日期：2026-09-14。对应[实施计划](agent-cli-storage-and-single-install-plan.md) P0、§9.2 和 §15。

## 结论与本次实现边界

本轮保留 `CODEX_HOME=/data/.codex/agents/<bot_agent_id>`，不注入 `sqlite_home` 或 `log_dir`。可重新安装的 CLI 负载与这些状态目录分别管理。

实际运行 `codex-cli 0.154.0` 证明：

- `sqlite_home` 生效，并同时移动本次涉及的六类 SQLite 数据库及其 WAL/SHM。它包含 goal 等业务状态，不能作为可丢弃缓存。
- 仅设置 `log_dir` 不会移动 `logs_2.sqlite`。本次 app-server 流程没有向指定的 `log_dir` 写文件，不能把这个配置当作 app-server SQLite 日志优化。
- 保留 Home、把 SQLite 放进 rootfs 的方案，在删除并重建 rootfs 后丢失 goal。持久 sessions 能找回 thread，并不能补回 goal。
- 默认 SQLite 留在持久 Home 时，同样的 rootfs 重建实验能够读回 goal。两种方案的普通 `auth.json` 文件均仍在原 Home，未变成软链接。

新增工具为 [`scripts/bench-agent-storage.py`](../../scripts/bench-agent-storage.py)，其回归测试为 [`scripts/test_bench_agent_storage.py`](../../scripts/test_bench_agent_storage.py)。本报告不把本地 Docker 数据替代 E2B/NFS 的性能验收，也不把未调用模型的状态实验替代真实认证、对话及 token 刷新测试。

## 版本与官方能力

Memoh 的协议快照 `internal/agent/runtime/codex/protocolgen/schema/VERSION.json` 和 `PinnedCodexVersion` 均为 `0.154.0`。实验当天 npm 的 `@openai/codex` latest 也解析为 `0.154.0`，所以这里的项目目标版与 registry 当前版是同一个版本；没有虚构第二个“最新版”。本机现有 `0.153.4` 另做一次 fresh/warm Home 探针，能完成 initialize、创建 thread、设置 paused goal，但不据此宣称 Memoh 支持所有旧版本。

[官方配置参考](https://learn.chatgpt.com/docs/config-file/config-reference)及[官方 schema](https://learn.chatgpt.com/docs/config-schema.json)包含 `sqlite_home` 和 `log_dir`。[环境变量文档](https://learn.chatgpt.com/docs/config-file/environment-variables)说明 `CODEX_HOME` 管理配置、凭证与状态根，`sqlite_home` 配置优先于 `CODEX_SQLITE_HOME` 环境变量。官方 schema 为当前文档，不是带版本签名的 0.154.0 源码快照；本报告的版本判断以精确安装包、CLI `--version`、`config/read` 和实际写入为准。

安装包取自 OpenAI 发布的 npm 平台包：

```text
@openai/codex@0.154.0-linux-arm64
tarball bytes: 122610794
tarball SHA-256: a2315b5f64bfeaff79b71e0d35505ba8c22cc1e96cab9dc950b614c804105b24
platform subtree: vendor/aarch64-unknown-linux-musl
```

下载后验证包内容的 SHA-512 与 registry `dist.integrity` 完全相同。测试使用完整平台目录，包括 binary、code-mode host 和资源文件，没有只复制一个二进制后把资源缺失误判为存储问题。

## 实验环境与隔离

使用独立容器 `memoh-codex-native-eval-20260914`、独立卷 `memoh-codex-native-eval-data-20260914`。没有操作现有 Bot 或其卷，没有复制宿主机或 Bot 凭证。

| 项目 | 实际值 |
| --- | --- |
| 镜像 | `python:3.12-slim`，`sha256:229a2c5bfa27522db7815ea81f9bed70af17ccb9de9fc7ad142b1877b5830d36` |
| 系统 | Debian 13.6，Linux `6.12.67-linuxkit`，aarch64 |
| `/data` | Docker named volume，`ext4`，`rw,relatime` |
| `/local` | 容器 rootfs，overlay |
| Python | 容器 3.12；工具回归测试同时在宿主 Python 3.9 运行 |
| 初始化配置 | `features.goals=true`、文件凭证存储、`analytics.enabled=false` |
| 环境 | 新建 `HOME`、`CODEX_HOME`，不继承 API key、宿主 Codex 配置或 `CODEX_SQLITE_HOME` |

`/data` 和 rootfs 最终都落在同一 Docker Desktop VM 的磁盘上。这是路径与生命周期对照，不是远程 NFS 对照。未执行全局 `drop_caches`。每个 trial 的第一次启动使用新 Home，第二次重用该 Home；两者都可能命中二进制页缓存。

## 实际写入路径

`config/read` 返回了传入的 `sqlite_home` / `log_dir`。工具在 initialize 后和进程退出后分别枚举自身目录；另用 `strace -f -e trace=%file` 跟踪一个独立样本，补充捕获启动后被清理的临时文件。该 traced 样本不混入性能数据。

| 文件或目录 | 默认配置 | 仅 `log_dir=/local/...` | 同时设置 `sqlite_home=/local/...` |
| --- | --- | --- | --- |
| `state_5.sqlite` | Home | Home | sqlite_home |
| `goals_1.sqlite` | Home | Home | sqlite_home |
| `memories_1.sqlite` | Home | Home | sqlite_home |
| `queue_1.sqlite` | Home | Home | sqlite_home |
| `thread_history_1.sqlite` | Home | Home | sqlite_home |
| `logs_2.sqlite` | Home | Home | sqlite_home |
| 上述数据库的 `-wal` / `-shm` | 同数据库 | 同数据库 | 同数据库 |
| `sessions/.../rollout-*.jsonl` | Home | Home | Home |
| `skills/.system/**` | Home | Home | Home |
| `tmp/arg0/**` | Home | Home | Home |
| `.tmp/**`、writer locks | Home | Home | Home |
| `auth.json` 读取位置 | Home | Home | Home |
| 指定的 log_dir 内新文件 | 未指定 | 0 | 0 |

initialize 已创建 state/goals/memories/queue/logs 数据库；本次探针在创建 thread 后还观察到 thread_history 数据库。表格只描述被此次流程实际触及的数据库，不能证明 Codex 的所有可选功能永远只使用这六个文件。

`tmp/arg0` 在正常退出时会被清理，因此只看退出后的文件列表会漏掉它。追踪记录确认该路径在 Home 中创建。这也说明移动 SQLite 后仍有 Home 元数据开销，不能直接套用原提案“整个 Home 本地化”的速度。

权限补充：以 UID/GID `65534:65534` 运行，给该用户明确拥有的独立 Home、sqlite/log 目录，fresh/warm 两次均完成真实初始化、thread/start 和 goal/set。没有假定 workspace 用户恒为 1000，也没有证明所有用户可以写任意 `/opt` 或 `/var/lib` 路径。

## rootfs 删除后的状态对照

为每种放置策略建立两个独立 Agent Home，各自创建一个 thread 和内容不同的 paused goal。实验通过真实 app-server `thread/goal/get` 核对 objective，通过 `thread/read` 核对 thread ID。`auth.json` 使用没有账号或 token 的合成 `{}` 文件，仅验证普通文件的路径、类型与哈希持久性。

执行顺序：

1. 创建四组状态，记录 thread ID、objective、Home、SQLite 路径、合成 auth 文件 SHA-256。
2. 关闭各 app-server，重启相同容器，再次读回状态。
3. `docker pause`，确认 `State.Paused=true`，`docker unpause`，再启动探针读回状态。这一步检查容器暂停前后的存储保留，没有测正在生成的 turn 暂停。
4. 删除实验容器并由同一镜像新建，重新挂入同一个测试卷。旧容器 ID 为 `cfba90e9ea3e...`，新容器为 `40f125f1bd72...`。检查确认 `/local/codex154/bin/codex` 与 `/local/sqlite` 均不存在，`/data/codex154/bin/codex` 仍存在。
5. 从持久卷中的同版本 CLI 启动 app-server，读取原 thread/goal；没有恢复或复制已丢失的本地 SQLite。

| 生命周期 | 默认 SQLite 在持久 Home（2 个 Agent） | SQLite 在 `/local`（2 个 Agent） |
| --- | --- | --- |
| 创建后读回 | goal objective 全部匹配 | 全部匹配 |
| 相同容器重启后 | 全部匹配 | 全部匹配 |
| 容器 pause/unpause 后 | 全部匹配 | 全部匹配 |
| 删除 rootfs、保留卷后 | 全部匹配 | 两者的 goal 均为 `null` |
| 删除 rootfs 后 `thread/read` | 原 thread ID 都可读 | 原 thread ID 都可读 |
| 合成 auth 文件 | 所有阶段哈希不变、非软链接 | 所有阶段哈希不变、非软链接 |

两个 Agent 的路径和 goal 内容均独立，没有串读；这验证路径分配，不声称同一 Linux 用户下有额外的安全隔离。本次没有创建真实模型记忆或执行队列任务；它们的路径已经确认，内容恢复尚不能据此宣称通过。goal 丢失本身已经足以否决把整组 SQLite 当缓存。

## 初始化原始样本

最终工具共执行 6 组 × 3 trials × 2 次启动，36 次都匹配 initialize response ID。随后还执行 config/read、thread/start、paused goal/set，未提交 `turn/start`。表中单位为毫秒；p95 采用 nearest-rank，在每组仅 3 个样本时等于最大值，不作统计显著性判断。

| CLI 位置 / 配置 | fresh Home 原始样本 | fresh p50 / p95 | warm Home 原始样本 | warm p50 / p95 |
| --- | --- | --- | --- | --- |
| rootfs / 默认 | 87.541, 62.131, 61.896 | 62.131 / 87.541 | 28.192, 21.307, 20.432 | 21.307 / 28.192 |
| rootfs / log_dir | 60.829, 82.076, 91.865 | 82.076 / 91.865 | 23.328, 21.548, 20.727 | 21.548 / 23.328 |
| rootfs / sqlite_home + log_dir | 61.416, 62.399, 56.018 | 61.416 / 62.399 | 20.220, 20.591, 20.256 | 20.256 / 20.591 |
| 卷 / 默认 | 71.234, 66.195, 60.298 | 66.195 / 71.234 | 21.671, 21.506, 21.574 | 21.574 / 21.671 |
| 卷 / log_dir | 58.165, 61.653, 73.022 | 61.653 / 73.022 | 19.489, 21.751, 21.820 | 21.751 / 21.820 |
| 卷 / sqlite_home + log_dir | 117.132, 62.179, 68.660 | 68.660 / 117.132 | 27.530, 22.160, 21.174 | 22.160 / 27.530 |

最后一组的一个 fresh 样本在握手及状态探针成功后，关闭 stdin 等待 2 秒仍未退出，由测量器发送 SIGTERM；单独记录 cleanup=2.006 s、returncode=-15、forced_termination=true。该人为清理没有加入 initialize 时间。自然非零退出、JSON-RPC error、超时或提前 EOF 仍作为失败样本，不进入成功延迟分位数。

文件系统子命令另用 32 个 4 KiB 文件验证 create+逐文件 fsync、stat、delete：分别为 0.029772 s、0.000153 s、0.000483 s。它验证测量工具能够在指定文件系统执行与清理，不代表大规模 NFS 基准。

## 重复运行工具

在一个专用测试 sandbox 中准备精确版本 CLI 及两个已存在、可写的根目录。下列命令假设 sandbox 的 `/data` 是待测持久卷、`/local` 是已确认的本地挂载；工具会在根目录下生成随机专属子目录，默认仅删除这些新建子目录。

```sh
python3 scripts/bench-agent-storage.py \
  --workspace /data \
  --launcher /local/codex154/bin/codex \
  --trials 5 --warm-starts 1 --probe-state \
  --output /local/default.json

python3 scripts/bench-agent-storage.py \
  --workspace /data \
  --launcher /local/codex154/bin/codex \
  --sqlite-root /local --log-root /local \
  --trials 5 --warm-starts 1 --probe-state --keep-artifacts \
  --output /local/sqlite-experiment.json

python3 scripts/bench-agent-storage.py \
  --workspace /data \
  --launcher /data/codex154/bin/codex \
  --trials 5 --warm-starts 1 --filesystem-files 2000 \
  --output /local/volume.json
```

要重现状态丢失，从 `--keep-artifacts` 报告中的 `codex_home`、command、`state_probe.thread_start.thread_id` 定位刚创建的测试状态。关闭该探针后，用报告中的同一环境与命令重新初始化，发送：

```json
{"id":"goal-check","method":"thread/goal/get","params":{"threadId":"<recorded thread ID>"}}
{"id":"thread-check","method":"thread/read","params":{"threadId":"<recorded thread ID>","includeTurns":true}}
```

记录成功结果，再销毁**这个专用 sandbox 的 rootfs**、挂回同一测试卷、重新取得相同版本 CLI，重复上述读操作。不能通过 `restart` 冒充 rootfs 删除，也不能把原本在 rootfs 上的 sqlite 目录额外挂成卷后宣称恢复成功。

工具使用 `Popen`、单调时钟、独立 stderr 文件、唯一请求 ID、有界 JSONL 读取和超时；只有对应 ID 的成功 response 才结束计时。它忽略通知、其他 ID 和服务端请求；无回复、错误回复、无效 JSON、过大单行、提前退出均有测试。清理从计时结果中分开，先关闭输入、再有界等待并按需终止/kill。环境白名单不会继承调用者的 API key 或 SQLite 覆盖。文件清单不跟随外部软链接。

测试命令：

```sh
python3 -m unittest discover -s scripts -p test_bench_agent_storage.py -v
```

七项回归测试通过。本地实际 app-server 流程已执行；仓库整体 UI/API、依赖安装与授权 repair 的验证由对应实施工作包提供，不能由此工具替代。

## E2B NFS 启动门控补验（2026-09-15）

在专用 Cloud 测试工作区中，Codex `0.154.0` 已通过真实安装和 `--version`，负载位于本地盘，Memoh 自身的 `/run/memoh/deps` 文件锁也正常。正确保持 stdin 的 app-server initialize 仍在持久 Home 上阻塞。以下都是未认证的初始化实验，不代表真实模型 turn：

| 实验 | 观察结果 |
| --- | --- |
| Home 保持 `/data/.codex` | `tmp/arg0/.../.lock` 的 `flock(LOCK_EX|LOCK_NB)` 不返回；30 s 内未完成 initialize |
| 新测试 Home 保持 `/data/.codex/agents/qa-arg0-probe`，仅 `tmp/arg0` 链接到本地 `/run` | arg0 锁立即成功，随后 `state_5.sqlite` 上的 `fcntl(F_SETLK)` 阻塞；34.75 s 后报告 SQLite runtime 初始化失败，initialize 仍未完成 |
| 全部 Home 放到专用本地测试目录 | initialize 在 0.8805 s 返回；这不满足持久 Home 的既定契约，未作为生产修复 |

精确版本源码支持这条失败链：

- [`arg0/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.154.0/codex-rs/arg0/src/lib.rs#L338-L385) 固定使用 `CODEX_HOME/tmp/arg0`。该路径不读取 `CODEX_TMPDIR`；`TMPDIR` 只参与整体 Home 的临时目录安全检查。
- [CLI 初始化顺序](https://github.com/openai/codex/blob/rust-v0.154.0/codex-rs/cli/src/main.rs#L1120-L1137) 先执行 arg0 setup，之后才解析 CLI 配置，因此 `sqlite_home`、`log_dir` 无法绕过第一处锁。
- arg0 内容是进程生命周期内的 helper 软链接与锁，可重建；[SQLite runtime](https://github.com/openai/codex/blob/rust-v0.154.0/codex-rs/state/src/sqlite.rs#L278-L289) 和 [thread writer 锁](https://github.com/openai/codex/blob/rust-v0.154.0/codex-rs/thread-store/src/local/writer_lock.rs#L89-L114) 另有持久/互斥语义。SQL 的 busy timeout 不提供 NFS 系统调用的硬超时。

**保留 Home 在当前 E2B NFS、只迁移 CLI 负载，未通过原生 Codex 启动验收。** 单独迁移 arg0 也不足以解决。此结果不授权迁移整个 Home、丢弃 SQLite、修改 NFS 挂载锁语义或切换生产模板；后续需要明确持久状态的存储契约并重新验证。未将这个测试软链接写入生产运行时代码，也没有读取或绑定现有 Agent 凭证。

## 尚未完成的性能与业务验收

- E2B 的实际模板、NFS mount、资源规格和 cold/warm 对照；安装阶段的下载、npm view、解压、发布、state 提交、GC 分解。
- Codex/Claude 的真实认证、模型 turn、token 刷新、会话继续，以及 workspace ready → repair → 首个事件的完整恢复耗时。
- SQLite 的 goal 以外业务内容恢复、正在执行操作时的 pause/resume 或崩溃恢复、并发沙箱写入约束。
- 官方 NFS 正确性边界。此处 ext4/overlay 的成功不证明生产 NFS 上 SQLite 的锁、WAL 或故障语义安全。

因此本报告完成了原生目录的能力核验、可重复握手测量与“本地 SQLite 丢失 goal”的故障证据；没有宣称达到 E2B 的 <3 s / <1 s 启动目标，也没有开启未经持久性验证的 SQLite 本地化。
