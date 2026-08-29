# Reasoning 收敛工程:工作文档

> 状态:**待团队确认后实施**。
> 前置阅读:[提案文档](./2026-08-10-reasoning-capability-architecture-proposal.md)(起因经过结果)。本文档是它的实施篇:精确的现状地图、目标结构、符号去向、分 PR 顺序。
> 现状地图来自两次全库普查(后端 Go / 前端 TS 各一次,2026-08-10,基于 PR #966 分支 + DeepSeek 前置修复)。

## 0. 一句话目标

把散在 **6 个后端位置 + 1 个前端文件** 的 reasoning 能力逻辑收敛为:**一个后端包做决策,一条 API 下发结果,前端只渲染**。同时清掉普查发现的 4 份词表拷贝、2 份重复实现、2 处死代码、若干现行 bug。

## 1. 现状地图(浓缩版)

完整地图很长,这里只放"决定架构"的部分。

### 1.1 逻辑散布在哪

| 位置 | 装了什么 | 问题 |
|---|---|---|
| `internal/models/types.go` | effort 词表、ThinkingMode、校验、NearestEffortToMedium、normalize | 定义层,基本合理,但和下面几处互相抄 |
| `internal/models/sdk.go` | ReasoningConfig 类型、wire 编码(BuildReasoningOptions 等) | wire 层,合理;但 `openAIWireEffort` 和 service.go 的 `normalizesMaxReasoningEffort` 是同一逻辑两份 |
| `internal/agent/application/service.go` | **全部调用时决策**:resolveReasoningConfig / pickEffort / offEffortFor / effectiveReasoningEfforts / anthropicEffortEra 等 7 个函数 | 住在一个 1594 行的编排文件里,但它们是纯函数,只依赖 models + 一个 settings 字符串——**放错了地方** |
| `internal/capabilities/` + `cmd/synccaps/` | build-time 能力发现(LiteLLM → YAML) | 自己维护了第三份 effort 顺序表 `effortOrder` |
| `internal/command/reasoning.go` | `/reasoning` 命令 | 自己维护第四份词表 `reasoningChoices`(用字面 `"off"`,少 minimal/max),完全不看模型 |
| `internal/settings/` | 存储层 | `isValidReasoningEffort` 与 models 包**同名不同义**(一个只拒空,一个严格词表) |
| `apps/web/.../reasoning-effort.ts` | 前端自己推导 mode/档位/off 可达性/迁移策略 | 两个 client-type 特判集合;desktop 复用 web,没有第三份 |

### 1.2 重复与平行实现(全部清单)

1. **effort 词表 4 份**:`models.validReasoningEfforts` / `capabilities.effortOrder` / `command.reasoningChoices`+`validEffort` / 前端 `KNOWN_EFFORTS`+`DECLARABLE_EFFORTS`。另有 i18n usage 文本硬编码词表(还写着已经不可选的 `none`,见 §5 bug 清单)。
2. **max→xhigh 归一化 2 份**:`service.go:normalizesMaxReasoningEffort`(列表过滤)与 `sdk.go:openAIWireEffort`(wire 兜底),注释自称 defence-in-depth,且两者都要求与前端 `MAX_NORMALIZED_CLIENT_TYPES` 同步——同一知识三处。
3. **ReasoningConfig 类型 2 个**:`models.ReasoningConfig`(现役 5 字段)与 `native/types.go:191-198` 的 `ReasoningConfig{Enabled,Effort}`——后者**全仓库无引用,是死代码**。
4. **`Model.SupportsReasoning`**(types.go:328)非测试代码已无调用方,被 `ResolveThinkingMode` 取代——平行 API 残留。
5. **legacy "off" 拼写桥 3 处**:`IsReasoningDisabled` 收 "none"、`normalizeAdvertisedEfforts` 重写声明、`botbackup/settings_compat` 收 `reasoning_enabled:false`。(这 3 处各管一个边界,保留,但要在同一个包里挨着放。)
6. **"Keep in sync" 注释后端 3 处 + 前端 5 处**,其中前端 1 处指向**已不存在的文件**(`internal/conversation/flow/resolver.go`)。

### 1.3 普查顺手发现的 bug(独立于重构,都要修)

| # | 位置 | 问题 | 严重度 |
|---|---|---|---|
| B1 | `native/spawn_adapter.go:127` | subagent spawn 链路只传 `ReasoningEffort` 一个字段,`Active/Disabled/Adaptive/OffEffort` 全丢——子代理的 reasoning 决策在传输中降级 | **高,先验证再修**(可能被下游重算掩盖) |
| B2 | `chat-pane.vue:1753` | 调 `availableEffortsForMode` 漏传第三参 clientType → 聊天页 Anthropic 的 Off 不显示,和 bots 设置页行为不一致 | 中(重构后此代码整体消失,若 PR B 排期近可不单修) |
| B3 | `internal/i18n/locales/*:cmd.reasoning.setUsage` | 提示词表写着 `off\|none\|low\|...`,但 `none` 实际已不可选 | 低 |
| B4 | 前端 `reasoning-effort.ts:29` | keep-in-sync 注释指向已删除的文件路径 | 低(重构后消失) |
| B5 | `apps/web` i18n | `bots.settings.reasoning*`、`bots.overview.reasoningBadge` 等死 key | 低,顺手清 |
| B6 | `conf/providers/minimax.yaml` | M2.5/M2.1-Lightning 标 `thinking_mode: toggle`+三档,但 models.dev/OpenRouter 双源说 M2.x always-on——存量数据可疑 | 中,需探针或官方文档确认后修数据 |

## 2. 目标结构

### 2.1 新包:`internal/reasoning`

决策逻辑收敛到一个**叶子包**(不 import models,函数收纯 string/slice,models 反过来 import 它做校验,避免环):

```
internal/reasoning/
├── vocabulary.go   # effort 常量、有序 tier 表、IsDisabled、IsValidDeclarable、
│                   # NearestToMedium、NormalizeAdvertised(自 models/types.go 迁入)
├── mode.go         # ThinkingMode 常量 + ResolveMode(mode, compats)(自 models/types.go 迁入)
├── resolve.go      # 调用时决策(自 agent/application/service.go 迁入):
│                   # ResolveConfig(mode, advertised, stored, requested, clientType) → *Config
│                   # 内含 pickEffort / effectiveEfforts / offEffortFor / anthropicEffortEra
├── options.go      # ★新增,第一步的核心:
│                   # OptionsFor(mode, advertised, clientType) → Options{
│                   #   Supported, CanDisable, Efforts []string, DefaultEffort }
│                   # 给 API 下发用;/reasoning 命令与 Web picker 的唯一数据源
├── wire.go         # Config 类型 + wire 编码(自 models/sdk.go 迁入 BuildReasoningOptions、
│                   # openAIEffortOptions、openAIWireEffort、anthropicLegacyBudget)
│                   # max→xhigh 归一收敛为一份,住在这里
└── *_test.go       # 各来源的测试跟随迁入合并
```

要点:

- **`options.go` 和 `resolve.go` 共享内部函数**——"picker 里能选什么"和"调用时怎么解析"从此在物理上不可能分叉,这是整个工程的核心保证。
- `models` 包保留 `ModelConfig` 存储结构和 `NewSDKChatModel`(provider 构造),校验改调 `reasoning.IsValidDeclarable`;老常量名(`models.ReasoningEffortDisable` 等)保留为**别名转发**,存量调用方不用一次性全改,后续 PR 渐进迁移。
- `capabilities.effortOrder`、`command.reasoningChoices`/`validEffort` 删除,改从 `reasoning` 包取。
- `settings.isValidReasoningEffort` 改名(比如 `hasReasoningEffortValue`)消除同名不同义。
- ACP 面(`acp/client/reasoning.go`)**不动**:它的 effort id 是 agent 自报的词表,和 models tier 本来就解耦,现状正确。

### 2.2 API:对齐 ACP 已有形态

普查的关键发现:**ACP 侧已经是终态形态**——`AcpclientReasoningState{available_efforts, current_effort, supported}`,前端消费路径也已存在。Native 模型对齐它:

models 相关响应(list/get)的每个 chat model 附带解析结果:

```json
"reasoning": {
  "supported": true,
  "can_disable": true,
  "efforts": ["low", "medium", "high"],
  "default_effort": "medium"
}
```

由 handler 调 `reasoning.OptionsFor(...)` 计算(client_type 服务端从 provider 拿,不再需要前端 join)。`efforts` 不含 disable token——可关闭性走 `can_disable` 布尔,哨兵值从 API 面消失。

### 2.3 前端:删推导,留渲染

| 现有符号(reasoning-effort.ts) | 去向 |
|---|---|
| `resolveThinkingMode` / `resolveEffortLevels` / `availableEffortsForMode` / `canTurnOff` / `nearestEffortToMedium` / `KNOWN_EFFORTS` / `DECLARABLE_EFFORTS` / `LEGACY_OFF_EFFORT` / `MAX_NORMALIZED_CLIENT_TYPES` / `IMPLICIT_OFF_CLIENT_TYPES` | **删除**,改读 API 的 `reasoning` 对象 |
| `EFFORT_LABELS` / `EFFORT_OPACITY` / `REASONING_EFFORT_DISABLE` | 保留(纯渲染映射 + 写库 token) |
| 换模型迁移策略(settings-interaction-card / chat-pane 两处 watch) | 简化为:选中值不在 `efforts` 里 → 取 `default_effort`(off 且 `can_disable` 则保留) |
| 三处 `activeClientType` computed(providers join) | 删除,不再需要 |

### 2.4 符号去向总表(后端)

| 现在 | 去向 | 动作 |
|---|---|---|
| `models/types.go` effort/mode 常量、词表、Nearest、normalize | `reasoning/vocabulary.go`、`mode.go` | 迁移+别名转发 |
| `models/sdk.go` `ReasoningConfig`、`BuildReasoningOptions`、`openAIEffortOptions`、`openAIWireEffort`、`anthropicLegacyBudget*` | `reasoning/wire.go` | 迁移(`NewSDKChatModel` 留在 models,import reasoning.Config) |
| `service.go` `resolveReasoningConfig`/`pickEffort`/`offEffortFor`/`effectiveReasoningEfforts`/`anthropicEffortEra`/`normalizesMaxReasoningEffort`/`hasEffort`/`reasoningEffortDisabled`/`offEffortOrEmpty` | `reasoning/resolve.go` | 迁移;service.go 只留一行调用 |
| `capabilities.effortOrder` | 删除 | 改用 reasoning 包的有序表(+disable 领头逻辑内联) |
| `command.reasoningChoices` / `validEffort` | 删除 | `/reasoning` 改调 `reasoning.OptionsFor`(顺带修复:它从此**按模型**给选项,而不是无条件 off+low~xhigh) |
| `native/types.go:191-198` 死 `ReasoningConfig` | 删除 | — |
| `Model.SupportsReasoning` | 删除 | 无调用方 |
| `native.RunConfig` 5 个扁平字段 + spawn_adapter | 改为直接携带 `*reasoning.Config` | 消除拆装往返,B1 一并修复 |

## 3. 分 PR 实施

### PR A:后端收敛(纯重构,行为不变)

1. 建 `internal/reasoning`,按 §2.4 迁移,测试跟随。
2. 删两处死代码、合并 max→xhigh、settings 改名、`RunConfig` 改携带 `*reasoning.Config`(先写 B1 的复现测试,确认现状是否真丢行为)。
3. `/reasoning` 命令接 `OptionsFor`(这一步有行为变化:选项按模型走,单独 commit,方便 review 时讨论)。

验收:`go build ./...`、全量 `go test`;`resolveReasoningConfig` 的表驱动测试**一个不改全绿**(纯函数搬家的最硬证据);`synccaps -check` 干净。

### PR B:API 下发 + 前端删推导

1. handler 给 models 响应加 `reasoning` 对象,swagger/SDK 重新生成。
2. 前端按 §2.3 删推导、改消费,B2/B4/B5 随之消失或顺手清。
3. i18n 死 key 清理 + B3 修正。

验收:vitest 全绿(推导类测试删除,渲染类保留改造);手工核对三个 surface(Web picker、chat composer、`/reasoning`)对同一批模型给出一致选项——拿 DeepSeek V4(可关)、MiniMax M2.5(不可关)、Claude 4.5(隐式可关)、Claude 4.6+(不可关)、Gemini(现状假档位,见开放问题 Q2)五个代表验。

### PR C / PR D:schema 升级、换源 models.dev

对应提案第二、三步,依赖 A/B 落地后开工,细节在提案文档里,届时再出各自的工作文档。

## 4. 开放问题(团队拍板)

1. **包名**:`internal/reasoning` vs `internal/models/reasoning`?本文按前者写(它是被 models import 的叶子包,平级更诚实),不强持。
2. **Gemini 怎么办**:查证结论——Gemini 官方支持调节(2.5 世代 `thinkingConfig.thinkingBudget` 预算制,3.x 世代 `thinking_level` 档位制),models.dev 数据也齐全(3.5-flash 给 `[minimal,low,medium,high]`,2.5-pro 给 `budget_tokens`);缺口在**我们自己的 Twilight AI Google provider**:它只处理 reasoning 输出流,请求侧完全不消费 `ReasoningEffort`,所以 `BuildReasoningOptions` 对 Google 返回 nil,picker 的三档是假控件。处置:(a) 给 Twilight AI 补 Gemini thinking 支持列为独立工作项(修根,工作量可估:3.x 映射 thinking_level,2.5 映射 effort→budget,参照 anthropicLegacyBudget 的模式);(b) 落地前 `OptionsFor` 对 Google 如实返回 `supported:false`——消失的是从未生效过的控件,用户实际损失为零。需要团队确认的只是 (a) 的排期。
3. **别名转发的清退节奏**:`models.ReasoningEffort*` 别名保留到什么时候?建议 PR C 时一并清,存量 import 改为直接用 reasoning 包。
4. **B1 的严重度**:spawn 链路丢字段如果确认影响子代理行为,是否要先出独立 hotfix 而不等 PR A?待复现测试给答案。

## 5. 不做什么(明确排除)

- 不动 ACP reasoning 面(已是正确形态)。
- 不动 DB schema 和存储语义(`reasoning_effort` 列、disable token 的存储形式,留给 PR C)。
- 不在这轮换数据源(留给 PR D)。
- 不动事件/展示层(ReasoningStart/Delta/End 流渲染,与能力解析无关)。
