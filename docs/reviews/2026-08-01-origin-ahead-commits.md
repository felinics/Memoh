# 本地领先 origin/main 的 11 个 Commit 审查报告

- 日期:2026-08-01
- 范围:`origin/main..HEAD`(11 commits,63 文件,+5192/-330)
- 审查维度:潜在 bug、抽象质量、hack / 坏味道、web 前端配置化潜力

```
98d26d8a fix(agent): validate direct vision image parts
a0e9e99a fix(agent): reject non-image vision payloads
6a1a07ba feat: harden Telegram turns and add vision fallback
da6bb033 fix(messaging): reject encoded structured messages
003253a5 fix(memory): lazily restore configured providers
9d637f86 fix(compaction): roll history into replacement summaries
012868c4 fix(discuss): honor configured compaction ceiling
e4befe52 fix(discuss): bound oversized group context
d13a11f5 fix(docker): bind split RPC listeners for compose
7b1f9317 fix(agent): avoid double-closing turn inject channel
f5d57af7 fix(workspace): avoid deadlock restoring preserved data
```

> **状态更新（2026-08-01）**：本报告发现的高危和中危问题已修复、测试并部署。下文保留修复前的审查结论与分析过程，实际落地情况见“第五部分：修复、验证与上线记录”。

**总体评价（修复前）**:工程质量整体较高——测试覆盖扎实(含 race、malformed 输入、并发去重),失败方向多为 fail-safe,SQL 层面(rolling compaction 的原子完成语句)设计严谨。但存在 **1 个高危功能性 bug**(rolling compaction 默认配置下必然失败)和若干中危并发/配置问题。

---

## 一、高危 / 中危问题(建议优先处理)

### 1. [BUG·高] Rolling compaction 在默认配置下必然失败

- 位置:`internal/agent/context/compaction/service_execution.go:85-101`、`internal/agent/application/service_compaction.go:88-89`
- 问题:输入预算方程不自洽。触发条件为 `inputTokens >= threshold`(默认 100000),而 rolling 输入预算 = `ModelContextTokens - SummaryTargetTokens - promptReserve`。按默认值 ratio=80 → `SummaryTargetTokens = 80000`,128K 窗口模型的输入预算 ≈ 46.7K,远小于触发瞬间的 ~100K 输入 → `service_execution.go:160-168` 每次硬报错 "rolling input exceeds model input budget"。即使 ratio=40,budget ≈ 87K 仍 < 100K 触发点。
- 后果:**除非 compaction 模型窗口显著大于 `threshold + SummaryTargetTokens`,该功能在默认设置下永远无法成功**;async 路径只在服务端日志报错,用户无感知(有 5 分钟失败冷却兜底,不会烧 token,但会话永远不压缩)。
- 建议:`SummaryTargetTokens` 改为相对模型窗口取值或加 `min()` 钳制;或在 settings 保存时校验 `threshold + summaryTarget ≤ model_window`。

### 2. [BUG·中] threshold=0 时手动 /compact 与 HTTP TriggerCompact 回归失败

- 位置:`internal/command/compact.go:117`、`internal/handlers/compaction.go:215`
- 问题:bot 关闭 compaction 或 threshold=0 时 `RollingSummaryTargetTokens` 为 0,`doCompaction` 开头直接报错 "rolling summary target must be positive"。改动前手动 compact 不依赖 threshold,属于回归。
- 建议:手动入口对 threshold≤0 给兜底 target(如模型窗口的一定比例)。

### 3. [BUG·中] ForceReply 标志与触发消息之间存在竞态

- 位置:`internal/channel/discuss/driver.go:137` + `worker.go:85`
- 问题:`NotifyRC` 每次全量覆盖 `sess.config`,而 worker 处理的是 drain 后的累积 RC。关键词消息 M1(ForceReply=true)入队后,另一条普通消息 M2 被采样命中(ForceReply=false)也入队,worker 用 M2 的 config 处理包含 M1 的 RC——M1 的强制回复指令和 fallback 静默丢失。反向时序(关键词消息最后到)恰好是想要的行为,所以问题被掩盖。
- 建议:把 ForceReply 作为 per-RC 属性随 rcCh 传递,而非放在可被覆盖的 session config 里。

### 4. [BUG·中] Vision fallback 重试不区分确定性错误且无任何退避

- 位置:`internal/agent/application/auxiliary_vision.go:128-158`
- 问题:对所有非 context 错误一律立即重试(最多 11 次),但多数失败是确定性的(模型解析失败、无 vision 能力、凭证解析失败),重试永远不会成功,且每次都重做凭证解析(可能触发 OAuth refresh)+ 完整 LLM 调用,无 backoff。
- 同时:`service.go:436-441` vision 调用无独立超时,与主 turn 共享 ctx 预算,慢视觉模型会直接推迟整个 turn 启动。
- 建议:区分可重试错误(429/5xx/网络)、加指数退避、加独立可配置 timeout。

### 5. [BUG·中] inject channel abort 路径残留 send-on-closed panic

- 位置:`internal/agent/application/turn_service.go:135-152` vs `internal/agent/runtime/session/control.go:400`
- 问题:7b1f9317 把关闭权收归 session runtime,消除了确定性 double-close(修复方向正确)。但 abort 类路径(`/stop`、`cancelRunControl`、`stopAllLocalControls`)会在 pump 仍在运行时关闭共享 channel,此时 `runHandle.Inject` 用另一把锁 `h.injectMu`,`h.injectClosed` 仍为 false,select 可能命中已关闭 channel 的发送分支 → panic 崩进程。窗口小,但未根治。
- 建议:channel 的 send/close 归同一把锁(或单一 owner)。

### 6. [BUG·中] `telegram-history-sync` 的 stderr 混入 stdout 污染 JSON 解析

- 位置:`cmd/telegram-history-sync/main.go:524-529`
- 问题:`dockerOutput` 把 `cmd.Stderr` 指向同一个 buffer,psql 任何 WARNING/NOTICE 都会混入输出,导致 `loadPreview`/`applyDeletion` 的 `json.Unmarshal` 失败且报文晦涩。对照同 commit 的 `telegram-discuss-rate` 是分离 stderr 的——两个新工具内部实现不一致。

### 7. [BUG·中] ollama.yaml 的 100 万 context_window 声明与防护目标冲突

- 位置:`conf/providers/ollama.yaml`(e4befe52 引入的 `deepseek-v4-flash:cloud`)
- 问题:声明 `context_window: 1000000`。`effectiveDiscussMessageTokenBudget` 在 bot 未开启 compaction 时直接用 `70% × 1M = 700K` 作为发送前上限——而 e4befe52 的动机恰恰是"provider 宣传的窗口大于端点实际接受",这个 1M 声明给 discuss 上下文膨胀重新开了口子。且该模型 ID 真实性待验证,若是预占位更应保守填值。

### 8. [BUG·中] `WithMaxTokens(SummaryTargetTokens)` 可能超出模型输出上限

- 位置:`internal/agent/context/compaction/service_execution.go:270-272`
- 问题:默认 80K 的 max_tokens 超过许多模型的最大输出(gpt-4o 16K、部分 Claude 64K),会被 API 400 拒绝并进入失败冷却循环。建议钳制到模型元数据的 max output tokens。

---

## 二、低危问题与坏味道

### Bug 类(低)

| 位置 | 问题 |
|---|---|
| `internal/channel/discuss/forced_reply.go:65` | `NO_REPLY` 抑制用 `EqualFold` 精确匹配,而 inbound 路径(`channel.go:2650-2660`)是前缀/后缀容忍匹配。模型输出 `"NO_REPLY."` 时,discuss fallback 会把原文发给用户。两处逻辑应统一。 |
| `internal/channel/discuss/forced_reply.go:31-38` | fallback 直发绕过 messaging executor 管线:不经过 inline tag 提取(附件/反应/TTS 标记会原样泄漏给用户);`ReplySender.Send` 在 worker 主循环同步执行且无超时,慢网络阻塞整个 discuss session。 |
| `internal/channel/inbound/channel.go:1648-1650` + `turn_discuss.go:19` | 任何显式触发(@提及、回复、私聊)都注入"matched a configured force-reply keyword"的 system 指令,对非关键词触发的 turn 文案与事实不符,误导模型。 |
| `internal/workspace/dataio.go:271` vs `:253` | 备份文件删除失败处理不一致:gRPC 路径会把删文件失败放大为整个 Start 失败(数据已恢复成功),mount 路径静默忽略。两条路径都有残留备份导致下次 Start 重复恢复的风险。 |
| `internal/memory/adapters/service.go:319-328` | Update 的 evict 与 reinstantiate 是两次独立加锁,存在极窄的过期配置竞态(后果为下一次 Get 用到更新前配置)。 |
| `internal/agent/application/service_history.go:292-299` | token 估算口径不统一:timeline/discuss 用 bytes/2,遗留 chat 路径仍用 len/4,CJK 会话在回退路径下压缩触发低估约 2 倍。 |
| `cmd/telegram-discuss-rate/main.go:365-369` | `keyword-add` 幂等时不做任何写操作但仍打印"已添加",用户无法区分。 |
| `cmd/telegram-history-sync/main.go:287` | apply 大事务默认超时 2 分钟偏紧,巨型群会话级联删除可能超时。 |

### Hack / 抽象类

| 类别 | 位置 | 问题 |
|---|---|---|
| [HACK·低] | `internal/config/config.go:53` vs `auxiliary_vision.go:19` | 默认 vision prompt 双份且互相矛盾(中文详细版 vs 英文简版);更糟的是 `conf/app.example.toml:75` 显式写 `auxiliary_vision_prompt = ""`,拷贝示例的用户得到英文兜底,缺省用户得到中文,中文默认值被自家示例架空。 |
| [HACK·低] | `auxiliary_vision.go:206-208` | 硬编码中文 user prompt 且不可配置(`Prompt` 配置只控制 system 部分)。 |
| [HACK·中] | `forced_reply.go:17`、`channel.go:1646`、`types.go:48-52` | Telegram 平台硬编码散落在通用 discuss 子系统四处。建议抽象为 per-platform discuss policy(map 或接口)。 |
| [抽象·中] | `internal/agent/application/service_compaction.go:100,212` | 旧的分段压缩路径(`runCompactionSync`、`syncCompactionTargetTokens`、`splitByRatio` 等)已成为生产死代码,仅测试覆盖。建议注释说明或清理。 |
| [抽象·中] | `auxiliary_vision.go:175-204` | 模型解析/凭证/SDK 构造逻辑几乎逐行复制 `buildBaseRunConfig`(service.go:662-690),应抽公共 helper。 |
| [抽象·中] | `cmd/telegram-discuss-rate/`(529 行) | 全部功能就是读写 `bots.metadata` 两个 key,而 server 已有 bot 更新 API 且前端有写 metadata 先例。直连 DB 的做法把 metadata key 契约泄露到运维 CLI,且依赖"server 不缓存 metadata"这一实现细节。**web 化后应废弃此工具。** |
| [抽象·低] | `internal/agent/context/compaction/selection.go:215,222` | token 估算常量三处拷贝(/2、/2、/4),长期会再次漂移,建议收敛为共享常量。 |
| [抽象·低] | `capability_policy.go:102-119` | "dataURL MIME 优先于声明 MIME"判定逻辑第三处拷贝,可抽共用 helper。 |
| [抽象·低] | `service_compaction.go:23-26` | `effectiveCompactionThreshold` 沦为空壳(`_ = contextTokenBudget` 后直接返回 threshold),建议内联删除;`triggerDiscussCompaction(ctx, cmd, 0)` 的第三参数已成死参数。 |
| [HACK·低] | `service_execution.go:287` | `completeErr := error(nil)` 非惯用,`var completeErr error` 即可。 |
| [HACK·低] | 多处 | 幻数:`promptReserve = window/100`(下限 1024)、`compactionBudgetThresholdPercent=70`、`compactionFailureCooldown=5min`、`maxCompactTokens` 兜底 30000、`defaultModelContextTokenBudget=128000`、`DefaultTelegramDiscussPassiveSampleRate=0.25`、`auxiliaryVisionMaxOutputTokens=8192`。 |
| [HACK·低] | `turn_service.go:114-116, 217-220` | 注释漂移:仍写 "the pump closes the channel",与 disableInject 新语义不符。 |
| [HACK·低] | e4befe52 / 6a1a07ba | 提交夹带不相关变更:ollama.yaml 新模型混在 discuss 修复里;两个共 1450 行的运维工具混在 "harden Telegram turns" 里。建议拆分提交。 |
| [抽象·低] | `cmd/internal/channel/providers.go:505` | `TelegramDiscussPolicy` 每条被动 Telegram 消息触发一次 `bots.GetForAccess` DB 查询,高频群聊有读放大,适合短 TTL 缓存。 |
| [抽象·低] | `driver_artifacts_test.go:198` | `TestBuildForceReplyMarksDiscussTurnAddressed` 与 artifacts 无关,放错文件。 |

### 已确认无问题的重点项

- **9d637f86 SQL 层**:`CompleteRollingCompactionLog` 单语句原子完成(child 置 ok + parents 置 superseded),`FOR UPDATE` 锁、validated CTE 校验、并发竞态走 error 完成路径可 reclaim,无脏数据;`0001_init.up.sql` 已同步。
- **stale-frontier 机制**:排队 async 触发在 DB 变更/LLM 调用前检测 frontier 前进并 noop,无假阴性。
- **f5d57af7 workspace 死锁修复**:两条重入路径均消除,语义保持一致,测试有效。
- **a0e9e99a / 98d26d8a vision MIME 防线**:dataURL 实际 MIME 优先于声明 MIME,三个入口布置完整,修复正确。
- **da6bb033 结构化消息拦截**:启发式严谨(marker 带闭合引号防前缀碰撞),发送前拦截无副作用,错误闭环正确。
- **d13a11f5 RPC listen addr**:方向正确,compose 未 publish 端口 + shared secret,安全性可接受。
- **003253a5 memory 惰性加载**:instantiate-once 语义正确,有并发测试覆盖。
- **7b1f9317 主路径**:正常结束路径 defer LIFO 顺序保证无 send-on-closed(残留 abort 路径问题见上)。

---

## 三、Web 前端配置化分析

### 现状事实

- Bot settings API(`internal/handlers/settings.go`)已支持 compaction 四字段、discuss_probe_model_id 等;`bot-compaction.vue` 已有 UI。
- Bot metadata(`bots.metadata` JSONB)可通过现有 bot 更新 API 写入,前端已有先例(`bot-skills.vue:570`)。**无需新 DB 列。**
- **不存在全局(server 级)配置页面/API**——server 级配置目前只有 TOML+env。
- ⚠️ 若 vision 配置要做成 web 热更新,必须先解决 `Service.auxiliaryVision` 无锁读写问题(`auxiliary_vision.go:65-70`),当前仅启动时写所以安全。

### 配置项清单与建议

| 配置项 | 当前位置 | 适合 web 化 | 需要的工作 |
|---|---|---|---|
| `telegram_discuss_passive_sample_rate`(默认 0.25) | bots.metadata,仅 CLI 直改 DB | **非常适合(优先级最高)** | 零 DB 变更:复用 bot 更新 API 写 metadata;前端 bot 设置加数字输入 + i18n。更规范做法:settings.UpsertRequest 加显式字段 + swagger/sdk 生成 |
| `telegram_discuss_force_reply_keywords` | bots.metadata,仅 CLI | **非常适合(优先级最高)** | 同上,前端加 tags input 字符串列表编辑器 |
| `agent.auxiliary_vision_model` / `_provider` / `_prompt` / `_max_retries` | TOML + env(进程级全局) | 适合(中等工程) | 需新建全局配置存储 + API + 页面;或改为 per-bot 放进 settings(语义更清晰)。落地前先给 auxiliaryVision 加锁 |
| compaction ratio 的 UI 文案 | 已在 bot-compaction.vue | **需要修正** | ratio 语义已从"压缩最旧 N%"变为"滚动摘要目标占 threshold 的比例",现有 `compactionRatioDescription` 文案过时且误导;默认值 80 在新语义下产生 80K max_tokens(见高危项 #1/#8),建议改默认值或改为独立的"摘要目标 tokens"字段并钳制 |
| SummaryTargetTokens | 由 threshold×ratio 派生 | 适合 | bot-compaction.vue 暴露为可选覆盖项,按模型窗口钳制 |
| vision 超时 / 图片大小限制 / `auxiliaryVisionMaxOutputTokens=8192` | 硬编码 | 低优先 | 可并入 vision 全局配置项 |
| 70% 窗口预留 / 5min 失败冷却 / 128000 窗口兜底 / 30000 输入兜底 | 硬编码常量 | 否(或放 TOML) | 如需暴露,放 `conf/app.*.toml` 而非 bot 级 UI |
| RPC listen addr(env) | docker-compose / env | 否 | 部署/网络层,改错会断内部 RPC |
| MCP federation 超时(60min/60s) | 硬编码 | 否 | 协议层超时,默认合理 |
| `defaultModelContextTokenBudget=128000` | 硬编码 | 否 | 模型 context_window 已可在 model-setting.vue 配置,兜底值属代码健壮性 |
| compaction artifact 日志 lineage | DB 已有 artifact_level/parent_ids | 可增强 | logs 列表补充展示 rolling lineage,便于排查 |

### 结论

1. **最高 ROI**:`telegram_discuss_passive_sample_rate` 与 `telegram_discuss_force_reply_keywords` 是唯一"明明有现成 API 通道却被做成 CLI 直改 DB"的两项。web 化成本最低(零 DB 变更)、收益最大(同时可废弃 529 行的 `cmd/telegram-discuss-rate`)。
2. **次优先**:auxiliary vision 四项(全局开关 + 模型选择),前提是先解决配置热更新的并发安全。
3. **顺手修正**:bot-compaction.vue 的 ratio 文案与默认值,配合高危项 #1 的修复一起做。

---

## 四、详细修复计划

按优先级分四个阶段。每项包含:根因、修复步骤(文件/函数)、验证方式、工作量预估。前置依赖已标注;无依赖的项可并行。

### 阶段 P0:compaction 正确性(问题 #1、#2、#8)

> 这三项同属 rolling compaction 预算体系,建议一个 PR 内一起修,避免预算方程改了又改。

#### P0-1 修复 rolling 输入预算不自洽(问题 #1)

- **根因**:`SummaryTargetTokens` 由 `threshold × ratio` 派生(与模型窗口无关),而输入预算 = `ModelContextTokens - SummaryTargetTokens - promptReserve`。默认 100K threshold + 80 ratio + 128K 窗口 → 预算 47K < 触发点 100K,必败。
- **修复步骤**:
  1. `internal/agent/application/service_compaction.go` `RollingSummaryTargetTokens`:派生值增加上界钳制 `min(threshold*ratio/100, ModelContextTokens*maxSummaryWindowFraction)`,其中 `maxSummaryWindowFraction` 取常量 0.25(摘要目标不超过窗口 1/4)。注意调用方需传入模型窗口——检查所有调用点(`service_compaction.go`、`command/compact.go`、`handlers/compaction.go`)统一签名。
  2. `service_execution.go:85-101`:当钳制后 `inputBudget < maxCompactTokens` 时,把 `maxCompactTokens` 降为 `inputBudget`(现有逻辑已做)并**降级为告警日志而非必败**;仅当 `inputBudget <= promptReserve` 才硬报错。
  3. `service_execution.go:160-168` 的硬报错保留(真超限仍报错),但报错文案补充当前 threshold/ratio/窗口值,便于排查。
  4. settings 保存路径(`internal/settings/`)增加校验:`compaction_threshold` 不得超过 compaction 模型窗口的 50%,超限返回 400(走 apperror 稳定 code,参考 `.agents/skills/memoh-error-handling`)。
- **验证**:新增表驱动测试覆盖(128K 窗口 + 100K threshold + ratio 80)、(1M 窗口 + 100K threshold)、(32K 小窗口)三组;跑 `go test ./internal/agent/context/compaction/... ./internal/agent/application/...`。
- **工作量**:0.5~1 天。

#### P0-2 修复手动 compact 在 threshold=0 时回归(问题 #2)

- **根因**:`command/compact.go:117`、`handlers/compaction.go:215` 用 `RollingSummaryTargetTokens(threshold, ratio)` 派生目标,threshold=0 → 0 → `doCompaction` 报错。
- **修复步骤**:手动入口(compact 命令 + HTTP trigger)在派生值 ≤ 0 时回退为 `ModelContextTokens × 0.25`(与 P0-1 的钳制常量一致),抽一个共享 helper `ManualSummaryTargetTokens(modelWindow int) int` 放 `service_compaction.go`。
- **验证**:为两个入口各加一个 threshold=0 的测试;手动跑 `/compact` 冒烟。
- **工作量**:半天。依赖:P0-1(共享钳制常量)。

#### P0-3 钳制 `WithMaxTokens` 不超过模型输出上限(问题 #8)

- **根因**:`service_execution.go:270-272` 直接把 `SummaryTargetTokens`(默认 80K)传给 `WithMaxTokens`,超模型 max output 被 API 400。
- **修复步骤**:
  1. 检查模型元数据(`internal/models/types.go` / capabilities 包)是否已有 max output tokens 字段;有则钳制 `min(SummaryTargetTokens, modelMaxOutput)`,无则先用保守常量 16384 并在 TODO 注明接入模型元数据。
  2. P0-1 的窗口钳制(≤ 窗口 1/4)叠加后,多数情况自然落在安全区。
- **验证**:mock SDK 断言 `WithMaxTokens` 入参不超过钳制值。
- **工作量**:半天。依赖:P0-1。

### 阶段 P1:并发与健壮性(问题 #3、#5、#4)

#### P1-1 修复 ForceReply 的 config 覆盖竞态(问题 #3)

- **根因**:`DiscussSessionConfig.ForceReply`(driver.go:62)是会话级字段,`NotifyRC` 每次全量覆盖 `sess.config`(driver.go:137),而 worker 处理的是 drain 后的累积 RC——后到的普通消息会冲掉先到关键词消息的 ForceReply。
- **修复步骤**(已核实代码结构):
  1. 把 `rcCh` 的元素类型从 `timeline.RenderedContext` 改为结构体 `discussEnvelope{ RC timeline.RenderedContext; ForceReply bool }`,`NotifyRC` 入队时把本次 config 的 ForceReply 快照进 envelope(driver.go)。
  2. worker drain 累积时,合并策略改为 `forceReply = OR(所有 envelope.ForceReply)`——任何一条被覆盖消息要求强制回复,本批次就强制回复。
  3. `trigger.Build`(trigger.go:28,66)改用合并后的值而非 `cfg.ForceReply`;`sessionConfigSnapshot` 中该字段保留给 fallback 判定,但触发判定不再依赖它。
  4. 同步更新 `turn.go:121` 的注释(语义从"针对最新消息"变为"针对本批次任一消息")。
- **验证**:新增测试模拟 M1(ForceReply=true)+ M2(false)按序入队、worker 单批次处理,断言 `plan.command.DiscussForceReply == true`;跑 `go test -race ./internal/channel/discuss/...`。
- **工作量**:1 天。

#### P1-2 根治 inject channel 的 send-on-closed(问题 #5)

- **根因**:`runHandle.Inject`(turn_service.go:135-152)用 `h.injectMu` 保护,而 channel 关闭权在 session runtime(`control.go` `closeInject`),两把锁;abort 路径先关 channel,`Inject` 的 select 仍可能命中发送分支 → panic。
- **修复步骤**:
  1. 首选方案:发送方不再直接 send,改为 `select { case ch <- msg: ... case <-runDoneCh: }`,其中 `runDoneCh` 由 runtime 在 `closeInject` **之前**关闭(close 顺序:先 close(done) 再 close(inject),同一把 runtime 锁内)。这样 done 关闭后 Inject 必走 done 分支,不会碰已关闭的 inject channel。
  2. 备选(更小改动):`Inject` 内 `defer recover()` + 记录 warn——不推荐作为正式修复,仅作兜底。
  3. 顺手修正注释漂移(turn_service.go:114-116、217-220)。
- **验证**:并发测试循环 `Inject` + `cancelRunControl` 1000 次跑 `-race`;`go test -race ./internal/agent/application/... ./internal/agent/runtime/session/...`。
- **工作量**:1 天。

#### P1-3 vision fallback 重试退避与独立超时(问题 #4)

- **根因**:`retryAuxiliaryVision`(auxiliary_vision.go:128-158)对所有错误立即重试;调用点(service.go:436-441)无独立超时。
- **修复步骤**:
  1. 定义哨兵错误类型区分确定性失败(模型解析失败、无 vision 能力、凭证解析失败——auxiliary_vision.go:180-190 三处)与可重试失败;确定性错误直接返回不重试。
  2. 可重试错误加指数退避:`500ms × 2^n`,上限 8s,尊重 ctx 取消。
  3. 调用点包一层 `context.WithTimeout(ctx, auxiliaryVisionTimeout)`,常量先取 60s,放入 TOML 配置项(并入阶段 P4 的 vision 配置)。
  4. 顺手去重:抽 `resolveVisionModelConfig()` helper 消除与 `buildBaseRunConfig`(service.go:662-690)的复制;删除双份默认 prompt 之一(保留 config.go 的中文版,删掉 auxiliary_vision.go:19 的英文版);user prompt 改为可配置或并入 system prompt。
  5. 修正 `conf/app.example.toml:75` 的 `auxiliary_vision_prompt = ""` 架空默认值问题:示例里改为注释掉的示例值。
- **验证**:确定性错误断言重试次数=0;可重试错误断言退避间隔;`go test ./internal/agent/application/...`。
- **工作量**:1 天。

### 阶段 P2:运维工具与配置修正(问题 #6、#7)

#### P2-1 修复 telegram-history-sync stderr 污染(问题 #6)

- **修复**:`dockerOutput`(cmd/telegram-history-sync/main.go:524-529)分离 stderr 到独立 buffer,仅在出错时附加到错误信息(对齐 telegram-discuss-rate 的 `psql` 实现)。同时把 `--timeout` 默认值从 2min 提到 10min。
- **验证**:构造 psql 输出带 WARNING 的集成测试;`go test ./cmd/telegram-history-sync/...`。
- **工作量**:半天。

#### P2-2 修正 ollama.yaml 1M 窗口声明(问题 #7)

- **修复**:`conf/providers/ollama.yaml` 的 `deepseek-v4-flash:cloud`:核实模型真实存在性与实际上下文窗口;无法核实则降到保守值(如 128000)或先移除该条目。同时在 `effectiveDiscussMessageTokenBudget`(turn_discuss.go:384-392)加一道硬上限(如 256K),声明窗口再大也不突破——防"窗口虚标"的最后一道闸。
- **验证**:表驱动测试覆盖超大声明窗口场景。
- **工作量**:半天。

### 阶段 P3:低危问题与坏味道清理

可拆成若干小 PR,按主题分组:

| 组 | 内容 | 工作量 |
|---|---|---|
| NO_REPLY 统一 | 抽共享抑制函数(inbound 前缀/后缀容忍版),`forced_reply.go` 与 `channel.go` 统一调用;fallback 发送前复用 inbound 清洗管线 | 0.5 天 |
| discuss fallback 加固 | `sendReplyFallback` 加 `context.WithTimeout`(30s);force-reply system 指令按触发来源区分文案(关键词/@提及/私聊) | 0.5 天 |
| token 估算统一 | 抽共享常量/函数,收敛 `timeline`(/2)、`turn_discuss`(/2)、`compaction/selection.go`(/4)、`service_history.go`(/4)四处 | 0.5 天 |
| workspace 备份删除 | `dataio.go:271` 改为 warn 日志 + 不使 Start 失败,与 `:253` 对齐 | 1 小时 |
| 平台策略抽象 | `forced_reply.go:17`、`channel.go:1646`、`types.go:48-52` 的 Telegram 硬编码收敛为 per-platform discuss policy 接口 | 1 天 |
| 死代码清理 | `runCompactionSync`、`syncCompactionTargetTokens`、`effectiveCompactionThreshold` 及 selection.go 非 rolling 分支:确认无回退需求后删除,或加注释明确保留理由 | 0.5 天 |
| 小项 | `error(nil)` → `var err error`;turn_service 注释漂移(随 P1-2);`driver_artifacts_test.go:198` 测试挪文件;memory Update 竞态合并为单次持锁;`TelegramDiscussPolicy` 加短 TTL 缓存 | 0.5 天 |
| 工具修复 | telegram-discuss-rate `keyword-add` 幂等时输出"已存在,未变更";若完成 P4-1 则整个废弃该工具 | 1 小时 |

### 阶段 P4:web 前端配置化

#### P4-1 bot 设置页:discuss 采样率 + 强制回复关键词(最高 ROI)

- **后端**:
  1. 方案 A(零 DB 变更,快):复用现有 bot 更新 API 的 `Metadata` 合并写入(`internal/bots/types.go:55-62`),前端直接读写 `telegram_discuss_passive_sample_rate` / `telegram_discuss_force_reply_keywords` 两个 key。
  2. 方案 B(规范,推荐):`internal/settings/types.go` 的 `UpsertRequest` 加两个显式字段,handler 内部落到 metadata;`mise run swagger-generate && mise run sdk-generate` 更新 SDK。
- **前端**(遵循 `apps/web/AGENTS.md` 与 `packages/ui/AGENTS.md` 设计契约):
  1. `apps/web/src/pages/bots/components/` 新增 `bot-discuss.vue`(或并入现有 bot 设置分组):采样率用 slider/number input(0~1,默认 0.25),关键词用 tags input。
  2. i18n 文案(中英日)说明"采样率越高 token 消耗越大"。
  3. 仅在 bot 绑定 Telegram channel 时展示该分组。
- **收尾**:废弃 `cmd/telegram-discuss-rate`(仓库删除或文档标记 deprecated)。
- **工作量**:1~2 天。

#### P4-2 bot-compaction.vue 文案与默认值修正

- 配合 P0-1:`compactionRatioDescription` 改为描述新语义("滚动摘要目标占阈值的比例"),或改为独立的"摘要目标 tokens"输入(带模型窗口上界校验提示)。
- 默认值 80 建议下调(如 25,与新钳制常量一致)。
- **工作量**:半天(含 i18n)。依赖:P0-1。

#### P4-3 auxiliary vision 配置 web 化(次优先)

- **前置**:给 `Service.auxiliaryVision` 读写加锁/atomic(auxiliary_vision.go:65-70)。
- **方案**:改为 per-bot settings 字段(vision model/provider/prompt/max_retries/timeout,空则回落 TOML 全局默认),复用现有 settings API + bot 设置页;模型选择复用现有 models CRUD 的引用选择器。
- **工作量**:2~3 天。依赖:P1-3。

### 建议执行顺序

```
P0-1 → P0-2 → P0-3        (一个 PR,compaction 预算体系)
P1-1, P1-2, P1-3          (可并行三个 PR)
P2-1, P2-2                (可并行小 PR)
P3                        (按主题拆小 PR,随时穿插)
P4-1 → P4-2 → P4-3        (P4-2 依赖 P0-1,P4-3 依赖 P1-3)
```

每阶段完成后跑 `mise run lint` 与相关包 `go test -race`;涉及 API 变更的跑 `mise run swagger-generate && mise run sdk-generate`。

---

## 五、修复、验证与上线记录（2026-08-01）

本节记录本轮审查后的实际处理结果。上文的问题描述和修复计划是修复前的快照，若与本节状态冲突，以本节为准。

### 已完成的修复

| 范围 | 处理结果 |
|---|---|
| Rolling compaction（问题 #1、#2、#8） | 摘要目标统一钳制为 `threshold × ratio`、模型窗口的四分之一和 16,384 tokens 三者中的最小值；手动压缩在 threshold 关闭时使用独立兜底；小窗口模型会自动收紧触发阈值，执行层仍保留最终预算校验。 |
| ForceReply 竞态（问题 #3） | `ForceReply` 改为随每条 discuss 消息入队，同一批消息按 OR 合并。先到的关键词消息不会再被后到的普通消息覆盖。强制回复提示也改为通用的“显式触发”语义，不再把 @提及或私聊误写成关键词命中。 |
| Vision fallback（问题 #4） | 确定性配置错误不再重试；仅网络错误、429 和可识别的 5xx 错误进入指数退避，间隔从 500 ms 增长到最多 8 s。视觉识别增加独立超时，默认 60 s，可通过 `agent.auxiliary_vision_timeout` 配置；默认提示词与输出上限也已收敛到共享定义。 |
| Inject 生命周期（问题 #5） | inject 的发送、关闭和运行状态判断收归 session runtime，在同一生命周期锁下完成；补充并发取消与注入测试，消除 abort 路径的 send-on-closed 窗口。 |
| Telegram history 工具（问题 #6） | stdout 与 stderr 分离，psql 的 warning/notice 不会再污染 JSON；默认执行超时从 2 分钟提高到 10 分钟。 |
| 超大模型窗口（问题 #7） | [Ollama 官方模型页](https://ollama.com/library/deepseek-v4-flash%3Acloud)可核实该 cloud 模型确实存在，因此未删除现有 100 万 context window 元数据；同时给 discuss 上下文增加 256K tokens 的硬上限，避免端点能力与声明不一致时继续放大请求。 |
| 低风险正确性问题 | 统一 `NO_REPLY` 识别，允许大小写差异及句首、句尾标点；fallback 发送增加 30 s 超时；token 估算统一为保守的 2 bytes/token；workspace 备份清理失败改为告警而不是启动失败；memory provider 替换改为原子更新；幂等的关键词添加会明确报告“未变更”。 |
| Web 配置 | Telegram channel 设置页已支持被动采样率和强制回复关键词，复用现有 bot metadata API，不增加数据库字段；中、英、日文案已补齐。Compaction ratio 文案也已改为当前的“摘要目标比例”语义。 |

### 验证结果

- `go test ./...`：仓库跟踪的 Go 源码全量测试通过。
- `go test -race ./internal/agent/application ./internal/agent/runtime/session ./internal/channel/discuss ./internal/memory/adapters`：并发重点包通过 race detector。
- `golangci-lint run --new-from-rev=HEAD`：本次涉及的 Go 包无新增 lint 问题。
- Web 生产构建通过；改动的 Vue 文件通过 ESLint，UI contract 检查通过。
- `git diff --check` 通过，未发现空白符错误。

全仓库 `golangci-lint run ./...` 仍会报告历史遗留问题，本轮只保证没有为改动范围引入新的 lint 错误。

### 部署结果

| 项目 | 结果 |
|---|---|
| Server 镜像 | `memohai/server:latest`，镜像 ID `e311f14b1eb9` |
| Web 镜像 | `memohai/web:latest`，镜像 ID `0963c0ff6635` |
| 回滚镜像 | `memohai/server:rollback-20260801-1728`、`memohai/web:rollback-20260801-1728` |
| 重启顺序 | `server` → `channel` → `web`；PostgreSQL 和 pgvector 未重启 |
| 健康检查 | Server、Channel、Web 均为 healthy，容器重启次数均为 0；Server `/health`、Channel health、Web `/health` 和 Web 代理的 Swagger 请求均返回 200 |
| 日志检查 | 上线后的 error/fatal/panic 扫描未发现新增异常 |

本次镜像在提交前由同一份代码工作树构建，Server 内嵌版本标识为 `v0.16.0-81-g98d26d8a-review.20260801`，commit 标识为 `98d26d8a5a25-dirty-review`。文档提交不影响运行时二进制内容。

### 后续事项

以下内容不影响本轮高危和中危问题的修复结论，留作后续重构或体验优化：

- discuss fallback 仍可进一步接入统一 messaging executor，避免 inline tag 处理逻辑分叉。
- Telegram discuss 策略仍散落在通用 channel/discuss 代码中，可收敛为 per-platform policy。
- 旧的分段 compaction 路径仍保留，可在确认不再需要回退后删除。
- Auxiliary vision 的 provider/model 构造仍可继续去重；若要支持 Web 热更新，还需要补充全局或 per-bot 配置 API。
- Compaction artifact lineage 暂未在 Web 日志页展示。
- `telegram-discuss-rate` CLI 暂时保留；待 Web 配置稳定后可标记弃用或移除。
